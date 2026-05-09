package render

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ChartDep mirrors a Chart.yaml dependency entry.
type ChartDep struct {
	Name       string `yaml:"name"`
	Version    string `yaml:"version"`
	Repository string `yaml:"repository"`
	Condition  string `yaml:"condition"`
	Alias      string `yaml:"alias,omitempty"`
}

// PatchChartDeps reads the library chart's Chart.yaml, replaces single-type
// entries with aliased entries for multi-instance types, and writes the
// result. Idempotent: running twice produces the same output.
//
// aliases maps binding → chart alias (from ComputeAliases).
// Returns the number of aliases injected (0 when no multi-instance).
func PatchChartDeps(chartYAMLPath string, aliases map[string]string) (int, error) {
	data, err := os.ReadFile(chartYAMLPath)
	if err != nil {
		return 0, fmt.Errorf("read Chart.yaml: %w", err)
	}

	var chart map[string]any
	if err := yaml.Unmarshal(data, &chart); err != nil {
		return 0, fmt.Errorf("parse Chart.yaml: %w", err)
	}

	depsRaw, ok := chart["dependencies"]
	if !ok {
		return 0, nil
	}
	depsSlice, ok := depsRaw.([]any)
	if !ok {
		return 0, nil
	}

	aliasSet := map[string]bool{}
	for _, a := range aliases {
		aliasSet[a] = true
	}

	// Parse existing deps.
	var deps []ChartDep
	for _, d := range depsSlice {
		dm, ok := d.(map[string]any)
		if !ok {
			continue
		}
		dep := ChartDep{}
		if v, ok := dm["name"].(string); ok {
			dep.Name = v
		}
		if v, ok := dm["version"].(string); ok {
			dep.Version = v
		}
		if v, ok := dm["repository"].(string); ok {
			dep.Repository = v
		}
		if v, ok := dm["condition"].(string); ok {
			dep.Condition = v
		}
		if v, ok := dm["alias"].(string); ok {
			dep.Alias = v
		}
		deps = append(deps, dep)
	}

	// For each dep, find which aliases it serves.
	// The dep's effective name is alias (if set) or name.
	depsByEffectiveName := map[string]ChartDep{}
	for _, dep := range deps {
		ename := dep.Alias
		if ename == "" {
			ename = dep.Name
		}
		depsByEffectiveName[ename] = dep
	}

	// Collect aliases that need new dep entries.
	// An alias needs a dep entry if it doesn't match any existing dep's
	// effective name.
	injected := 0
	var newDeps []ChartDep

	// Keep deps whose effective name is still in the alias set OR
	// whose effective name is NOT a type that became multi-instance.
	multiInstanceTypes := map[string]bool{}
	for _, dep := range deps {
		ename := dep.Alias
		if ename == "" {
			ename = dep.Name
		}
		// Count how many aliases start with this type.
		count := 0
		for _, a := range aliases {
			if a == ename || strings.HasPrefix(a, ename+"-") {
				count++
			}
		}
		if count > 1 {
			multiInstanceTypes[ename] = true
		}
	}

	for _, dep := range deps {
		ename := dep.Alias
		if ename == "" {
			ename = dep.Name
		}
		if multiInstanceTypes[ename] {
			// This dep is being replaced by aliased entries — skip it.
			continue
		}
		newDeps = append(newDeps, dep)
	}

	// Add aliased entries for multi-instance types.
	for typeName := range multiInstanceTypes {
		// Find the original dep for this type.
		origDep := depsByEffectiveName[typeName]

		// Collect all aliases for this type, sorted for determinism.
		var typeAliases []string
		for _, a := range aliases {
			if a == typeName || strings.HasPrefix(a, typeName+"-") {
				typeAliases = append(typeAliases, a)
			}
		}
		sort.Strings(typeAliases)

		for _, alias := range typeAliases {
			newDep := ChartDep{
				Name:       origDep.Name,
				Version:    origDep.Version,
				Repository: origDep.Repository,
				Alias:      alias,
				Condition:  alias + ".enabled",
			}
			newDeps = append(newDeps, newDep)
			injected++
		}
	}

	if injected == 0 {
		return 0, nil
	}

	// Convert back to []any for YAML marshaling.
	var depsOut []any
	for _, d := range newDeps {
		m := map[string]any{
			"name":       d.Name,
			"version":    d.Version,
			"repository": d.Repository,
			"condition":  d.Condition,
		}
		if d.Alias != "" {
			m["alias"] = d.Alias
		}
		depsOut = append(depsOut, m)
	}
	chart["dependencies"] = depsOut

	out, err := yaml.Marshal(chart)
	if err != nil {
		return 0, fmt.Errorf("marshal Chart.yaml: %w", err)
	}
	if err := os.WriteFile(chartYAMLPath, out, 0o644); err != nil {
		return 0, fmt.Errorf("write Chart.yaml: %w", err)
	}

	return injected, nil
}

// ChartAliasesFromServices computes aliases and returns them for use by
// both the render pipeline and Chart.yaml patching. Convenience wrapper
// around ComputeAliases that parses services from a values map.
func ChartAliasesFromServices(values map[string]any) map[string]string {
	suse, ok := values["suse-library"].(map[string]any)
	if !ok {
		return nil
	}
	servicesRaw, ok := suse["services"].([]any)
	if !ok {
		return nil
	}
	var svcMaps []map[string]any
	for _, raw := range servicesRaw {
		if m, ok := raw.(map[string]any); ok {
			svcMaps = append(svcMaps, m)
		}
	}
	return ComputeAliases(svcMaps)
}
