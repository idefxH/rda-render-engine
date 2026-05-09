// inlinecomments.go — maintain inline `# → resolved` comments next to
// every `${binding:...}` reference in a project's chart/values.yaml.
// Phase 1 #3 of Design Orientation 0001 Appendix A.
//
// Why this exists: when a dev writes `DB_HOST: ${binding:db.host}` in
// their `suse-library.env:` block, they don't see what the reference
// resolves to. The DSL is explicit about WHERE the value comes from,
// not about WHAT it currently is. This file closes that gap by
// having `rda render` maintain a trailing `# → <resolved>` comment
// next to each reference, refreshed every render so the displayed
// value never goes stale.
//
// Contract:
//   - rda OWNS the LineComment of every `suse-library.env` entry
//     whose value contains a `${binding:...}` reference. Existing
//     LineComments on those entries are overwritten unconditionally.
//   - rda DOES NOT touch HeadComments / FootComments anywhere, nor
//     LineComments on entries without binding references, nor any
//     other map / sequence in the file.
//   - User-added context belongs above the line (HeadComment) — that
//     placement is the supported "I want to annotate this entry"
//     pattern. Trying to share LineComment with rda would lose
//     either side's intent; we picked rda's because the resolved
//     value is operationally more useful and changes more often.
//   - For computed fields declared in dsl-mappings `binding_fields`
//     (issuer, public_url), the comment carries an extra
//     `(derived: <field>)` annotation so the dev knows the value
//     came from a chart-author-defined template, not the
//     binding-secret directly.
//
// Implementation: the file is parsed via yaml.v3 Node mode (preserves
// every other comment + key order), the env mapping is walked, the
// LineComments are set, and the file is round-tripped back to disk
// only if at least one comment differs from what was already there
// (no-op idempotency — keeps `rda doctor` clean and Tilt's
// hash-based reload from spinning).

package render

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/idefxH/rda-render-engine/dslmapping"
)

// MaintainInlineComments reads chart/values.yaml at path, walks
// `suse-library.env` (when present), updates the LineComment of each
// `${binding:...}`-bearing entry to reflect the resolved value, and
// writes the file back atomically only when something changed.
//
// Returns true when the file was rewritten, false when no change was
// needed. Errors are wrapped for diagnostic surface.
//
// Safe to call on absent / empty / no-env files: those produce
// (false, nil) without touching disk.
func MaintainInlineComments(
	valuesPath string,
	mappings *dslmapping.Document,
	releaseName string,
) (changed bool, err error) {
	data, err := os.ReadFile(valuesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", valuesPath, err)
	}
	if len(data) == 0 {
		return false, nil
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		// Not our problem: someone else (loadValuesAsMap, render
		// itself) will fail loud with a better error message.
		return false, nil
	}

	// Need a generic-map view of the same file to drive collectBindings.
	// Re-parsing is cheap; sharing the Node tree would require
	// converting Node → map ourselves.
	var values map[string]any
	if err := yaml.Unmarshal(data, &values); err != nil {
		return false, nil
	}

	bindings := collectBindings(values, mappings, releaseName)
	if len(bindings) == 0 {
		// No services[] entries → no binding refs can be valid →
		// nothing to maintain.
		return false, nil
	}
	computedFields := computedFieldNamesByBinding(values, mappings)

	envNode := findEnvNode(&root)
	if envNode == nil {
		return false, nil
	}

	// Walk env entries, mutate LineComments where applicable.
	mutated := false
	for i := 0; i+1 < len(envNode.Content); i += 2 {
		valNode := envNode.Content[i+1]
		if valNode == nil || valNode.Kind != yaml.ScalarNode {
			continue
		}
		if !strings.Contains(valNode.Value, "${binding:") {
			continue
		}
		want := resolvedComment(valNode.Value, bindings, computedFields)
		if valNode.LineComment != want {
			valNode.LineComment = want
			mutated = true
		}
	}

	if !mutated {
		return false, nil
	}

	out, err := yaml.Marshal(&root)
	if err != nil {
		return false, fmt.Errorf("marshal %s: %w", valuesPath, err)
	}
	tmp := valuesPath + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, valuesPath); err != nil {
		_ = os.Remove(tmp)
		return false, fmt.Errorf("rename %s -> %s: %w", tmp, valuesPath, err)
	}
	return true, nil
}

// findEnvNode walks the YAML document tree to find the
// `suse-library.env` mapping node. Returns nil when missing or when
// any intermediate node is not a mapping (silently — render handles
// the wrong-shape case loud elsewhere).
func findEnvNode(root *yaml.Node) *yaml.Node {
	if root == nil || len(root.Content) == 0 {
		return nil
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return nil
	}
	suse := mapNodeGet(doc, "suse-library")
	if suse == nil || suse.Kind != yaml.MappingNode {
		return nil
	}
	env := mapNodeGet(suse, "env")
	if env == nil || env.Kind != yaml.MappingNode {
		return nil
	}
	return env
}

// mapNodeGet returns the value Node associated with key in a mapping
// Node, or nil when absent.
func mapNodeGet(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := m.Content[i]
		if k != nil && k.Kind == yaml.ScalarNode && k.Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// resolvedComment computes the `# → ...` comment text for a single
// env value. The leading "→ " marker identifies rda-managed comments;
// computed/derived fields (chart-author binding_fields) get an extra
// "(derived: <field>)" annotation so the dev knows the source.
//
// On resolve error (typo, unknown binding/field), the comment carries
// the error rather than the resolved value — even more useful for
// debugging than silently leaving the stale value in place.
func resolvedComment(raw string, bindings map[string]*BindingFields, computedFields map[string]map[string]bool) string {
	resolved, err := resolveBindingRefsString(raw, bindings, "")
	if err != nil {
		return fmt.Sprintf("# → ERROR: %s", err.Error())
	}
	derivedNote := ""
	// If the raw is a single bare ref AND that field is in the
	// binding's computed set, mark as derived.
	if bindingName, fieldName, ok := parseBareBindingRef(strings.TrimSpace(raw)); ok {
		if fields, ok := computedFields[bindingName]; ok && fields[fieldName] {
			derivedNote = fmt.Sprintf("  (derived: %s.%s)", bindingName, fieldName)
		}
	}
	return fmt.Sprintf("# → %s%s", resolved, derivedNote)
}

// computedFieldNamesByBinding builds a binding name → set of
// chart-author-declared computed field names map. Used by
// resolvedComment to emit the "(derived: ...)" annotation only for
// fields that came from a `binding_fields:` template, not for
// hardcoded ones (host/port/url/etc.).
//
// Returns an empty map when mappings is nil — derived annotation just
// stops appearing, no harm done.
func computedFieldNamesByBinding(values map[string]any, mappings *dslmapping.Document) map[string]map[string]bool {
	out := map[string]map[string]bool{}
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
		bf := entry.Versions[0].BindingFields
		if len(bf) == 0 {
			continue
		}
		fields := map[string]bool{}
		for name := range bf {
			fields[name] = true
		}
		out[binding] = fields
	}
	return out
}
