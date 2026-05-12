// Package render projects the unified `services[]` DSL into a chart-specific
// values overlay that Helm reads alongside the user's chart/values.yaml.
//
// Why this package exists:
//
// Helm resolves sub-chart values BEFORE templates run. So the library chart's
// _helpers.tpl cannot inject `services[].auth.user.name` into
// `postgresql.auth.username` at template time — by the time helpers run, the
// postgres sub-chart has already loaded its values. The projection has to
// happen out of band, pre-helm. That's what this package does: walk the DSL,
// apply per-chart values_mapping from the bundle's dsl-mappings.yaml, return
// the projected overlay shape.
//
// The output is consumed by:
//   - rda render (cmd/render.go) — writes the overlay to chart/values.generated.yaml
//   - rda promote (Phase 2 follow-on) — runs --check as a pre-flight
//   - tilt-extension-suse-rda — invokes rda render before helm dep update
//
// The function is pure: no I/O, no globals, no side effects. Easy to unit-test
// without booting cobra or touching disk.
//
// Anchor: idefxH/rda-cli/rda.md `BEHAVIOR: render`. Closes the gap surfaced by
// idefxH/rda-opinion-bundle-example#66.
package render

import (
	"fmt"
	"sort"
	"strings"

	"text/template"

	semver "github.com/Masterminds/semver/v3"

	"github.com/idefxH/rda-render-engine/dslmapping"
	"github.com/idefxH/rda-render-engine/errs"
)

// Result is the outcome of one Project call.
type Result struct {
	// Overlay is the projected values shape. Keys are top-level
	// (e.g. "suse-library") matching how the user writes their values.yaml.
	Overlay map[string]any

	// ProjectionsCount is the number of services[] entries that produced a
	// projection. Entries with provisioning != local, or types not in the
	// catalogue, are NOT counted.
	ProjectionsCount int

	// Warnings lists best-effort issues that don't fail the projection but
	// the user should know about: type not catalogued, multi-binding-same-
	// type collision, fallback to no-projection because the bundle is older
	// than v0.11. Each warning is a one-line stderr-friendly string.
	Warnings []string
}

// Project walks values["suse-library"]["services"][] and applies each service
// type's values_mapping from the bundle's dsl-mappings.yaml. Returns the
// projected overlay (also wrapped under "suse-library:") plus per-call
// telemetry.
//
// When mappings is nil (older bundle without dsl-mappings.yaml, or unreachable
// bundle), returns an empty overlay with a notice in Warnings — safe to wire
// into Tiltfile and CI unconditionally.
//
// When values has no `suse-library.services[]` block, returns an empty overlay
// with no warnings — render is idempotent on projects that haven't called
// add-service yet.
//
// Errors are returned only for catastrophic problems (malformed structure
// where a map was expected). The DSL contract has loud-failure points
// elsewhere (validateConsistency at template time, required: true at
// binding-secret render time); render stays out of validation.
// Project applies values_mapping + chart_defaults + passthrough projection
// per services[] entry. releaseName is the Helm release name (the project
// name) — used for ${binding:NAME.field} cross-binding reference
// resolution.
//
// stage activates per-environment overrides: when non-empty, each
// services[] entry's `overrides.<stage>` map is deep-merged into the
// entry before projection. Use "" for the local/dev case (no merge).
// Closes idefxH/rda-cli#93.
func Project(values map[string]any, mappings *dslmapping.Document, releaseName string) (Result, error) {
	return ProjectWithStage(values, mappings, releaseName, "")
}

