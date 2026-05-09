// envblock.go — workload env: block resolution at render time.
//
// Phase 1 of Design Orientation 0001 Appendix A. Replaces the
// auto-projection pattern (every chart's binding_secret list emitted as
// <BINDING>_<KEY> for every enabled service) with explicit references
// in the user's `suse-library.env:` block:
//
//     suse-library:
//       env:
//         DB_HOST:        ${binding:db.host}
//         DB_PORT:        ${binding:db.port}
//         DB_USER:        ${binding:db.username}
//         DB_PASSWORD:    ${binding:db.password}
//         DATABASE_URL:   "postgres://${binding:db.username}:${binding:db.password}@${binding:db.host}:${binding:db.port}/${binding:db.database}"
//         AUTH_ISSUER:    ${binding:auth.issuer}
//
// `rda render` walks this block, classifies each entry, and writes a
// resolved structured form under `suse-library.env_resolved` for the
// deployment.yaml template to iterate.
//
// Two cases per entry:
//
//   1. BARE REFERENCE — a single ${binding:NAME.field} where `field` is
//      a key in the binding's binding_secret schema. Resolved to a
//      secretKeyRef so the secret never appears in the deployment spec.
//      Emitted as { name, kind: secret, secretRef, secretKey }.
//
//   2. COMPOSED OR DERIVED — anything else (literal string, composed
//      string with one-or-more refs, or a bare ref to a derived field
//      not in binding_secret like `auth.issuer`). Resolved to a literal
//      string. Emitted as { name, kind: value, value }.
//
// The dev's intent governs: if they want the secretKeyRef indirection
// (passwords NOT in `kubectl get deployment -o yaml`), they write a
// bare reference. If they need composition (DATABASE_URL), the
// resolved literal lands in the deployment spec — visible there but
// fine for dev. The D5 lint catches plaintext passwords leaking into
// `overrides.<non-dev>` separately.

package render

import (
	"fmt"
	"sort"
	"strings"

	"github.com/idefxH/rda-render-engine/dslmapping"
)

// EnvEntry is one resolved env var entry, ready for the deployment.yaml
// template to render as either a `valueFrom: secretKeyRef:` or a `value:`
// field. Field names match the YAML keys the template iterates.
type EnvEntry struct {
	Name      string `yaml:"name"`
	Kind      string `yaml:"kind"`           // "secret" or "value"
	SecretRef string `yaml:"secretRef,omitempty"`
	SecretKey string `yaml:"secretKey,omitempty"`
	Value     string `yaml:"value,omitempty"`
}

// bindingSecretIndex maps binding name → set of secret keys exposed
// by that binding's chart. Built once per render from dsl-mappings.
// Used by classifyEnvValue to decide if a bare reference can use
// secretKeyRef (key is in the binding-secret) or must be inlined as a
// literal (computed/derived field, not stored in the secret).
type bindingSecretIndex map[string]map[string]bool

// buildBindingSecretIndex walks services[] and, for each enabled
// catalogued entry, indexes the keys of its chart's binding_secret
// (using the first declared version). Returns the index plus a map of
// binding → external Secret name for secretRef overrides.
func buildBindingSecretIndex(values map[string]any, mappings *dslmapping.Document) (bindingSecretIndex, map[string]string) {
	out := bindingSecretIndex{}
	overrides := map[string]string{}
	if mappings == nil {
		return out, overrides
	}
	suse, ok := values["suse-library"].(map[string]any)
	if !ok {
		return out, overrides
	}
	servicesRaw, ok := suse["services"].([]any)
	if !ok {
		return out, overrides
	}
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
		// Check for endpoint.secretRef override
		if ep, ok := svc["credentials"].(map[string]any); ok {
			if sr, ok := ep["secretRef"].(string); ok && sr != "" {
				overrides[binding] = sr
			}
		}
		entry, ok := mappings.Charts[chartType]
		if !ok || len(entry.Versions) == 0 {
			continue
		}
		ver := entry.Versions[0]
		keys := map[string]bool{}
		for _, bs := range ver.BindingSecret {
			if bs.Key != "" {
				keys[bs.Key] = true
			}
		}
		out[binding] = keys
	}
	return out, overrides
}

// resolveWorkloadEnv reads suse["env"] (the workload env block),
// classifies each entry, and returns the resolved EnvEntry list sorted
// by name (deterministic output for diff stability).
//
// Returns nil, nil when no env block — render stays a no-op.
//
// Errors carry the offending env name to be actionable: "env[DB_HOST]:
// references unknown binding \"db\"" rather than just "unknown binding
// \"db\"".
func resolveWorkloadEnv(
	envRaw map[string]any,
	bindings map[string]*BindingFields,
	secretIdx bindingSecretIndex,
	secretOverrides map[string]string,
	disabledBindings map[string]bool,
	releaseName string,
) ([]EnvEntry, error) {
	if envRaw == nil || len(envRaw) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(envRaw))
	for n := range envRaw {
		names = append(names, n)
	}
	sort.Strings(names)

	out := make([]EnvEntry, 0, len(names))
	for _, name := range names {
		raw := envRaw[name]
		var rawStr string
		if s, ok := raw.(string); ok {
			rawStr = s
		} else if raw == nil {
			// `MY_VAR: null` — treat as empty string literal.
			rawStr = ""
		} else {
			// Numbers, bools etc. — render as their YAML scalar form.
			rawStr = fmt.Sprintf("%v", raw)
		}
		// Skip env entries whose binding refs target disabled services.
		if refersToDisabledBinding(rawStr, disabledBindings) {
			continue
		}
		entry, err := classifyEnvValue(name, rawStr, bindings, secretIdx, secretOverrides, releaseName)
		if err != nil {
			return nil, fmt.Errorf("env[%s]: %w", name, err)
		}
		out = append(out, entry)
	}
	return out, nil
}

