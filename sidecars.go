package render

import (
	"fmt"

	"github.com/idefxH/rda-render-engine/dslmapping"
)

// projectSidecars processes a service with inject: sidecar and renders
// sidecar container specs into suse-library.sidecars[]. DO-0004 Phase 3b.
//
// The library chart's deployment.yaml iterates sidecars[] and adds
// each as an additional container in the app pod.
func projectSidecars(
	svc map[string]any,
	suseOut map[string]any,
	ver dslmapping.VersionEntry,
	binding string,
) error {
	inject, _ := svc["inject"].(string)
	if inject != "sidecar" {
		return nil
	}

	if ver.SidecarTemplate == nil {
		return fmt.Errorf("services[binding=%s] has inject: sidecar but chart has no sidecar_template in dsl-mappings", binding)
	}

	tmpl := ver.SidecarTemplate

	sidecar := map[string]any{
		"name":  binding,
		"image": tmpl.Image,
	}

	if tmpl.Resources != nil {
		sidecar["resources"] = tmpl.Resources
	}

	// Read sidecar-specific config from the service entry
	if env, ok := svc["env"].(map[string]any); ok {
		envList := []map[string]any{}
		for k, v := range env {
			envList = append(envList, map[string]any{"name": k, "value": fmt.Sprintf("%v", v)})
		}
		sidecar["env"] = envList
	}

	// Append to existing sidecars
	var sidecars []any
	if existing, ok := suseOut["sidecars"].([]any); ok {
		sidecars = existing
	}
	sidecars = append(sidecars, sidecar)
	suseOut["sidecars"] = sidecars

	return nil
}

// projectWorkloadSidecars processes services[] entries that inject as
// sidecars into a specific workload. The sidecar is appended to the
// workload's own sidecars list rather than a global suseOut["sidecars"].
//
// Each workload entry in workloads_resolved carries its own "sidecars"
// key — a list of sidecar container specs. The library chart's
// deployment.yaml iterates workloads_resolved[].sidecars.
func projectWorkloadSidecars(
	svc map[string]any,
	workload map[string]any,
	ver dslmapping.VersionEntry,
	binding string,
) error {
	inject, _ := svc["inject"].(string)
	if inject != "sidecar" {
		return nil
	}

	if ver.SidecarTemplate == nil {
		return fmt.Errorf("services[binding=%s] has inject: sidecar but chart has no sidecar_template in dsl-mappings", binding)
	}

	tmpl := ver.SidecarTemplate

	sidecar := map[string]any{
		"name":  binding,
		"image": tmpl.Image,
	}

	if tmpl.Resources != nil {
		sidecar["resources"] = tmpl.Resources
	}

	// Read sidecar-specific config from the service entry
	if env, ok := svc["env"].(map[string]any); ok {
		envList := []map[string]any{}
		for k, v := range env {
			envList = append(envList, map[string]any{"name": k, "value": fmt.Sprintf("%v", v)})
		}
		sidecar["env"] = envList
	}

	// Append to the workload's sidecars list
	var sidecars []any
	if existing, ok := workload["sidecars"].([]any); ok {
		sidecars = existing
	}
	sidecars = append(sidecars, sidecar)
	workload["sidecars"] = sidecars

	return nil
}