// ProjectWithStage is the full-fat entry point. Project() wraps it
// for the no-stage case so existing call sites stay simple.
func ProjectWithStage(values map[string]any, mappings *dslmapping.Document, releaseName, stage string) (Result, error) {
	// Apply per-stage overrides to services[] entries before projection.
	// Pure mutation of the values input (the caller's map) — render is
	// already side-effecting on the values it walks (chart_defaults,
	// passthrough merge), and making a deep copy here would double the
	// memory cost on every render.
	if stage != "" {
		trace("phase0", "", "applyStageOverrides", fmt.Sprintf("stage=%s", stage))
		preProcessWorkloadOverrides(values, stage)
		applyAppOverrides(values, stage)
		applyStageOverrides(values, stage)
		applyWorkloadOverrides(values, stage)
	}
	resolveTemplates(values)
	if suse, ok := values["suse-library"].(map[string]any); ok {
		if d, ok := suse["domain"].(string); ok && d != "" {
			trace("phase0", "", "resolveTemplates", fmt.Sprintf("domain=%s", d))
		}
	}

	res := Result{Overlay: map[string]any{}}

	// Pre-pass: build the binding name → fields map. Used by
	// resolveBindingRefs when projecting passthrough / chart_defaults
	// values that contain ${binding:...} references. Empty when no
	// services[] / no mappings — refs at projection time then fail loud.
	bindings := collectBindings(values, mappings, releaseName)
	trace("phase1", "", "collectBindings", fmt.Sprintf("%d binding(s)", len(bindings)))
	_ = bindings // kept for use below

	if mappings == nil {
		res.Warnings = append(res.Warnings,
			"no dsl-mappings.yaml in the resolved bundle — projection skipped. "+
				"This is expected on bundles older than v0.11.0; the DSL is "+
				"decorative for sub-chart configuration in that case.")
		return res, nil
	}

	suse, ok := values["suse-library"].(map[string]any)
	if !ok {
		return res, nil // no suse-library block — nothing to project
	}

	servicesRaw, _ := suse["services"].([]any)

	suseOut := map[string]any{}

	// Compute chart aliases for multi-instance support (#24).
	// Single-instance types: alias = type (backward compatible).
	// Multi-instance types: alias = <type>-<binding> for EVERY instance.
	svcMaps := make([]map[string]any, 0, len(servicesRaw))
	for _, raw := range servicesRaw {
		if m, ok := raw.(map[string]any); ok {
			svcMaps = append(svcMaps, m)
		}
	}
	aliases := ComputeAliases(svcMaps)

	for i, svcRaw := range servicesRaw {
		svc, ok := svcRaw.(map[string]any)
		if !ok {
			return res, fmt.Errorf("services[%d] is not a map (got %T)", i, svcRaw)
		}

		binding, _ := svc["binding"].(string)
		chartType, _ := svc["type"].(string)
		if binding == "" || chartType == "" {
			// Library validateConsistency catches this loud at template
			// time. Render stays best-effort: skip silently.
			continue
		}

		// Closes rda-cli#65 + rda-cli#67: services[].enabled drives
		// projection. Disabled entries are inert scaffolds — the dev hasn't
		// activated them yet, so we don't project any values, and we don't
		// flip <chart>.enabled for them. Default-when-missing is true:
		// pre-0.1.38 projects (scaffolded before this field existed) keep
		// projecting as before. Fresh scaffolds via `rda add-service` since
		// 0.1.38 explicitly write enabled: false.
		if enabled, ok := svc["enabled"].(bool); ok && !enabled {
			continue
		}

		provisioning := "deploy"
		if p, ok := svc["provisioning"].(string); ok && p != "" {
			provisioning = p
		}
		// Normalize legacy names
		if provisioning == "local" {
			provisioning = "deploy"
		}
		if provisioning == "external" || provisioning == "shared" {
			provisioning = "connect"
		}
		if provisioning == "operator" {
			// DO-0004 Phase 3c: operator-managed instances create a CR
			// instead of deploying a sub-chart. The CR goes into
			// suse-library.crds[] (same path as CRD projection).
			// The operator binding (referenced by svc["operator"]) must
			// be a separate services[] entry with provisioning: local|shared.
			operatorBinding, _ := svc["operator"].(string)
			if operatorBinding != "" && mappings != nil && mappings.HasType(chartType) {
				entry := mappings.Charts[chartType]
				if len(entry.Versions) > 0 {
					ver := entry.Versions[0]
					if ver.CRDProjection != nil {
						// Use CRD projection to create the operator CR
						if err := projectCRDs(svc, suseOut, ver, binding, bindings, releaseName, 0); err != nil {
							return res, err
						}
					}
				}
			}
			res.ProjectionsCount++
			continue
		}
		if provisioning != "deploy" {
			// shared / external entries don't deploy a sub-chart.
			continue
		}

		if !mappings.HasType(chartType) {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("services[binding=%s].type=%q is not in dsl-mappings.yaml — "+
					"no projection performed for this binding. validateConsistency "+
					"will fail loud at helm template time.", binding, chartType))
			continue
		}

		// Resolve chart alias for this binding (#24 multi-instance).
		chartAlias := aliases[binding]
		if chartAlias == "" {
			chartAlias = chartType // fallback for safety
		}

		// Auto-set <alias>.enabled: true. The library chart's Chart.yaml
		// gates each dep on `condition: <alias>.enabled`.
		chartBlock := getOrInitMap(suseOut, chartAlias)
		chartBlock["enabled"] = true

		// Apply values_mapping for this chart type. Selects the versions[]
		// entry matching the service's branch constraint (DO-0005 Phase 2).
		entry, ok := mappings.Charts[chartType]
		if !ok || len(entry.Versions) == 0 {
			continue
		}
		ver := selectVersion(entry, svc)

		// Stable iteration: sort dsl-paths for deterministic output. Helps
		// --check produce a stable diff on no-op runs.
		dslPaths := make([]string, 0, len(ver.ValuesMapping))
		for k := range ver.ValuesMapping {
			dslPaths = append(dslPaths, k)
		}
		sort.Strings(dslPaths)

		for _, dslPath := range dslPaths {
			valuesPath := AliasedPath(ver.ValuesMapping[dslPath], chartType, chartAlias)
			val, found := digDSL(svc, dslPath)
			if !found {
				continue
			}
			// Skip child fields of disabled parents. When
			// persistence.enabled=false, don't project persistence.size
			// — the chart template may create volumeMounts for values
			// that exist even when persistence is off.
			if parent, _, ok := strings.Cut(dslPath, "."); ok {
				if parent != dslPath {
					enabledVal, enabledFound := digDSL(svc, parent+".enabled")
					if enabledFound && (enabledVal == false || enabledVal == "false") {
						if dslPath != parent+".enabled" {
							continue
						}
					}
				}
			}
			if err := setAtPath(suseOut, valuesPath, val); err != nil {
				return res, fmt.Errorf("services[binding=%s].%s -> %s: %w",
					binding, dslPath, valuesPath, err)
			}
		}

		// chart_defaults projection (NS Phase G, rda-cli 0.1.49).
		// Literal fill-in values the chart REQUIRES but the DSL doesn't
		// surface — typically shape adapters between the unified DSL and a
		// chart's specific schema. Example: dex's ingress requires
		// `hosts: [{host, paths: [{path, pathType}]}]`; the DSL writes
		// `host` from values_mapping, chart_defaults fills `paths`.
		//
		// Applied AFTER values_mapping (so the projection has already
		// seeded the chart block, e.g. hosts[0].host has been written) and
		// BEFORE passthrough (so the user's escape-hatch values still win
		// over chart_defaults). Skipped silently when chart_defaults is
		// absent — backwards-compatible with mappings that don't declare
		// it.
		//
		// Stable iteration: sort target paths so --check produces a stable
		// diff on no-op runs (mirrors the values_mapping loop above).
		if len(ver.ChartDefaults) > 0 {
			defaultPaths := make([]string, 0, len(ver.ChartDefaults))
			for k := range ver.ChartDefaults {
				defaultPaths = append(defaultPaths, k)
			}
			sort.Strings(defaultPaths)
			for _, target := range defaultPaths {
				val := ver.ChartDefaults[target]
				resolved, err := resolveBindingRefs(val, bindings, binding)
				if err != nil {
					return res, fmt.Errorf("chart_defaults[%s][%s]: %w",
						chartType, target, err)
				}
				aliasedTarget := AliasedPath(target, chartType, chartAlias)
				if err := setAtPath(suseOut, aliasedTarget, resolved); err != nil {
					return res, fmt.Errorf("chart_defaults[%s] -> %s: %w",
						chartType, aliasedTarget, err)
				}
			}
		}

		// derived_values projection (rda-cli 0.1.55+).
		//
		// Each entry's `template` is a Go text/template rendered with:
		//   .Service           the services[] entry (so .Service.ingress.host works)
		//   .Release.Name      releaseName arg (project name)
		//   .Binding           the entry's binding
		//   .Type              the entry's chart type
		//
		// `skip_if` is a dotted DSL path; when its value is non-empty, the
		// derivation is skipped (user override takes precedence).
		//
		// Use case: dex's `config.issuer` derives from `services[].ingress.host`
		// when ingress is enabled, falling back to the in-cluster service URL.
		// Eliminates the issuer/ingress mismatch footgun (user typing a
		// :5556 in the issuer URL while ingress routes to port 80).
		for _, dv := range ver.DerivedValues {
			if dv.SkipIf != "" {
				existing, ok := digDSL(svc, dv.SkipIf)
				if ok && !isEmpty(existing) {
					continue // user provided an override, leave their value alone
				}
			}
			if dv.Template == "" {
				return res, fmt.Errorf("derived_values[%s] target=%s: missing template",
					chartType, dv.Target)
			}
			tpl, err := template.New(chartType + "/" + dv.Target).Parse(dv.Template)
			if err != nil {
				return res, fmt.Errorf("derived_values[%s] target=%s parse: %w",
					chartType, dv.Target, err)
			}
			domain, _ := suse["domain"].(string)
			input := map[string]any{
				"Service":    svc,
				"Release":    map[string]any{"Name": releaseName},
				"Binding":    binding,
				"Type":       chartType,
				"ChartAlias": chartAlias,
				"Domain":     domain,
			}
			var buf strings.Builder
			if err := tpl.Execute(&buf, input); err != nil {
				return res, fmt.Errorf("derived_values[%s] target=%s render: %w",
					chartType, dv.Target, err)
			}
			aliasedTarget := AliasedPath(dv.Target, chartType, chartAlias)
			if err := setAtPath(suseOut, aliasedTarget, buf.String()); err != nil {
				return res, fmt.Errorf("derived_values[%s] -> %s: %w",
					chartType, aliasedTarget, err)
			}
		}

		// Passthrough projection (BEHAVIOR/render step 5h, rda.md 0.1.43).
		// The DSL covers common knobs (auth, persistence, ingress, ...);
		// every other chart-specific value lands in services[].passthrough.
		// Convention: passthrough is rooted at the chart values DIRECTLY —
		// for type=grafana the user writes
		//   passthrough.sidecar.dashboards.enabled: true
		// which projects to suse-library.grafana.sidecar.dashboards.enabled.
		//
		// Sur-nesting fail-loud: if `passthrough.<chartType>` exists (the
		// common typo where the dev kept the chart-name key thinking the
		// block was keyed by chart), we return ERR_PASSTHROUGH_SURNESTED
		// rather than projecting the inner block. Pre-0.1.43 the bad shape
		// was silently dropped — `payments` shipped weeks with a missing
		// grafana sidecar before anyone noticed.
		//
		// DSL ↔ passthrough collisions on identical paths are caught loud
		// at helm-template time by suse-library.dsl.validatePassthrough in
		// the bundle's library chart. Render stays out of validation; we
		// do the merge here, the library does the collision contract.
		if ptRaw, ok := svc["passthrough"]; ok && ptRaw != nil {
			pt, ok := ptRaw.(map[string]any)
			if !ok {
				return res, fmt.Errorf(
					"services[binding=%s,type=%s].passthrough must be a map (got %T)",
					binding, chartType, ptRaw)
			}
			surNestedKey := ""
			if _, ok := pt[chartType]; ok {
				surNestedKey = chartType
			} else if chartAlias != chartType {
				if _, ok := pt[chartAlias]; ok {
					surNestedKey = chartAlias
				}
			}
			if surNestedKey != "" {
				return res, fmt.Errorf(
					"%w: services[binding=%s,type=%s].passthrough has a top-level %q key — "+
						"passthrough is rooted at the chart values directly. "+
						"Drop the extra %q nesting: write "+
						"`passthrough.<sub>.<key>: value` (e.g. "+
						"`passthrough.sidecar.dashboards.enabled: true`), "+
						"NOT `passthrough.%s.<sub>.<key>: value`. "+
						"See rda-docs/concepts/passthrough.md.",
					errs.ErrPassthroughSurnested, binding, chartType, surNestedKey, surNestedKey, surNestedKey)
			}
			// Resolve ${binding:NAME.field} / ${binding-self:field}
			// references before merging. This is where dex's
			// passthrough.config.storage.config.host = ${binding:postgres-state.host}
			// gets resolved to the actual postgres binding's host.
			resolved, err := resolveBindingRefs(pt, bindings, binding)
			if err != nil {
				return res, fmt.Errorf(
					"services[binding=%s,type=%s].passthrough: %w",
					binding, chartType, err)
			}
			resolvedMap, _ := resolved.(map[string]any)
			deepMerge(chartBlock, resolvedMap)
		}

		// Step 5.i: capabilities projection (Phase 2.2 of DO 0001).
		// Walk svc.bootstrap.<cap> per the chart's CapabilitySpec
		// (loaded in Phase 2.1 / #114); apply Schema validation,
		// Transforms, and project to the chart values overlay via
		// the per-Backend dispatch. Forward-compat: charts without
		// capabilities declared in dsl-mappings simply skip the
		// step (no-op when ver.Capabilities is empty).
		if len(ver.Capabilities) > 0 {
			aliasedVer := ver
			if chartAlias != chartType {
				aliasedVer = aliasVersionEntry(ver, chartType, chartAlias)
			}
			warnings, err := projectCapabilities(svc, suseOut, aliasedVer, binding, chartAlias, "")
			if err != nil {
				return res, err
			}
			res.Warnings = append(res.Warnings, warnings...)
		}

		// Step 5.i: cross-binding dependency wiring (DO-0004 Phase 1).
		if len(ver.Dependencies) > 0 {
			if err := projectDependencies(svc, suseOut, ver, binding, chartAlias, chartType, bindings, servicesRaw, mappings, releaseName); err != nil {
				return res, err
			}
		}

		// Step 5.i2: CRD projection (DO-0004 Phase 2).
		if ver.CRDProjection != nil {
			appPort := 8080
			if p, ok := suse["port"].(int); ok {
				appPort = p
			}
			if err := projectCRDs(svc, suseOut, ver, binding, bindings, releaseName, appPort); err != nil {
				return res, err
			}
		}

		// Step 5.j: cross-binding datasource wiring (DO 0002).
		if ver.DatasourcesTarget != "" {
			if err := projectDatasources(svc, suseOut, ver, binding, chartAlias, bindings, values, mappings); err != nil {
				return res, err
			}
		}

		res.ProjectionsCount++
	}

	// Sidecar injection (DO-0004 Phase 3b).
	// Services with inject: sidecar don't deploy a sub-chart — they add
	// a container to the app's Deployment. Processed outside the main
	// projection loop because they bypass the provisioning/enable checks.
	for _, svcRaw := range servicesRaw {
		svc, ok := svcRaw.(map[string]any)
		if !ok {
			continue
		}
		inject, _ := svc["inject"].(string)
		if inject != "sidecar" {
			continue
		}
		binding, _ := svc["binding"].(string)
		chartType, _ := svc["type"].(string)
		if binding == "" || chartType == "" {
			continue
		}
		if enabled, ok := svc["enabled"].(bool); ok && !enabled {
			continue
		}
		if mappings != nil && mappings.HasType(chartType) {
			entry := mappings.Charts[chartType]
			if len(entry.Versions) > 0 {
				ver := entry.Versions[0]
				if err := projectSidecars(svc, suseOut, ver, binding); err != nil {
					return res, err
				}
			}
		}
	}

	// Workloads[] pipeline (DO-0005 Phase 1).
	// Read workloads[] from suse values, apply per-stage overrides,
	// resolve shape defaults, resolve env per-workload using shared
	// bindings, collect sidecars per-workload, and output
	// workloads_resolved[] in suseOut.
	secretIdx, secretOverrides := buildBindingSecretIndex(values, mappings)
	disabledBindings := map[string]bool{}
	for _, svcRaw := range svcMaps {
		b, _ := svcRaw["binding"].(string)
		e, _ := svcRaw["enabled"].(bool)
		eStr, _ := svcRaw["enabled"].(string)
		if b != "" && !e && eStr != "true" {
			disabledBindings[b] = true
		}
	}

	// Resolve workloads: parse, validate, apply shape defaults.
	workloads, wErr := resolveWorkloads(suse)
	if wErr != nil {
		return res, fmt.Errorf("suse-library.%w", wErr)
	}

	if len(workloads) > 0 {
		// Per-workload processing: env resolution + sidecars.
		workloadsResolved := make([]any, 0, len(workloads))
		for _, w := range workloads {
			wName, _ := w["name"].(string)

			// Resolve env block for this workload.
			wEnv, _ := w["env"].(map[string]any)
			envResolved, err := resolveWorkloadEnv(wEnv, bindings, secretIdx, secretOverrides, disabledBindings, releaseName)
			if err != nil {
				return res, fmt.Errorf("suse-library.workloads[name=%s].%w", wName, err)
			}
			if len(envResolved) > 0 {
				w["env_resolved"] = envEntriesToValues(envResolved)
			}
			// Remove the raw env block — it's been resolved.
			delete(w, "env")
			// Remove shape — it was used for defaults, not needed in output.
			delete(w, "shape")

			// Collect sidecars for this workload from services[].
			if mappings != nil {
				for _, svcRaw := range servicesRaw {
					svc, ok := svcRaw.(map[string]any)
					if !ok {
						continue
					}
					svcBinding, _ := svc["binding"].(string)
					chartType, _ := svc["type"].(string)
					if svcBinding == "" || chartType == "" {
						continue
					}
					entry, ok := mappings.Charts[chartType]
					if !ok || len(entry.Versions) == 0 {
						continue
					}
					ver := entry.Versions[0]
					// Sidecar target: if inject_target matches workload name,
					// or no target specified (inject into all workloads).
					injectTarget, _ := svc["inject_target"].(string)
					if injectTarget != "" && injectTarget != wName {
						continue
					}
					if err := projectWorkloadSidecars(svc, w, ver, svcBinding); err != nil {
						return res, err
					}
				}
			}

			workloadsResolved = append(workloadsResolved, w)
		}
		suseOut["workloads_resolved"] = workloadsResolved
	}

	// env_injection_path: for brownfield-helm projects, project env_resolved
	// entries at a custom path in the overlay so the wrapped chart picks
	// them up via its own extraEnv/env convention.
	if injPath, ok := suse["env_injection_path"].(string); ok && injPath != "" {
		var allEnvEntries []any
		wResolved, _ := suseOut["workloads_resolved"].([]any)
		for _, wr := range wResolved {
			wMap, ok := wr.(map[string]any)
			if !ok {
				continue
			}
			if envList, ok := wMap["env_resolved"].([]any); ok {
				allEnvEntries = append(allEnvEntries, envList...)
			}
		}
		if len(allEnvEntries) > 0 {
			// Convert env_resolved (secretKeyRef format) to plain env format
			// that most charts understand: [{name: X, value: Y}] or
			// [{name: X, valueFrom: {secretKeyRef: {name: S, key: K}}}]
			var helmEnv []any
			for _, e := range allEnvEntries {
				entry, ok := e.(map[string]any)
				if !ok {
					continue
				}
				name, _ := entry["name"].(string)
				kind, _ := entry["kind"].(string)
				if kind == "secret" {
					secretRef, _ := entry["secretRef"].(string)
					secretKey, _ := entry["secretKey"].(string)
					helmEnv = append(helmEnv, map[string]any{
						"name": name,
						"valueFrom": map[string]any{
							"secretKeyRef": map[string]any{
								"name": secretRef,
								"key":  secretKey,
							},
						},
					})
				} else {
					val, _ := entry["value"].(string)
					helmEnv = append(helmEnv, map[string]any{
						"name":  name,
						"value": val,
					})
				}
			}
			// Write at the injection path in the overlay root
			setNestedValue(res.Overlay, injPath, helmEnv)
		}
	}

	// bootstrap.jobs[] projection: walk services[], collect each entry's
	// bootstrap.jobs into a flat list under suse-library.bootstrap_jobs.
	// Cross-binding refs inside the job's env/command/image are resolved
	// here. The bundle's library chart iterates the flat list and emits
	// one Helm Job per entry with post-install/post-upgrade hooks.
	bootstrapJobs, err := projectBootstrapJobs(values, bindings)
	if err != nil {
		return res, err
	}
	if len(bootstrapJobs) > 0 {
		suseOut["bootstrap_jobs"] = bootstrapJobs
	}

	// Inject chart alias map for multi-instance binding-secret resolution.
	// The library chart's _helpers.tpl reads this to compute the correct
	// host for aliased sub-charts (replaces the chart type in the host
	// template with the alias).
	hasMulti := false
	aliasMap := map[string]any{}
	for binding, alias := range aliases {
		if _, ok := aliases[binding]; ok {
			svcType := ""
			for _, svcRaw := range servicesRaw {
				if svc, ok := svcRaw.(map[string]any); ok {
					if b, _ := svc["binding"].(string); b == binding {
						svcType, _ = svc["type"].(string)
						break
					}
				}
			}
			if alias != svcType {
				aliasMap[binding] = alias
				hasMulti = true
			}
		}
	}
	if hasMulti {
		suseOut["_chart_aliases"] = aliasMap
	}

	projectAppFields(suse, suseOut)

	if len(suseOut) > 0 {
		res.Overlay["suse-library"] = suseOut
	}
	return res, nil
}

