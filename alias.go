package render

import "strings"

// ComputeAliases assigns a Helm chart alias to each service binding.
//
// Single-instance types keep the bare type name as alias (backward compatible).
// Multi-instance types (2+ bindings sharing a type) get <type>-<binding> for
// EVERY instance — deterministic, no order dependency.
//
// The returned map is keyed by binding name; the value is the chart alias
// that projection.go uses as the values namespace key and Chart.yaml uses
// as the alias field.
//
// SYNC: the Tilt extension (suse_rda/Tiltfile filter_enabled_deps) uses the
// same algorithm to expand Chart.yaml deps. Both must agree on alias names;
// if you change the formula here, update filter_enabled_deps too.
func ComputeAliases(services []map[string]any) map[string]string {
	byType := map[string][]string{}
	for _, svc := range services {
		t, _ := svc["type"].(string)
		b, _ := svc["binding"].(string)
		if t == "" || b == "" {
			continue
		}
		byType[t] = append(byType[t], b)
	}

	aliases := map[string]string{}
	for t, bindings := range byType {
		if len(bindings) == 1 {
			aliases[bindings[0]] = t
		} else {
			for _, b := range bindings {
				aliases[b] = t + "-" + b
			}
		}
	}
	return aliases
}

// IsMultiInstance reports whether chartType has more than one binding in the alias map.
func IsMultiInstance(aliases map[string]string, chartType string) bool {
	count := 0
	for _, alias := range aliases {
		if alias == chartType || strings.HasPrefix(alias, chartType+"-") {
			count++
		}
	}
	return count > 1
}

// MultiInstanceTypes returns the set of chart types that have multiple bindings.
func MultiInstanceTypes(services []map[string]any) map[string]bool {
	counts := map[string]int{}
	for _, svc := range services {
		t, _ := svc["type"].(string)
		if t != "" {
			counts[t]++
		}
	}
	out := map[string]bool{}
	for t, n := range counts {
		if n > 1 {
			out[t] = true
		}
	}
	return out
}

// AliasedPath transforms a values_mapping target path by replacing the chart
// type prefix with the alias. E.g., "postgresql.auth.username" with alias
// "postgresql-payments-db" becomes "postgresql-payments-db.auth.username".
//
// When alias == chartType (single-instance), the path is returned unchanged.
func AliasedPath(path, chartType, alias string) string {
	if alias == chartType {
		return path
	}
	prefix := chartType + "."
	if strings.HasPrefix(path, prefix) {
		return alias + "." + path[len(prefix):]
	}
	if path == chartType {
		return alias
	}
	return path
}

// AliasedHost transforms a service.host template by replacing the chart type
// in the hostname with the alias. E.g.:
//
//	"{{ .Release.Name }}-postgresql.{{ .Release.Namespace }}.svc.cluster.local"
//
// becomes:
//
//	"{{ .Release.Name }}-postgresql-payments-db.{{ .Release.Namespace }}.svc.cluster.local"
//
// When alias == chartType, returns the original template unchanged.
func AliasedHost(hostTemplate, chartType, alias string) string {
	if alias == chartType {
		return hostTemplate
	}
	return strings.Replace(hostTemplate, "-"+chartType+".", "-"+alias+".", 1)
}
