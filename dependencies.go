package render

import (
	"fmt"
	"strings"

	"github.com/idefxH/rda-render-engine/dslmapping"
)

// projectDependencies resolves cross-binding dependencies for a service entry.
// For each dependency declared in the chart's dsl-mappings, it:
//   1. Reads the DSL field from the consumer service to find the referenced binding name
//   2. Looks up that binding in services[] — fails loud if missing, disabled, or wrong type
//   3. Disables the consumer chart's internal sub-chart (e.g. airflow.postgresql.enabled=false)
//   4. Wires values from the referenced binding into the consumer chart's values
//
// suseOut is the overlay being built. bindings is the pre-computed binding→fields map.
// allServices is the raw services[] slice for looking up referenced bindings.
func projectDependencies(
	svc map[string]any,
	suseOut map[string]any,
	ver dslmapping.VersionEntry,
	binding string,
	chartAlias string,
	chartType string,
	bindings map[string]*BindingFields,
	allServices []any,
	mappings *dslmapping.Document,
	releaseName string,
) error {
	if len(ver.Dependencies) == 0 {
		return nil
	}

	// Track which dsl_fields were successfully wired. Used to detect
	// when a field references a type that no dep entry accepts.
	wiredFields := map[string]bool{}

	for _, dep := range ver.Dependencies {
		refBindingName, _ := svc[dep.DSLField].(string)
		accepted := strings.Join(dep.AcceptedTypes(), "/")
		if refBindingName == "" {
			if dep.Required {
				return fmt.Errorf(
					"services[binding=%s] requires a %s service for %q but none exists. "+
						"Add one with: rda service add %s <name>",
					binding, accepted, dep.DSLField, dep.AcceptedTypes()[0])
			}
			continue
		}

		refSvc := findServiceByBinding(allServices, refBindingName)
		if refSvc == nil {
			return fmt.Errorf(
				"services[binding=%s].%s references binding %q but no service with that binding exists",
				binding, dep.DSLField, refBindingName)
		}

		refType, _ := refSvc["type"].(string)
		if !dep.AcceptsType(refType) {
			// Multiple deps may share the same dsl_field (e.g., state_db
			// accepts postgresql OR mariadb via separate entries). Skip
			// this dep if the ref type doesn't match — another dep entry
			// for the same field may accept it.
			continue
		}

		// Check enabled
		if enabled, ok := refSvc["enabled"].(bool); ok && !enabled {
			return fmt.Errorf(
				"services[binding=%s].%s references binding %q but that service is disabled (enabled: false). "+
					"Enable it first",
				binding, dep.DSLField, refBindingName)
		}

		// secretRef bindings: host/port/auth are now resolved from the
		// K8s Secret at render-time via kubectl (#131). No skip needed.

		// Disable the consumer chart's internal sub-chart
		chartBlock := getOrInitMap(suseOut, chartAlias)
		depBlock := getOrInitMap(chartBlock, refType)
		depBlock["enabled"] = false

		// Wire values from the referenced binding into the consumer
		if len(dep.Wiring) > 0 {
			refBF := bindings[refBindingName]
			for targetPath, sourcePath := range dep.Wiring {
				aliasedTarget := AliasedPath(targetPath, chartType, chartAlias)
				var val string
				switch {
				case sourcePath == "__host__":
					if refBF != nil {
						val = refBF.Host
					}
				case sourcePath == "__host_short__":
					if refBF != nil {
						host := refBF.Host
						if strings.HasSuffix(host, ".svc.cluster.local") {
							host = host[:strings.Index(host, ".")]
						}
						val = host
					}
				case sourcePath == "__port__":
					if refBF != nil {
						var portInt int
						fmt.Sscanf(refBF.Port, "%d", &portInt)
						if portInt > 0 {
							if err := setAtPath(suseOut, aliasedTarget, portInt); err != nil {
								return fmt.Errorf("dependency wiring %s.%s -> %s: %w",
									binding, dep.DSLField, aliasedTarget, err)
							}
							continue
						}
						val = refBF.Port
					}
				case strings.HasPrefix(sourcePath, "__url__"):
					// Supports __url__ alone or __url__/suffix (e.g. __url__/token)
					if refBF != nil {
						suffix := strings.TrimPrefix(sourcePath, "__url__")
						val = refBF.URL + suffix
					}
				case strings.HasPrefix(sourcePath, "__binding:"):
					// Syntax: __binding:FIELD__ or __binding:FIELD__/suffix
					// Find the closing __ to extract the field name; anything
					// after it is appended verbatim to the resolved value.
					rest := strings.TrimPrefix(sourcePath, "__binding:")
					endIdx := strings.Index(rest, "__")
					if endIdx >= 0 {
						fieldName := rest[:endIdx]
						suffix := rest[endIdx+2:]
						if refBF != nil {
							if resolved, err := refBF.Get(fieldName); err == nil {
								val = resolved + suffix
							}
						}
					}
				case strings.HasPrefix(sourcePath, "__literal:"):
					val = strings.TrimPrefix(sourcePath, "__literal:")
				case strings.HasPrefix(sourcePath, "__bootstrap:"):
					// Syntax: __bootstrap:KEY.FIELD__ where KEY is the bootstrap
					// map key in the provider service (e.g. "auth.clients") and
					// FIELD is the item field (e.g. "id", "secret"). The consumer
					// binding name is used to find the matching item.
					bootstrapPath := strings.TrimPrefix(sourcePath, "__bootstrap:")
					bootstrapPath = strings.TrimSuffix(bootstrapPath, "__")
					val = resolveBootstrapPath(refSvc, bootstrapPath, binding)
				default:
					// When the ref binding uses secretRef, prefer the resolved
					// BindingFields (from kubectl) over DSL scaffold defaults.
					refHasSecretRef := false
					if ep, ok := refSvc["credentials"].(map[string]any); ok {
						if sr, _ := ep["secretRef"].(string); sr != "" {
							refHasSecretRef = true
						}
					}
					if refHasSecretRef && refBF != nil {
						key := dslPathToBindingKey(sourcePath)
						if resolved, err := refBF.Get(key); err == nil && resolved != "" {
							val = resolved
						}
					}
					if val == "" {
						v, found := digDSL(refSvc, sourcePath)
						if found {
							if s, ok := v.(string); ok {
								val = s
							} else {
								val = fmt.Sprintf("%v", v)
							}
						}
					}
				}
				if val != "" {
					// Passthrough wins: if the target path was already
					// written (by passthrough or stage overrides), don't
					// overwrite it. This lets dev overrides beat wiring.
					if existing := getAtPath(suseOut, aliasedTarget); existing != nil {
						trace("phase2", binding, "wiring SKIP", fmt.Sprintf("%s (existing=%v, would-be=%v)", aliasedTarget, existing, val))
						continue
					}
					if err := setAtPath(suseOut, aliasedTarget, val); err != nil {
						return fmt.Errorf("dependency wiring %s.%s -> %s: %w",
							binding, dep.DSLField, aliasedTarget, err)
					}
				}
			}
		}
		wiredFields[dep.DSLField] = true
	}

	// Check for fields that reference a binding but no dep entry accepted the type.
	for _, dep := range ver.Dependencies {
		refBindingName, _ := svc[dep.DSLField].(string)
		if refBindingName == "" || wiredFields[dep.DSLField] {
			continue
		}
		refSvc := findServiceByBinding(allServices, refBindingName)
		if refSvc == nil {
			continue
		}
		refType, _ := refSvc["type"].(string)
		// Collect all accepted types across deps with this field
		var allAccepted []string
		for _, d := range ver.Dependencies {
			if d.DSLField == dep.DSLField {
				allAccepted = append(allAccepted, d.AcceptedTypes()...)
			}
		}
		return fmt.Errorf(
			"services[binding=%s].%s references binding %q (type=%s) but dependency accepts: %s",
			binding, dep.DSLField, refBindingName, refType, strings.Join(allAccepted, ", "))
	}

	return nil
}