// projectAppFields copies resolved app-level fields (after override
// merge + template resolution) into the overlay so Helm sees the
// stage-effective values. Only projects fields that the library chart
// consumes and that may have been modified by overrides or templates.
func projectAppFields(suse, suseOut map[string]any) {
	appFields := []string{
		"replicas", "port", "domain",
		"ingress", "image", "resources", "probes",
		"imagePullSecrets", "podAnnotations", "metrics",
		"securityContext", "podSecurityContext",
		"initContainers", "volumes", "volumeMounts",
		"nodeSelector", "tolerations", "affinity",
	}
	for _, field := range appFields {
		if v, ok := suse[field]; ok {
			suseOut[field] = v
		}
	}
}

// selectVersion picks the versions[] entry matching the service's branch.
// If the service declares a branch and the chart has a branches map with
// a chart_version for that branch, finds the first versions[] entry whose
// semver Constraint matches the branch's chart_version. Falls back to
// Versions[0] when no branch is set, no match is found, or the constraint
// can't be parsed.
func selectVersion(entry dslmapping.ChartEntry, svc map[string]any) dslmapping.VersionEntry {
	if len(entry.Versions) == 0 {
		return dslmapping.VersionEntry{}
	}
	// Filter versions by chart source. Entries with empty Source match
	// all sources (schema-compatible charts). Entries with a specific
	// Source only match when that source is active.
	source := ChartSource()
	filtered := make([]dslmapping.VersionEntry, 0, len(entry.Versions))
	for _, v := range entry.Versions {
		if v.Source == "" || v.Source == source {
			filtered = append(filtered, v)
		}
	}
	if len(filtered) == 0 {
		return entry.Versions[0] // fallback to first entry
	}
	branch, _ := svc["branch"].(string)
	if branch == "" {
		if n, ok := svc["branch"].(int); ok {
			branch = fmt.Sprintf("%d", n)
		}
	}
	if branch == "" {
		branch = entry.DefaultBranch
	}
	if branch != "" {
		if be, ok := entry.Branches[branch]; ok && be.ChartVersion != "" {
			// AppCo versions use "0.3.9-28.1" where the suffix is a
			// build number, not a semver pre-release. Strip it so the
			// constraint matching works on the base version.
			raw := be.ChartVersion
			if idx := strings.Index(raw, "-"); idx > 0 {
				raw = raw[:idx]
			}
			chartVer, err := semver.NewVersion(raw)
			if err == nil {
				for _, v := range filtered {
					c, err := semver.NewConstraint(v.Constraint)
					if err != nil {
						continue
					}
					if c.Check(chartVer) {
						return v
					}
				}
			}
		}
	}
	return filtered[0]
}