// refersToDisabledBinding checks if a raw env value contains binding
// refs that ALL point to disabled services. Mixed refs (some enabled,
// some disabled) are NOT skipped — they'll error at classify time,
// which is the correct behavior (the dev should fix the composed ref).
func refersToDisabledBinding(raw string, disabled map[string]bool) bool {
	if len(disabled) == 0 {
		return false
	}
	matches := bindingRefRegex.FindAllStringSubmatch(raw, -1)
	if len(matches) == 0 {
		return false
	}
	for _, m := range matches {
		payload := m[2]
		parts := strings.SplitN(payload, ".", 2)
		bindingName := parts[0]
		if !disabled[bindingName] {
			return false
		}
	}
	return true
}

// classifyEnvValue decides between secret-ref and literal-value. The
// rule is:
//
//   - If `raw` is a SINGLE bare ${binding:NAME.field} reference (no
//     surrounding text, no concatenation) AND `field` exists in NAME's
//     binding_secret schema, emit a secretKeyRef entry — the actual
//     value never lands in the deployment spec.
//
//   - Otherwise (composed string, literal, or bare ref to a derived
//     field like `auth.issuer`), resolve fully to a literal string and
//     emit a value entry.
func classifyEnvValue(
	name, raw string,
	bindings map[string]*BindingFields,
	secretIdx bindingSecretIndex,
	secretOverrides map[string]string,
	releaseName string,
) (EnvEntry, error) {
	trimmed := strings.TrimSpace(raw)
	if bindingName, fieldName, isBare := parseBareBindingRef(trimmed); isBare {
		// Bare ref — verify the binding exists; on success try the
		// secret-key shortcut, else fall through to literal resolution.
		bf, ok := bindings[bindingName]
		if !ok {
			avail := sortedKeys(bindings)
			where := "available bindings"
			list := strings.Join(avail, ", ")
			if len(avail) == 0 {
				list = "(none — services[] is empty)"
			}
			return EnvEntry{}, fmt.Errorf(
				"references unknown binding %q (%s: %s)",
				bindingName, where, list)
		}
		// Eager field-exists check via BindingFields.Get — same error
		// surface as the rest of the binding resolver. Resolved value
		// is discarded when we go down the secret-ref path; consumed
		// when we fall through to literal.
		resolved, err := bf.Get(fieldName)
		if err != nil {
			return EnvEntry{}, fmt.Errorf("${binding:%s.%s}: %w",
				bindingName, fieldName, err)
		}
		if keys := secretIdx[bindingName]; keys != nil && keys[fieldName] {
			secretName := fmt.Sprintf("%s-%s-binding", releaseName, bindingName)
			if override, ok := secretOverrides[bindingName]; ok {
				secretName = override
			}
			return EnvEntry{
				Name:      name,
				Kind:      "secret",
				SecretRef: secretName,
				SecretKey: fieldName,
			}, nil
		}
		// Derived field (issuer, public_url, *_url, *_port…): inline
		// the resolved literal — there's no key in the secret to point
		// to. The dev sees the inlined value at render time via the
		// `# →` annotation (Phase 1.3 / DO 0001-A.A.2.3).
		return EnvEntry{Name: name, Kind: "value", Value: resolved}, nil
	}
	// Composed string (or pure literal). Resolve any embedded refs and
	// emit the result as a value entry. selfBinding is empty: the
	// workload env block is not "inside" any binding.
	resolved, err := resolveBindingRefsString(raw, bindings, "")
	if err != nil {
		return EnvEntry{}, err
	}
	return EnvEntry{Name: name, Kind: "value", Value: resolved}, nil
}

// parseBareBindingRef returns (binding, field, true) if `s` is exactly
// one ${binding:NAME.field} reference with no surrounding text. Returns
// ("", "", false) for empty strings, literals, composed strings, and
// ${binding-self:...} forms (which are render-internal — the workload
// env block has no "self" context so we forbid it loud at the regex
// level).
func parseBareBindingRef(s string) (binding, field string, ok bool) {
	matches := bindingRefRegex.FindStringSubmatchIndex(s)
	if matches == nil {
		return "", "", false
	}
	// Whole-string match? matches[0:1] is the span of the full match.
	if matches[0] != 0 || matches[1] != len(s) {
		return "", "", false
	}
	// Group 1: "binding" or "binding-self". We accept only "binding"
	// here — binding-self has no meaning in the workload env block.
	kind := s[matches[2]:matches[3]]
	if kind != "binding" {
		return "", "", false
	}
	payload := s[matches[4]:matches[5]]
	parts := strings.SplitN(payload, ".", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// envEntriesToValues converts a list of EnvEntry to the []any /
// map[string]any shape that yaml.Marshal expects for embedding in
// values.generated.yaml. Keys are emitted in the natural Go field
// order.
func envEntriesToValues(entries []EnvEntry) []any {
	out := make([]any, len(entries))
	for i, e := range entries {
		m := map[string]any{
			"name": e.Name,
			"kind": e.Kind,
		}
		switch e.Kind {
		case "secret":
			m["secretRef"] = e.SecretRef
			m["secretKey"] = e.SecretKey
		case "value":
			m["value"] = e.Value
		}
		out[i] = m
	}
	return out
}

// sortedKeys returns the keys of a generic map in deterministic order.
// Used for stable error messages.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