// findBindingsByType returns binding names of enabled services matching the given type.
// Excludes the service identified by excludeBinding (to avoid self-referencing).
func findBindingsByType(services []any, chartType, excludeBinding string) []string {
	var out []string
	for _, raw := range services {
		svc, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		b, _ := svc["binding"].(string)
		t, _ := svc["type"].(string)
		if b == excludeBinding || t != chartType {
			continue
		}
		if enabled, ok := svc["enabled"].(bool); ok && !enabled {
			continue
		}
		out = append(out, b)
	}
	return out
}

// findServiceByBinding searches services[] for a service with the given binding name.
func findServiceByBinding(services []any, binding string) map[string]any {
	for _, raw := range services {
		svc, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if b, _ := svc["binding"].(string); b == binding {
			return svc
		}
	}
	return nil
}

// DependencyHints returns human-readable hints for dependencies the chart
// declares. Used by add-service to tell the dev what they need to provide.
func DependencyHints(chartType string, mappings *dslmapping.Document) []string {
	if mappings == nil {
		return nil
	}
	entry, ok := mappings.Charts[chartType]
	if !ok || len(entry.Versions) == 0 {
		return nil
	}
	ver := entry.Versions[0]
	if len(ver.Dependencies) == 0 {
		return nil
	}
	var hints []string
	for _, dep := range ver.Dependencies {
		req := ""
		if dep.Required {
			req = " (required)"
		}
		accepted := strings.Join(dep.AcceptedTypes(), "/")
		hints = append(hints, fmt.Sprintf(
			"%s: set %s=<binding-name> (a %s service)%s",
			dep.DSLField, dep.DSLField, accepted, req))
	}
	return hints
}