// digDSL walks a nested map by a dotted path. Returns the leaf value and true
// when found; nil and false when any intermediate key is missing OR the path
// terminates at a non-scalar (we don't try to project sub-trees as a unit —
// each leaf maps independently per dsl-mappings.yaml). Sub-tree projection
// would require the values_mapping to declare it, which it doesn't today.
func digDSL(m map[string]any, dottedPath string) (any, bool) {
	keys := strings.Split(dottedPath, ".")
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		v, exists := mm[k]
		if !exists {
			return nil, false
		}
		cur = v
	}
	return cur, true
}

// setAtPath writes value at dotted path inside the given map, creating
// intermediate map[string]any as needed.
//
// Path components can include bracket notation `name[N]` to address an
// element of a list. This is needed because some AppCo charts expose
// list-shaped values in their schema (e.g. prometheus's
// server.ingress.hosts is `[]string`, grafana's ingress.hosts is too).
// dsl-mappings.yaml encodes the projection target as
// `prometheus.server.ingress.hosts[0]` and we have to actually create
// (or extend) the list at index 0, not write a literal key `hosts[0]`.
//
// Semantics:
//   - foo.bar.baz                — descend into maps; create missing
//                                  intermediate maps as we go.
//   - foo.bar.baz[N]             — at the final step, the parent must be
//                                  a map; we set `bar[baz]` to a slice
//                                  whose length is at least N+1, and
//                                  write `value` at index N. Existing
//                                  scalar elements at lower indices are
//                                  preserved; unset slots are nil.
//   - foo.bar[N].baz             — descend through a list. `foo[bar]`
//                                  must already be a list (or we create
//                                  one of length N+1, with element N a
//                                  fresh map). We then descend into
//                                  element N as a map.
//
// Returns an error when an existing intermediate value is a scalar
// (would have to overwrite to descend), when a list is shorter than the
// asked index AND the path needs to descend further (vs append at the
// leaf, which is fine), or when the bracket spec is malformed.
func getAtPath(m map[string]any, dottedPath string) any {
	keys := strings.Split(dottedPath, ".")
	cur := m
	for _, k := range keys[:len(keys)-1] {
		seg, _, _, _ := parsePathSegment(k)
		next, ok := cur[seg]
		if !ok {
			return nil
		}
		nm, ok := next.(map[string]any)
		if !ok {
			return nil
		}
		cur = nm
	}
	last, _, _, _ := parsePathSegment(keys[len(keys)-1])
	return cur[last]
}

