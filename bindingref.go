// bindingref.go — cross-binding wiring at render time.
//
// Closes idefxH/rda-cli#96 (the dex+postgres pain). When a chart needs
// another binding's host/port/credential at install time (dex's
// `storage.type: postgres` needs the postgres binding's host, port,
// database, user, password baked into `passthrough.config.storage.config.*`),
// the dev hand-rolls magic strings into passthrough — knowing the
// release name + the binding-secret naming convention — and edits by
// hand. Footgun-ridden.
//
// Solution: ${binding:NAME.field} substitution at render time. Walk
// services[] to build a binding name → fields map, then resolve any
// reference encountered in scaffold defaults / passthrough values.
//
// Recurring cases this unblocks:
//   - dex → postgres / mariadb (state backend)
//   - grafana → prometheus (datasource URL)
//   - opentelemetry-collector → tempo + loki + prometheus (exporters)
//   - app → vault (token at boot)
//   - app → kafka schema registry
//
// Syntax:
//   ${binding:NAME.field}     — reference another binding's field
//   ${binding-self:field}     — reference the current entry's own field
//
// Available fields: host, port, url, username, password, database,
// type, secret_name. The set is intentionally narrow; cross-binding
// for chart-specific knobs goes through the chart's own `passthrough`
// section, not here.
//
// Validation: a reference to an unknown binding OR an unknown field
// returns a render-time error listing the available bindings/fields.
package render

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"text/template"

	"github.com/idefxH/rda-render-engine/dslmapping"
)

// readSecretViaKubectl reads a K8s Secret and returns its decoded data keys.
// Returns nil if kubectl fails (no cluster, Secret not found, timeout).
func readSecretViaKubectl(secretName, namespace, branch string) map[string]string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	args := []string{"get", "secret", secretName, "-o", "json"}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	out, err := exec.CommandContext(ctx, "kubectl", args...).Output()
	if err != nil {
		return nil
	}
	var secret struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(out, &secret); err != nil {
		return nil
	}
	decoded := map[string]string{}
	for k, v := range secret.Data {
		raw, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			continue
		}
		decoded[k] = string(raw)
	}
	if branch == "" {
		return decoded
	}
	// DO-0009: multi-branch secrets. Filter keys by b<branch>. prefix.
	prefix := "b" + strings.ReplaceAll(branch, ".", "-") + "."
	filtered := map[string]string{}
	for k, v := range decoded {
		if strings.HasPrefix(k, prefix) {
			filtered[strings.TrimPrefix(k, prefix)] = v
		}
	}
	if len(filtered) > 0 {
		return filtered
	}
	// No branch-prefixed keys found — fall back to unprefixed keys.
	// This supports secrets that don't use the multi-branch format.
	return decoded
}

// bindingRefRegex matches `${binding:NAME.field}` and `${binding-self:field}`.
// Captures: group 1 = "binding" or "binding-self", group 2 = ref payload.
var bindingRefRegex = regexp.MustCompile(`\$\{(binding(?:-self)?):([^}]+)\}`)

// BindingFields is the projection of a services[] entry that other
// entries can reference via ${binding:NAME.field}. The set is
// deliberately narrow — extending it requires a doc + scaffold
// migration.
//
// Multi-port charts (minio, dex, prometheus-with-subcharts) expose
// each port via the Ports map. The PRIMARY port is also surfaced via
// the short Host/Port/URL fields — so dev apps can reference the
// principal endpoint without naming the port. Secondary ports
// require explicit naming: ${binding:minio.console_url}.
type BindingFields struct {
	Host       string
	Port       string
	URL        string
	Username   string
	Password   string
	Database   string
	Type       string
	SecretName string
	// Ports maps port name → resolved fields. Always non-empty when
	// the chart's catalogue entry declared a service.
	Ports map[string]PortFields

	// Computed is the bag of chart-author-defined extra fields
	// declared in dsl-mappings.yaml under
	// `charts.<type>.versions[N].binding_fields:`. Populated at
	// collectBindings time by rendering each spec's template against
	// the same inputs as DerivedValues (.Service, .Release.Name,
	// .Binding, .Type). Reached via Get(<field>) AFTER the hardcoded
	// fields fail to match — so chart-defined names cannot collide
	// with the well-known set (host, port, url, username, password,
	// database, type, secret_name, *_port, *_url). Phase 1 #2 of
	// Design Orientation 0001 Appendix A.
	Computed map[string]string

	// Secret holds the resolved values of every binding_secret[] key
	// declared by the chart. Populated at collectBindings time by
	// evaluating each entry's Literal / Template / FromDSL/Default
	// against the same inputs as Computed. Reached via Get(<key>)
	// AFTER Computed — so chart-author binding_fields take priority
	// when a name overlaps. Closes idefxH/rda-cli#122: any key in
	// binding_secret is automatically reachable as
	// \${binding:NAME.<key>}, no per-chart binding_fields duplication.
	Secret map[string]string
}

