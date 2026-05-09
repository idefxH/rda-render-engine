// bootstrap.go — projects services[].bootstrap.jobs[] entries into the
// overlay's flat suse-library.bootstrap_jobs[] list.
//
// Why a flat list instead of per-binding under <binding>.bootstrap.jobs:
// the bundle's library chart renders one Helm Job per entry; iterating
// a flat list is straightforward `range .Values.bootstrap_jobs`.
// Per-binding nesting would require the template to walk services[]
// AND the overlay's <binding>.* — twice the iteration. The flat list
// also keeps the overlay shape consistent regardless of how many
// bindings carry bootstrap.jobs.
//
// Cross-binding refs (${binding:NAME.field}, ${binding-self:field})
// inside job.env / job.command / job.image / job.args are resolved
// here, NOT at Helm-template time. This keeps the resolution rules
// concentrated in render and lets the bundle template stay dumb.
//
// Closes idefxH/rda-cli#92.
package render

import (
	"fmt"
)

// projectBootstrapJobs walks services[] and emits a {binding, type,
// job: <resolved>} entry per bootstrap.jobs[] item into the overlay's
// suse-library.bootstrap_jobs[] list.
//
// Skipped silently for services that are disabled, not catalogued, or
// have provisioning != local (jobs that run against a shared / external
// binding need the operator's CI, not Helm hooks on the dev cluster).
//
// Returns the projected list (or nil if no jobs across services), and
// an error when ${binding:...} resolution fails.
func projectBootstrapJobs(values map[string]any, bindings map[string]*BindingFields) ([]any, error) {
	suse, ok := values["suse-library"].(map[string]any)
	if !ok {
		return nil, nil
	}
	servicesRaw, ok := suse["services"].([]any)
	if !ok {
		return nil, nil
	}

	var out []any
	for _, svcRaw := range servicesRaw {
		svc, ok := svcRaw.(map[string]any)
		if !ok {
			continue
		}
		binding, _ := svc["binding"].(string)
		chartType, _ := svc["type"].(string)
		if binding == "" {
			continue
		}
		// Skip disabled services (matches the projection contract:
		// disabled = inert, no rendering).
		if enabled, ok := svc["enabled"].(bool); ok && !enabled {
			continue
		}
		// Skip non-local: the binding-secret it depends on lives in a
		// remote cluster (shared) or external endpoint; running a Helm
		// post-install Job on the dev cluster won't have access.
		provisioning := "deploy"
		if p, ok := svc["provisioning"].(string); ok && p != "" {
			provisioning = p
		}
		if provisioning != "deploy" {
			continue
		}

		bootstrap, ok := svc["bootstrap"].(map[string]any)
		if !ok {
			continue
		}
		jobsRaw, ok := bootstrap["jobs"].([]any)
		if !ok {
			continue
		}
		for i, jobRaw := range jobsRaw {
			job, ok := jobRaw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("services[binding=%s].bootstrap.jobs[%d] must be a map (got %T)",
					binding, i, jobRaw)
			}
			resolved, err := resolveBindingRefs(job, bindings, binding)
			if err != nil {
				return nil, fmt.Errorf("services[binding=%s].bootstrap.jobs[%d]: %w",
					binding, i, err)
			}
			out = append(out, map[string]any{
				"binding": binding,
				"type":    chartType,
				"job":     resolved,
			})
		}
	}
	return out, nil
}