func setAtPath(m map[string]any, dottedPath string, value any) error {
	keys := strings.Split(dottedPath, ".")
	if len(keys) == 0 {
		return fmt.Errorf("empty path")
	}
	cur := m
	for i, raw := range keys[:len(keys)-1] {
		k, idx, hasIdx, perr := parsePathSegment(raw)
		if perr != nil {
			return fmt.Errorf("path %s: %w", dottedPath, perr)
		}
		if !hasIdx {
			next, exists := cur[k]
			if !exists {
				nextMap := map[string]any{}
				cur[k] = nextMap
				cur = nextMap
				continue
			}
			nm, ok := next.(map[string]any)
			if !ok {
				return fmt.Errorf("path %s: existing value at %s is not a map (got %T) — "+
					"can't descend to set %s",
					dottedPath, strings.Join(keys[:i+1], "."), next, dottedPath)
			}
			cur = nm
			continue
		}
		// Bracket form on a non-leaf segment: descend into list element.
		listVal, exists := cur[k]
		var list []any
		if exists {
			ll, ok := listVal.([]any)
			if !ok {
				return fmt.Errorf("path %s: existing value at %s is not a list (got %T) — "+
					"can't descend to set %s",
					dottedPath, strings.Join(keys[:i+1], "."), listVal, dottedPath)
			}
			list = ll
		}
		// Grow to fit the index, fill missing slots with empty maps so
		// subsequent descent works.
		for len(list) <= idx {
			list = append(list, map[string]any{})
		}
		// Element at idx must be a map (we're descending further).
		elem, ok := list[idx].(map[string]any)
		if !ok {
			return fmt.Errorf("path %s: list element at %s[%d] is not a map (got %T)",
				dottedPath, strings.Join(keys[:i+1], "."), idx, list[idx])
		}
		cur[k] = list
		cur = elem
	}
	// Leaf segment.
	leafRaw := keys[len(keys)-1]
	leafKey, leafIdx, leafHasIdx, perr := parsePathSegment(leafRaw)
	if perr != nil {
		return fmt.Errorf("path %s: %w", dottedPath, perr)
	}
	if !leafHasIdx {
		cur[leafKey] = value
		return nil
	}
	// Bracket form on the leaf: set list[idx] = value, growing the list
	// (with nils as placeholders) to fit the index.
	listVal, exists := cur[leafKey]
	var list []any
	if exists {
		ll, ok := listVal.([]any)
		if !ok {
			return fmt.Errorf("path %s: existing value at %s is not a list (got %T) — "+
				"can't write index [%d]", dottedPath, leafKey, listVal, leafIdx)
		}
		list = ll
	}
	for len(list) <= leafIdx {
		list = append(list, nil)
	}
	list[leafIdx] = value
	cur[leafKey] = list
	return nil
}