// PortFields is the cross-binding shape of one named port.
type PortFields struct {
	Port string
	URL  string // <scheme>://<host>:<port>
}

// Get looks up a named field on the binding. Returns an error with the
// list of valid fields when the name isn't recognised — the error
// message is the dev-facing UX.
func (b *BindingFields) Get(field string) (string, error) {
	switch field {
	case "host":
		return b.Host, nil
	case "port":
		return b.Port, nil
	case "url":
		return b.URL, nil
	case "username":
		return b.Username, nil
	case "password":
		return b.Password, nil
	case "database":
		return b.Database, nil
	case "type":
		return b.Type, nil
	case "secret_name":
		return b.SecretName, nil
	}
	// Named-port accessors: <port_name>_port and <port_name>_url.
	// e.g. minio.s3_url, minio.console_url, dex.grpc_port.
	for _, suffix := range []string{"_port", "_url"} {
		if strings.HasSuffix(field, suffix) {
			portName := strings.TrimSuffix(field, suffix)
			if pf, ok := b.Ports[portName]; ok {
				if suffix == "_port" {
					return pf.Port, nil
				}
				return pf.URL, nil
			}
		}
	}
	// Build error with available fields + ports for actionability.
	avail := []string{"host", "port", "url", "username", "password", "database", "type", "secret_name"}
	if len(b.Ports) > 0 {
		ports := make([]string, 0, len(b.Ports))
		for n := range b.Ports {
			ports = append(ports, n)
		}
		sort.Strings(ports)
		for _, n := range ports {
			avail = append(avail, n+"_port", n+"_url")
		}
	}
	// Computed: chart-author-declared binding_fields (Phase 1 #2).
	// Tried LAST so chart authors can't shadow the well-known set —
	// a `host` entry in binding_fields would be a chart-author bug
	// (we'd never reach it; the hardcoded host wins) but it's
	// silently harmless instead of corrupting the rendered output.
	if v, ok := b.Computed[field]; ok {
		return v, nil
	}
	if len(b.Computed) > 0 {
		computed := make([]string, 0, len(b.Computed))
		for k := range b.Computed {
			computed = append(computed, k)
		}
		sort.Strings(computed)
		avail = append(avail, computed...)
	}
	// Secret: chart's binding_secret[] keys (closes #122). Tried
	// last so chart-author binding_fields names + the hardcoded set
	// take priority on overlap. Empty-string secret values still
	// resolve (the render path doesn't treat empty as "missing").
	if v, ok := b.Secret[field]; ok {
		return v, nil
	}
	if len(b.Secret) > 0 {
		secretKeys := make([]string, 0, len(b.Secret))
		for k := range b.Secret {
			secretKeys = append(secretKeys, k)
		}
		sort.Strings(secretKeys)
		avail = append(avail, secretKeys...)
	}
	return "", fmt.Errorf("unknown field %q (valid: %s)", field, strings.Join(avail, ", "))
}

