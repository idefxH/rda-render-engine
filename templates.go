package render

import "strings"

// resolveTemplates expands {{.domain}} (and future tokens) in all
// string values under suse-library. Runs after both override passes
// so {{.domain}} sees the stage-merged value.
func resolveTemplates(values map[string]any) {
	suse, ok := values["suse-library"].(map[string]any)
	if !ok {
		return
	}
	domain, _ := suse["domain"].(string)
	if domain == "" {
		return
	}
	replacer := strings.NewReplacer("{{.domain}}", domain)
	walkAndReplace(suse, replacer)
}

func walkAndReplace(m map[string]any, r *strings.Replacer) {
	for k, v := range m {
		switch val := v.(type) {
		case string:
			if strings.Contains(val, "{{.") {
				m[k] = r.Replace(val)
			}
		case map[string]any:
			walkAndReplace(val, r)
		case []any:
			for i, item := range val {
				switch el := item.(type) {
				case string:
					if strings.Contains(el, "{{.") {
						val[i] = r.Replace(el)
					}
				case map[string]any:
					walkAndReplace(el, r)
				}
			}
		}
	}
}