// parsePathSegment splits a path component like `hosts[0]` into the
// key (`hosts`) and the index (0). Returns hasIndex=false for plain
// `hosts`. Errors on malformed brackets like `hosts[`, `hosts[]`, or
// non-integer indices.
func parsePathSegment(raw string) (key string, idx int, hasIndex bool, err error) {
	open := strings.Index(raw, "[")
	if open < 0 {
		return raw, 0, false, nil
	}
	if !strings.HasSuffix(raw, "]") {
		return "", 0, false, fmt.Errorf("malformed bracket in path segment %q (missing ])", raw)
	}
	key = raw[:open]
	idxStr := raw[open+1 : len(raw)-1]
	if idxStr == "" {
		return "", 0, false, fmt.Errorf("malformed bracket in path segment %q (empty index)", raw)
	}
	n := 0
	for _, c := range idxStr {
		if c < '0' || c > '9' {
			return "", 0, false, fmt.Errorf("non-numeric index %q in path segment %q", idxStr, raw)
		}
		n = n*10 + int(c-'0')
	}
	return key, n, true, nil
}

// getOrInitMap returns the existing map at key, or initialises a new one and
// stores it. Used to flip <chart>.enabled: true whether or not the chart
// already has other projected fields.
func getOrInitMap(parent map[string]any, key string) map[string]any {
	if existing, ok := parent[key].(map[string]any); ok {
		return existing
	}
	m := map[string]any{}
	parent[key] = m
	return m
}

