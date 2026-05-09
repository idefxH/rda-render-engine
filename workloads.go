// workloads.go — workload shape resolution, default merging, and validation.
//
// Phase 1 of Design Orientation 0005. Adds multi-workload support to the
// render engine. Each workload[] entry describes one application workload
// (API server, worker, cron job, daemon). Shape defaults (web, worker,
// cron, daemon) provide sensible starting points that the developer can
// override per-workload.
//
// The output is a list of resolved workload maps ready for the projection
// pipeline to produce workloads_resolved[] in the final overlay.
package render

import (
	"fmt"
	"sort"
)

// shapeDefaults maps shape names to their default properties.
// Hardcoded for now — will be bundle-driven in Phase 2.
var shapeDefaults = map[string]map[string]any{
	"web": {
		"kind":     "Deployment",
		"port":     8080,
		"replicas": 1,
		"probes": map[string]any{
			"liveness":  map[string]any{"path": "/health", "initialDelaySeconds": 5, "periodSeconds": 10},
			"readiness": map[string]any{"path": "/ready", "initialDelaySeconds": 3, "periodSeconds": 5},
		},
		"resources": map[string]any{
			"requests": map[string]any{"cpu": "100m", "memory": "128Mi"},
			"limits":   map[string]any{"memory": "512Mi"},
		},
	},
	"worker": {
		"kind":     "Deployment",
		"replicas": 1,
		"probes":   nil,
		"resources": map[string]any{
			"requests": map[string]any{"cpu": "100m", "memory": "128Mi"},
			"limits":   map[string]any{"memory": "512Mi"},
		},
	},
	"cron": {
		"kind":          "CronJob",
		"probes":        nil,
		"restartPolicy": "OnFailure",
		"resources": map[string]any{
			"requests": map[string]any{"cpu": "50m", "memory": "64Mi"},
			"limits":   map[string]any{"memory": "256Mi"},
		},
	},
	"daemon": {
		"kind": "DaemonSet",
		"probes": map[string]any{
			"liveness": map[string]any{"path": "/health", "initialDelaySeconds": 10, "periodSeconds": 30},
		},
		"resources": map[string]any{
			"requests": map[string]any{"cpu": "50m", "memory": "64Mi"},
			"limits":   map[string]any{"memory": "128Mi"},
		},
	},
}

// bareDefaults are applied when no shape is specified.
var bareDefaults = map[string]any{
	"kind":     "Deployment",
	"replicas": 1,
}

// resolveWorkloads reads the workloads[] block from suse values,
// applies shape defaults under explicit config, and validates each
// workload entry. Returns a list of resolved workload maps.
//
// Each resolved workload has at minimum: name, kind, image.
// Shape defaults are deep-merged UNDER the explicit config — the
// developer's values always win.
func resolveWorkloads(suse map[string]any) ([]map[string]any, error) {
	workloadsRaw, ok := suse["workloads"].([]any)
	if !ok || len(workloadsRaw) == 0 {
		return nil, nil
	}

	seen := map[string]bool{}
	resolved := make([]map[string]any, 0, len(workloadsRaw))

	for i, wRaw := range workloadsRaw {
		w, ok := wRaw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("workloads[%d] is not a map (got %T)", i, wRaw)
		}

		name, _ := w["name"].(string)
		if name == "" {
			return nil, fmt.Errorf("workloads[%d]: missing required field 'name'", i)
		}
		if seen[name] {
			return nil, fmt.Errorf("workloads[%d]: duplicate workload name %q", i, name)
		}
		seen[name] = true

		// Validate image
		if _, hasImage := w["image"]; !hasImage {
			return nil, fmt.Errorf("workloads[%d] (name=%s): missing required field 'image'", i, name)
		}

		// Determine shape and get defaults
		shape, _ := w["shape"].(string)
		var defaults map[string]any
		if shape != "" {
			d, ok := shapeDefaults[shape]
			if !ok {
				validShapes := make([]string, 0, len(shapeDefaults))
				for k := range shapeDefaults {
					validShapes = append(validShapes, k)
				}
				sort.Strings(validShapes)
				return nil, fmt.Errorf("workloads[%d] (name=%s): unknown shape %q (valid: %v)",
					i, name, shape, validShapes)
			}
			defaults = deepCopyWorkloadMap(d)
		} else {
			defaults = deepCopyWorkloadMap(bareDefaults)
		}

		// Deep-merge: shape defaults go UNDER explicit config.
		// Start from defaults, then merge explicit config ON TOP.
		merged := defaults
		deepMergeWorkload(merged, w)

		// Ensure kind is set
		if _, hasKind := merged["kind"]; !hasKind {
			merged["kind"] = "Deployment"
		}

		resolved = append(resolved, merged)
	}

	return resolved, nil
}

// deepMergeWorkload recursively merges src into dst. Maps are merged
// key-by-key; scalars and lists from src overwrite dst. Nil values in
// src explicitly set the key to nil (important for probes: null).
func deepMergeWorkload(dst, src map[string]any) {
	for k, srcVal := range src {
		if srcVal == nil {
			dst[k] = nil
			continue
		}
		dstVal, exists := dst[k]
		if !exists {
			dst[k] = srcVal
			continue
		}
		dstMap, dstIsMap := dstVal.(map[string]any)
		srcMap, srcIsMap := srcVal.(map[string]any)
		if dstIsMap && srcIsMap {
			deepMergeWorkload(dstMap, srcMap)
			continue
		}
		// Scalar / list / type-mismatch: src wins.
		dst[k] = srcVal
	}
}

// deepCopyWorkloadMap produces an independent copy of a workload map
// so shape defaults are not mutated across calls.
func deepCopyWorkloadMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		switch t := v.(type) {
		case map[string]any:
			out[k] = deepCopyWorkloadMap(t)
		case []any:
			cp := make([]any, len(t))
			copy(cp, t)
			out[k] = cp
		default:
			out[k] = v
		}
	}
	return out
}