// ValidateDependencies checks that all required dependencies are satisfied
// for a service entry. Returns nil if all good, error if something is missing.
// Called by render before projectDependencies for fail-fast validation.
func ValidateDependencies(
	svc map[string]any,
	ver dslmapping.VersionEntry,
	binding string,
	allServices []any,
) error {
	for _, dep := range ver.Dependencies {
		refBindingName, _ := svc[dep.DSLField].(string)
		if refBindingName == "" && dep.Required {
			accepted := strings.Join(dep.AcceptedTypes(), "/")
			return fmt.Errorf(
				"services[binding=%s] requires dependency %q (%s) but field is empty",
				binding, dep.DSLField, accepted)
		}
		if refBindingName == "" {
			continue
		}
		ref := findServiceByBinding(allServices, refBindingName)
		if ref == nil {
			return fmt.Errorf(
				"services[binding=%s].%s=%q: no service with that binding",
				binding, dep.DSLField, refBindingName)
		}
		refType, _ := ref["type"].(string)
		if !dep.AcceptsType(refType) {
			accepted := strings.Join(dep.AcceptedTypes(), "/")
			return fmt.Errorf(
				"services[binding=%s].%s=%q: type=%s not accepted (expected: %s)",
				binding, dep.DSLField, refBindingName, refType, accepted)
		}
		if enabled, ok := ref["enabled"].(bool); ok && !enabled {
			return fmt.Errorf(
				"services[binding=%s].%s=%q: that service is disabled",
				binding, dep.DSLField, refBindingName)
		}
	}
	return nil
}

// dslPathToBindingKey maps DSL wiring source paths to binding-secret
// key names. The DSL uses dotted paths (auth.user.name) while the
// binding-secret uses flat keys (username, password, database).
func dslPathToBindingKey(dslPath string) string {
	mapping := map[string]string{
		"auth.user.name":     "username",
		"auth.user.password": "password",
		"auth.user.database": "database",
		"auth.admin.password": "password",
		"auth.password":      "password",
		"auth.rbac.rootPassword": "password",
	}
	if key, ok := mapping[dslPath]; ok {
		return key
	}
	return lastSegment(dslPath)
}

// lastSegment returns the last dotted-path segment (e.g., "auth.user.name" → "name").
func lastSegment(path string) string {
	if idx := strings.LastIndex(path, "."); idx >= 0 {
		return path[idx+1:]
	}
	return path
}

// resolveBootstrapPath resolves a __bootstrap:KEY.FIELD__ sentinel.
// path is "KEY.FIELD" (e.g. "auth.clients.id"). It scans the provider
// service's bootstrap map for a key that is a dot-prefix of path, uses
// the remainder as the item field name, and matches by consumer binding.
func resolveBootstrapPath(svc map[string]any, path, consumerBinding string) string {
	bs, ok := svc["bootstrap"].(map[string]any)
	if !ok {
		return ""
	}
	for key, itemsRaw := range bs {
		prefix := key + "."
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		fieldName := path[len(prefix):]
		items, ok := itemsRaw.([]any)
		if !ok {
			continue
		}
		// First pass: find item matching the consumer binding
		for _, itemRaw := range items {
			item, ok := itemRaw.(map[string]any)
			if !ok {
				continue
			}
			name, _ := item["name"].(string)
			id, _ := item["id"].(string)
			if name == consumerBinding || id == consumerBinding+"-client" || id == consumerBinding {
				if v, ok := item[fieldName]; ok {
					return fmt.Sprintf("%v", v)
				}
			}
		}
		// Fallback: field from the first item
		if len(items) > 0 {
			if item, ok := items[0].(map[string]any); ok {
				if v, ok := item[fieldName]; ok {
					return fmt.Sprintf("%v", v)
				}
			}
		}
	}
	return ""
}

// resolveBootstrapField is retained for any direct callers outside the
// wiring switch. New code should use resolveBootstrapPath.
func resolveBootstrapField(svc map[string]any, capName, consumerBinding, fieldName string) string {
	return resolveBootstrapPath(svc, capName+"."+fieldName, consumerBinding)
}