// collectBindings walks services[] and projects each enabled+catalogued
// entry into a BindingFields. The map is keyed by binding name; the
// resolver picks from it on every ${binding:NAME.field} reference.
//
// Returns an empty map when values has no services[] block, or when
// mappings is nil. Entries with provisioning != local AND no endpoint
// are still indexed (their Host/Port come from the catalogue's
// service.host template at the project's namespace, OR from
// services[].endpoint when provisioning=external).
func collectBindings(values map[string]any, mappings *dslmapping.Document, releaseName string) map[string]*BindingFields {
	out := map[string]*BindingFields{}
	if mappings == nil {
		return out
	}
	suse, ok := values["suse-library"].(map[string]any)
	if !ok {
		return out
	}
	servicesRaw, ok := suse["services"].([]any)
	if !ok {
		return out
	}
	// Compute aliases for multi-instance host resolution.
	svcMaps := make([]map[string]any, 0, len(servicesRaw))
	for _, raw := range servicesRaw {
		if m, ok := raw.(map[string]any); ok {
			svcMaps = append(svcMaps, m)
		}
	}
	aliases := ComputeAliases(svcMaps)

	for _, svcRaw := range servicesRaw {
		svc, ok := svcRaw.(map[string]any)
		if !ok {
			continue
		}
		binding, _ := svc["binding"].(string)
		chartType, _ := svc["type"].(string)
		if binding == "" || chartType == "" {
			continue
		}

		entry, ok := mappings.Charts[chartType]
		if !ok || len(entry.Versions) == 0 {
			continue
		}
		ver := entry.Versions[0]

		chartAlias := aliases[binding]
		if chartAlias == "" {
			chartAlias = chartType
		}

		bf := &BindingFields{
			Type:       chartType,
			SecretName: fmt.Sprintf("%s-%s-binding", releaseName, binding),
			Ports:      map[string]PortFields{},
		}

		// Host template: use aliased host for multi-instance so each
		// instance gets a distinct Kubernetes Service name.
		hostTemplate := AliasedHost(ver.Service.Host, chartType, chartAlias)
		bf.Host = renderHostTemplate(hostTemplate, releaseName)

		// Multi-port: walk every port, build the Ports map. The
		// primary port also surfaces in the short Host/Port/URL
		// fields. provisioning=external overrides only the primary
		// port (endpoint.{host,port,scheme}); secondary ports fall
		// back to the catalogue's declared values.
		ports := ver.Service.PortsResolved()
		extOverride := false
		var extHost, extScheme string
		var extPortStr string
		prov, _ := svc["provisioning"].(string)
		if prov == "connect" || prov == "external" || prov == "shared" {
			if epRaw, ok := svc["credentials"].(map[string]any); ok {
				// secretRef: read credentials from K8s Secret via kubectl
				if sr, ok := epRaw["secretRef"].(string); ok && sr != "" {
					branch, _ := svc["branch"].(string)
					secretData := readSecretViaKubectl(sr, releaseName, branch)
					if secretData != nil {
						if h := secretData["host"]; h != "" {
							extHost = h
							extOverride = true
						}
						if p := secretData["port"]; p != "" {
							extPortStr = p
							extOverride = true
						}
						if s := secretData["scheme"]; s != "" {
							extScheme = s
						}
						// Populate auth fields from Secret
						if u := secretData["username"]; u != "" {
							bf.Username = u
						}
						if p := secretData["password"]; p != "" {
							bf.Password = p
						}
						if d := secretData["database"]; d != "" {
							bf.Database = d
						}
					}
				} else {
					// Inline endpoint
					if h, ok := epRaw["host"].(string); ok && h != "" {
						extHost = h
						extOverride = true
					}
					if p, ok := epRaw["port"]; ok {
						extPortStr = fmt.Sprintf("%v", p)
						extOverride = true
					}
					if s, ok := epRaw["scheme"].(string); ok && s != "" {
						extScheme = s
					}
				}
			}
		}
		for name, p := range ports {
			scheme := p.Scheme
			if scheme == "" {
				scheme = "http"
			}
			host := bf.Host
			portStr := fmt.Sprintf("%d", p.Port)
			if extOverride && p.Primary {
				if extHost != "" {
					host = extHost
				}
				if extPortStr != "" {
					portStr = extPortStr
				}
				if extScheme != "" {
					scheme = extScheme
				}
			}
			pf := PortFields{
				Port: portStr,
				URL:  fmt.Sprintf("%s://%s:%s", scheme, host, portStr),
			}
			bf.Ports[name] = pf
			if p.Primary {
				bf.Host = host
				bf.Port = portStr
				bf.URL = pf.URL
			}
		}

		// Auth fields from the DSL — but only when not already set by
		// secretRef (which has the real external credentials).
		if bf.Username == "" {
			if v, ok := digDSL(svc, "auth.user.name"); ok {
				bf.Username = stringify(v)
			}
		}
		if bf.Password == "" {
			if v, ok := digDSL(svc, "auth.user.password"); ok {
				bf.Password = stringify(v)
			}
		}
		if bf.Database == "" {
			if v, ok := digDSL(svc, "auth.user.database"); ok {
				bf.Database = stringify(v)
			}
		}
		// auth.password (single-password types like redis/valkey) maps
		// onto Password too — the dev typically references "password"
		// regardless of which auth shape the chart has.
		if bf.Password == "" {
			if v, ok := digDSL(svc, "auth.password"); ok {
				bf.Password = stringify(v)
			}
		}
		if bf.Password == "" {
			// Vault dev mode: token lives at auth.admin.password.
			if v, ok := digDSL(svc, "auth.admin.password"); ok {
				bf.Password = stringify(v)
			}
		}

		// Phase 1 #2 of DO 0001-A: render chart-author binding_fields
		// templates and stash in BindingFields.Computed so callers
		// reaching Get(<field>) hit them after the hardcoded names.
		if len(ver.BindingFields) > 0 {
			bf.Computed = renderBindingFieldTemplates(ver.BindingFields, svc, releaseName, binding, chartType)
		}
		// Closes #122: resolve every binding_secret[].key so dev code
		// can write \${binding:NAME.<key>} without the chart author
		// having to re-declare them under binding_fields. Same template
		// inputs as Computed.
		if len(ver.BindingSecret) > 0 {
			bf.Secret = renderBindingSecretValues(ver.BindingSecret, svc, releaseName, binding, chartType)
		}
		out[binding] = bf
	}
	return out
}

