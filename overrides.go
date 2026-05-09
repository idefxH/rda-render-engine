// overrides.go — services[].overrides.<stage> merge at render time.
//
// Lets devs declare per-environment knobs without overlay-fork-or-set
// hell:
//
//   - binding: payments-db
//     type: postgresql
//     persistence:
//       enabled: true
//       size: 1Gi                    # local default
//     overrides:
//       staging:
//         persistence: { size: 50Gi }
//       prod:
//         persistence: { size: 500Gi, storageClass: io1 }
//         resources: { requests: { memory: 4Gi, cpu: 1 } }
//
// `rda render --stage staging` deep-merges overrides.staging into the
// entry before projection. `rda promote --target staging` invokes the
// same path. Local dev (--stage absent) leaves the entry unchanged.
//
// Merge semantics:
//   - Deep-merge on maps (recursive).
//   - Last-wins on scalars: override replaces base.
//   - Lists are replaced wholesale (no element-wise merging).
//   - The `overrides` key is removed from the entry after merge so it
//     doesn't leak into the rendered chart values (or trip
//     validateConsistency's strict-key check at template time).
//
// Closes idefxH/rda-cli#93.
package render

// applyAppOverrides deep-merges suse-library.overrides.<stage> into the
// suse-library block itself, then drops the overrides key. Same semantics
// as services[].overrides but at the app level.
// Must run BEFORE applyStageOverrides so services inherit the merged domain.
func applyAppOverrides(values map[string]any, stage string) {
	suse, ok := values["suse-library"].(map[string]any)
	if !ok {
		return
	}
	ovRaw, ok := suse["overrides"]
	if !ok {
		return
	}
	ov, ok := ovRaw.(map[string]any)
	if !ok {
		delete(suse, "overrides")
		return
	}
	stageOverrideRaw, ok := ov[stage]
	delete(suse, "overrides")
	if !ok {
		return
	}
	stageOverride, ok := stageOverrideRaw.(map[string]any)
	if !ok {
		return
	}
	deepMergeOverrides(suse, stageOverride)
}

// applyStageOverrides walks values["suse-library"]["services"][] and
// for each entry, deep-merges entry.overrides.<stage> into the entry
// then drops the overrides key. Silent no-op when:
//   - values has no suse-library / services block
//   - the entry has no overrides field
//   - overrides has no entry for the requested stage
func applyStageOverrides(values map[string]any, stage string) {
	suse, ok := values["suse-library"].(map[string]any)
	if !ok {
		return
	}
	servicesRaw, ok := suse["services"].([]any)
	if !ok {
		return
	}
	for _, svcRaw := range servicesRaw {
		svc, ok := svcRaw.(map[string]any)
		if !ok {
			continue
		}
		ovRaw, ok := svc["overrides"]
		if !ok {
			continue
		}
		ov, ok := ovRaw.(map[string]any)
		if !ok {
			// Drop malformed overrides so they don't pollute the entry.
			delete(svc, "overrides")
			continue
		}
		stageOverrideRaw, ok := ov[stage]
		// Always drop the overrides key after merge attempt — even when
		// the requested stage isn't declared. The key is render-time-only
		// metadata; leaving it in the entry would leak into the projected
		// chart values.
		delete(svc, "overrides")
		if !ok {
			continue
		}
		stageOverride, ok := stageOverrideRaw.(map[string]any)
		if !ok {
			continue
		}
		deepMergeOverrides(svc, stageOverride)
	}
}

// deepMergeOverrides recursively merges src into dst with the
// semantics documented at the top of the file. Adapted from
// projection.go::deepMerge but specialised for the override case
// (we want a fresh function name so the intent is clear at the call
// site, even if the body is currently identical — projection's
// deepMerge handles passthrough merge with the same shape).
func deepMergeOverrides(dst, src map[string]any) {
	for k, srcVal := range src {
		dstVal, exists := dst[k]
		if !exists {
			dst[k] = srcVal
			continue
		}
		dstMap, dstIsMap := dstVal.(map[string]any)
		srcMap, srcIsMap := srcVal.(map[string]any)
		if dstIsMap && srcIsMap {
			deepMergeOverrides(dstMap, srcMap)
			continue
		}
		// Scalar / list / type-mismatch: src wins.
		dst[k] = srcVal
	}
}
