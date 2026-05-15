package render

import (
	"fmt"
	"sort"
	"strings"
)

// serializeGrafanaIni walks the overlay and converts every `grafana.ini`
// key whose value is a nested map (section → key → value) into an INI-
// formatted multi-line string.
//
// Why: the upstream Grafana chart supports `grafana.ini` as either a
// 2-level dict (sections → key=value) or a raw INI string. The CLI's
// projection naturally produces the dict form (each wiring target writes
// `grafana.grafana\.ini.<section>.<key>` as a dotted YAML key with
// escaped dots). But downstream tools — and the bundle's helm-template
// renderer for the ConfigMap — read the value verbatim and need the INI
// text directly. The conversion happens HERE so the overlay file is
// consumable without going through the chart's _helpers.tpl conversion.
//
// Only converts values of TYPE map[string]any whose immediate parent has
// the literal key "grafana.ini". Strings (when a user already wrote the
// raw form via passthrough) are left untouched. Nested values that are
// themselves maps (3rd level) are flattened with `key.subkey = value`
// — the grafana chart does the same when it serialises sections.
func serializeGrafanaIni(m map[string]any) {
	for k, v := range m {
		switch val := v.(type) {
		case map[string]any:
			if k == "grafana.ini" {
				m[k] = mapToINI(val)
				continue
			}
			serializeGrafanaIni(val)
		case []any:
			for _, item := range val {
				if im, ok := item.(map[string]any); ok {
					serializeGrafanaIni(im)
				}
			}
		}
	}
}

// mapToINI renders a 2-level section→key→value map into INI text.
//
// Sections are sorted alphabetically so render output is deterministic
// (matches the rest of the projection's stable-iteration story —
// `--check` diffs need to be reproducible). Keys within a section are
// also sorted.
//
// Section values that are themselves a map are emitted as standard INI
// `key = value` lines. Anything else at the top level (e.g. a leaf
// value that landed at "grafana.ini.foo" directly with no section)
// is skipped — the chart's grafana.ini contract requires section
// headers, and `foo = bar` without one wouldn't make sense.
func mapToINI(sections map[string]any) string {
	sectionNames := make([]string, 0, len(sections))
	for k := range sections {
		sectionNames = append(sectionNames, k)
	}
	sort.Strings(sectionNames)

	var buf strings.Builder
	for _, section := range sectionNames {
		entries, ok := sections[section].(map[string]any)
		if !ok {
			continue
		}
		keys := make([]string, 0, len(entries))
		for k := range entries {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		buf.WriteString("[")
		buf.WriteString(section)
		buf.WriteString("]\n")
		for _, k := range keys {
			fmt.Fprintf(&buf, "%s = %s\n", k, iniValue(entries[k]))
		}
		buf.WriteString("\n")
	}
	return strings.TrimRight(buf.String(), "\n")
}

// iniValue renders a Go value as the right-hand side of an INI key=value
// line. Strings pass through verbatim (the projection has already
// resolved templates and sentinels); booleans / numbers use their Go
// formatting; nil becomes empty.
func iniValue(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", v)
	}
}