// deepMerge recursively merges src into dst. Maps are merged key-by-key
// (recursing on map values); scalar / list values in src overwrite dst at
// the same key. dst is mutated in place.
//
// Used by passthrough projection: we want the developer's escape-hatch
// values to fill in alongside the DSL-projected values, not replace the
// chart block wholesale. Last-wins on conflicting scalars matches Helm's
// own --values stacking semantics; collisions on same DSL+passthrough
// paths are caught loud upstream by suse-library.dsl.validatePassthrough,
// so the order of operations here is irrelevant in practice.
func deepMerge(dst, src map[string]any) {
	for k, v := range src {
		if vm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				deepMerge(dm, vm)
				continue
			}
		}
		dst[k] = v
	}
}


// aliasVersionEntry returns a shallow copy of ver with all target paths
// (values_mapping targets, chart_defaults keys, derived_values targets,
// capability targets) transformed to use chartAlias instead of chartType.
func aliasVersionEntry(ver dslmapping.VersionEntry, chartType, chartAlias string) dslmapping.VersionEntry {
	out := ver

	if len(ver.ValuesMapping) > 0 {
		m := make(map[string]string, len(ver.ValuesMapping))
		for k, v := range ver.ValuesMapping {
			m[k] = AliasedPath(v, chartType, chartAlias)
		}
		out.ValuesMapping = m
	}

	if len(ver.ChartDefaults) > 0 {
		m := make(map[string]any, len(ver.ChartDefaults))
		for k, v := range ver.ChartDefaults {
			m[AliasedPath(k, chartType, chartAlias)] = v
		}
		out.ChartDefaults = m
	}

	if len(ver.DerivedValues) > 0 {
		dvs := make([]dslmapping.DerivedValue, len(ver.DerivedValues))
		for i, dv := range ver.DerivedValues {
			dvs[i] = dv
			dvs[i].Target = AliasedPath(dv.Target, chartType, chartAlias)
		}
		out.DerivedValues = dvs
	}

	if len(ver.Capabilities) > 0 {
		caps := make(map[string]dslmapping.CapabilitySpec, len(ver.Capabilities))
		for k, cap := range ver.Capabilities {
			c := cap
			c.Projection.Target = AliasedPath(cap.Projection.Target, chartType, chartAlias)
			caps[k] = c
		}
		out.Capabilities = caps
	}

	return out
}

// isEmpty reports whether v is "no value provided" — used by
// derived_values's skip_if to decide if the user explicitly set the
// field. Treats nil, empty string, false, and 0 as empty. Anything
// else (non-empty string, non-zero number, true, list, map) counts
// as a real value the user wrote, and the derivation skips.
func isEmpty(v any) bool {
	if v == nil {
		return true
	}
	switch x := v.(type) {
	case string:
		return x == ""
	case bool:
		return !x
	case int:
		return x == 0
	case int64:
		return x == 0
	case float64:
		return x == 0
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}