// renderBindingFieldTemplates evaluates each chart-author-defined
// binding_field template against the same inputs DerivedValues uses
// (.Service, .Release.Name, .Binding, .Type). Errors (template parse
// or execute) yield empty-string values rather than failing the whole
// render — chart-author template bugs surface separately via the
// dsl-mappings-schema test guard, and a missing computed field yields
// a render-time error from BindingFields.Get with a clear "valid: ..."
// list (which still includes the chart-declared names).
func renderBindingFieldTemplates(specs map[string]dslmapping.BindingFieldSpec, svc map[string]any, releaseName, binding, chartType string) map[string]string {
	out := map[string]string{}
	input := map[string]any{
		"Service": svc,
		"Release": map[string]any{"Name": releaseName},
		"Binding": binding,
		"Type":    chartType,
	}
	for name, spec := range specs {
		if spec.Template == "" {
			continue
		}
		tpl, err := template.New(chartType + "/" + name).Parse(spec.Template)
		if err != nil {
			continue
		}
		var buf strings.Builder
		if err := tpl.Execute(&buf, input); err != nil {
			continue
		}
		out[name] = strings.TrimSpace(buf.String())
	}
	return out
}

// renderBindingSecretValues evaluates each chart-declared binding_secret[]
// entry into a name → string map, parallel to renderBindingFieldTemplates.
// Resolution per entry:
//
//   - Literal: use literal value verbatim.
//   - Template: render the Go template against the same input map as
//     binding_fields ({.Service, .Release.Name, .Binding, .Type}).
//   - FromDSL: dig into svc[] at the dotted path; on miss, fall back to
//     Default. Empty result is fine (the renderer treats empty as a
//     valid value, not a missing one).
//
// Keys with skip_env: true still get a resolved value here — the
// skip_env flag controls env-var projection, not addressability via
// ${binding:NAME.<key>}. Closes idefxH/rda-cli#122.
func renderBindingSecretValues(specs []dslmapping.BindingSecretEntry, svc map[string]any, releaseName, binding, chartType string) map[string]string {
	out := map[string]string{}
	input := map[string]any{
		"Service": svc,
		"Release": map[string]any{"Name": releaseName},
		"Binding": binding,
		"Type":    chartType,
	}
	for _, entry := range specs {
		if entry.Key == "" {
			continue
		}
		switch {
		case entry.Literal != "":
			out[entry.Key] = entry.Literal
		case entry.Template != "":
			tpl, err := template.New(chartType + "/" + entry.Key).Parse(entry.Template)
			if err != nil {
				continue
			}
			var buf strings.Builder
			if err := tpl.Execute(&buf, input); err != nil {
				continue
			}
			out[entry.Key] = strings.TrimSpace(buf.String())
		case entry.FromDSL != "":
			if v, ok := digDSL(svc, entry.FromDSL); ok {
				out[entry.Key] = stringify(v)
			} else if entry.Default != "" {
				out[entry.Key] = entry.Default
			}
		default:
			if entry.Default != "" {
				out[entry.Key] = entry.Default
			}
		}
	}
	return out
}

