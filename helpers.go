package render

import "strings"

// setNestedValue sets a value at a dot-separated path in a map,
// creating intermediate maps as needed.
// e.g. setNestedValue(m, "app.extraEnv", list) → m["app"]["extraEnv"] = list
func setNestedValue(m map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	current := m
	for i, part := range parts {
		if i == len(parts)-1 {
			current[part] = value
			return
		}
		next, ok := current[part]
		if !ok {
			next = map[string]any{}
			current[part] = next
		}
		nextMap, ok := next.(map[string]any)
		if !ok {
			nextMap = map[string]any{}
			current[part] = nextMap
		}
		current = nextMap
	}
}