// renderHostTemplate substitutes {{.Release.Name}} in the catalogue's
// service.host template. {{.Release.Namespace}} stays literal — Helm
// fills it at install time.
func renderHostTemplate(tmpl, release string) string {
	repl := strings.NewReplacer(
		"{{.Release.Name}}", release,
		"{{ .Release.Name }}", release,
	)
	return repl.Replace(tmpl)
}

// stringify converts any DSL leaf to its string form for binding
// reference resolution.
func stringify(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// resolveBindingRefs walks an arbitrary value (string / map / list)
// and substitutes every ${binding:...} / ${binding-self:...} reference
// with the resolved value. selfBinding names the current entry — used
// for ${binding-self:...} resolution.
//
// Returns the resolved value (same shape as input) and an error when a
// reference can't be resolved (unknown binding or unknown field). The
// error message points at the offending reference — the dev's
// values.yaml is what they edit, so the message must be specific.
func resolveBindingRefs(value any, bindings map[string]*BindingFields, selfBinding string) (any, error) {
	switch v := value.(type) {
	case string:
		return resolveBindingRefsString(v, bindings, selfBinding)
	case map[string]any:
		out := map[string]any{}
		for k, sub := range v {
			r, err := resolveBindingRefs(sub, bindings, selfBinding)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, sub := range v {
			r, err := resolveBindingRefs(sub, bindings, selfBinding)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	default:
		return value, nil
	}
}

func resolveBindingRefsString(s string, bindings map[string]*BindingFields, selfBinding string) (string, error) {
	if !strings.Contains(s, "${binding") {
		return s, nil
	}
	matches := bindingRefRegex.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s, nil
	}
	var out strings.Builder
	last := 0
	for _, m := range matches {
		// m[0:1] = full match; m[2:3] = "binding" or "binding-self";
		// m[4:5] = ref payload.
		out.WriteString(s[last:m[0]])
		kind := s[m[2]:m[3]]
		ref := s[m[4]:m[5]]
		var bindingName, fieldName string
		if kind == "binding-self" {
			if selfBinding == "" {
				return "", fmt.Errorf("${binding-self:%s} used outside a services[] entry context", ref)
			}
			bindingName = selfBinding
			fieldName = ref
		} else {
			parts := strings.SplitN(ref, ".", 2)
			if len(parts) != 2 {
				return "", fmt.Errorf("malformed reference ${binding:%s} — expected ${binding:NAME.field}", ref)
			}
			bindingName, fieldName = parts[0], parts[1]
		}
		bf, ok := bindings[bindingName]
		if !ok {
			avail := make([]string, 0, len(bindings))
			for n := range bindings {
				avail = append(avail, n)
			}
			sort.Strings(avail)
			where := "available bindings"
			list := strings.Join(avail, ", ")
			if len(avail) == 0 {
				list = "(none — services[] is empty)"
			}
			return "", fmt.Errorf("${binding:%s} references unknown binding %q (%s: %s)",
				ref, bindingName, where, list)
		}
		val, err := bf.Get(fieldName)
		if err != nil {
			return "", fmt.Errorf("${binding:%s.%s}: %w", bindingName, fieldName, err)
		}
		out.WriteString(val)
		last = m[1]
	}
	out.WriteString(s[last:])
	return out.String(), nil
}
