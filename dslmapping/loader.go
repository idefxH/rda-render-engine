// Package dslmapping reads library-chart/dsl-mappings.yaml from the resolved
// opinion bundle and exposes the per-chart catalog data the CLI needs to
// stay in sync with the bundle's own data-driven helpers.
//
// The bundle's _helpers.tpl reads this same file at Helm template time;
// the CLI mirrors the read so commands like `rda explain` and
// `rda add-service` see the same authoritative list of supported types
// and DSL fields. Without this loader, the CLI would have its own
// hardcoded catalog that drifts every time a chart lands in the bundle.
//
// Schema (matches library-chart/dsl-mappings.yaml v1alpha1):
//
//	apiVersion: rda.suse.com/dsl-mapping/v1alpha1
//	charts:
//	  <name>:
//	    versions:
//	      - constraint: <semver>
//	        service: { host, port, scheme? }
//	        values_mapping: { dsl_path: values_path, ... }
//	        binding_secret:
//	          - { key, literal|template|from_dsl, required?, default?, skip_env? }
//
// The loader does not validate the schema (the bundle's
// `tests/dsl-mappings-schema/check.py` does that). It treats malformed
// entries as best-effort: missing keys yield empty strings, missing
// versions[] yields no fields, etc. Strict validation belongs at the
// bundle-PR boundary, not on every CLI run.
package dslmapping

// Document is the parsed dsl-mappings.yaml.
type Document struct {
	APIVersion string                 `yaml:"apiVersion"`
	Charts     map[string]ChartEntry  `yaml:"charts"`
}

// ChartEntry is one chart's catalogue entry — a list of versions[] each
// targeting a constraint range.
// BranchEntry maps a branch name to its AppCo OCI tag.
type BranchEntry struct {
	ChartVersion string `yaml:"chart_version"`
}

// ChartSourceSpec describes a chart's OCI reference and image
// pull requirements for a given source (appco or community).
type ChartSourceSpec struct {
	ChartRef          string `yaml:"chart_ref"`
	ChartVersion      string `yaml:"chart_version"`
	RequiresPullSecret bool  `yaml:"requires_pull_secret,omitempty"`
}

type ChartEntry struct {
	DefaultBranch string                        `yaml:"default_branch,omitempty"`
	Branches      map[string]BranchEntry        `yaml:"branches,omitempty"`
	Sources       map[string]ChartSourceSpec     `yaml:"sources,omitempty"`
	Versions      []VersionEntry                `yaml:"versions"`
	// InfraOnly marks non-DSL infra deps (e.g. cloudnative-pg operator).
	// Hidden from service catalogue, used only for source switching.
	InfraOnly     bool                          `yaml:"infra_only,omitempty"`
}

// VersionEntry is one constraint-matched mapping block.
type VersionEntry struct {
	Constraint    string               `yaml:"constraint"`
	// Source limits this version entry to a specific chart source
	// ("appco" or "community"). Empty means the entry applies to
	// all sources — used when the chart schema is identical between
	// AppCo and upstream.
	Source        string               `yaml:"source,omitempty"`
	Service       ServiceSpec          `yaml:"service"`
	ValuesMapping map[string]string    `yaml:"values_mapping"`
	BindingSecret []BindingSecretEntry `yaml:"binding_secret"`
	// ChartDefaults are literal values the renderer writes into the
	// overlay AFTER values_mapping. Path syntax matches values_mapping
	// targets (dotted with optional `key[N]` bracket notation). Used
	// for chart-required fields the DSL doesn't surface — typically
	// shape adapters that bridge the unified DSL to a chart's specific
	// schema (e.g. dex's rich `hosts: [{host, paths}]` requires a
	// `paths` field the simple `hosts: [str]` DSL doesn't carry).
	//
	// Applied per service[] entry, in the same scope as values_mapping.
	// Not applied for disabled / non-local-provisioning entries (those
	// short-circuit before projection).
	//
	// Phase G (rda-cli 0.1.49): Bridges shape gaps without dropping the
	// uniform DSL — the dev still writes `services[i].ingress.hosts:
	// [str]`, the projector handles the chart-side shape.
	ChartDefaults map[string]any `yaml:"chart_defaults,omitempty"`

	// DerivedValues are computed projections applied AFTER values_mapping
	// AND chart_defaults. Each entry's `template` is a Go template (text/
	// template package) rendered with these inputs:
	//
	//   .Service           the services[] entry (post-merge of defaults)
	//   .Release.Name      project name (rda new <name>)
	//   .Release.Namespace deploy namespace (project.yaml namespace)
	//   .Binding           the entry's binding name (services[].binding)
	//   .Type              the entry's chart type (services[].type)
	//   .ChartAlias        the resolved alias (multi-instance: <type>-<binding>)
	//   .Domain            suse-library.domain value
	//   .ChartSource       resolved chart source ("appco" or "community").
	//                      Per-service .Service.source wins over project/global.
	//                      Use to branch on image registry:
	//                        {{ if eq .ChartSource "appco" }}dp.apps.rancher.io/...{{ else }}ghcr.io/...{{ end }}
	//
	// Use:
	//   - Compute fields whose value depends on other DSL fields (e.g. dex's
	//     `config.issuer` should derive from `services[].ingress.host` when
	//     ingress is enabled, falling back to the in-cluster service URL).
	//   - Avoid the issuer/ingress mismatch footgun: the user sets ONE field
	//     (ingress.host); the issuer auto-matches.
	//
	// `skip_if` is a dotted path into `services[].`; when the resolved value
	// is non-empty/non-zero, the derived projection is skipped (user override
	// wins). Use empty string to always derive.
	//
	// Applied AFTER values_mapping + chart_defaults so when both write the
	// same target the derived value can be the explicit fallback. The user
	// override flow is: values_mapping projects user-provided value → derived
	// sees skip_if path non-empty → skips. When user didn't set the field,
	// values_mapping doesn't write the target → derived fires.
	DerivedValues []DerivedValue `yaml:"derived_values,omitempty"`

	// BindingFields surface chart-specific computed values as
	// reachable `${binding:NAME.field}` references (Phase 1 #2 of
	// Design Orientation 0001 Appendix A). The legacy BindingFields
	// (host, port, url, username, password, database, type,
	// secret_name + multi-port *_port / *_url accessors) are
	// hardcoded in internal/render/bindingref.go; this map adds
	// chart-author-controlled extras.
	//
	// Use case: dex's `issuer` and `public_url`. Both derive from
	// `services[].ingress.host` and the chart's in-cluster Service
	// URL — exactly the same template the chart-side
	// `derived_values: dex.config.issuer` already renders. Exposing
	// the same value as a binding field lets the workload's
	// `suse-library.env:` block carry `${binding:auth.issuer}` →
	// resolved to the same URL the chart's config.issuer points at.
	//
	// The map key is the field name as the user references it in
	// `${binding:NAME.<field>}` (lowercase, snake_case for compound
	// names: `public_url`, NOT `publicURL`). The `Template` is a Go
	// text/template rendered with the same inputs as DerivedValues
	// (.Service, .Release.Name, .Binding, .Type) by
	// internal/render/bindingref.go at collectBindings time.
	//
	// Bind-time errors (template parse / render failures) currently
	// fall through to empty-string field values rather than failing
	// the whole render — chart-author bugs are caught at unit-test
	// time via the dsl-mappings-schema validator.
	BindingFields map[string]BindingFieldSpec `yaml:"binding_fields,omitempty"`

	// Capabilities declare typed bootstrap-data lists the chart can
	// consume from the user's `services[].bootstrap.<capability>:`
	// block (Phase 2 of Design Orientation 0001). Each entry pairs a
	// `Backend` strategy (file-static / file-initdb / api-job /
	// k8s-resource) with a per-item `Schema` and a `Projection` that
	// describes how to materialise the user's items into the chart's
	// values overlay (file-static / file-initdb) or runtime jobs
	// (api-job).
	//
	// Map key is the namespaced capability name as the user
	// references it in `bootstrap.<key>:` and as the CLI dispatches:
	// `auth.users`, `auth.clients`, `db.schemas`, `db.seeds`,
	// `obs.dashboards`, `storage.buckets`, `messaging.topics`. Phase 2
	// V1 stabilises these seven names; new names need a corresponding
	// rda-docs entry to keep the catalogue discoverable.
	//
	// Why namespaced? `users` collides between dex (OIDC end-users),
	// postgres (login roles), minio (IAM), vault (token holders).
	// `auth.users` vs `db.users` vs `storage.users` vs `secrets.users`
	// disambiguates by intent. Resolved in Design Orientation 0001 D2.
	//
	// Phase 2.1 introduces the data model only; Phase 2.2 adds the
	// render-time projection (file-static + file-initdb backends).
	// Phase 2.3 wires the `rda bootstrap <cap> <action>` CLI dispatch
	// against this schema.
	Capabilities map[string]CapabilitySpec `yaml:"capabilities,omitempty"`

	// Scaffold drives the `rda service add <type> <binding>` UX.
	// Per-DSL-field guidance for what to write (or NOT write) into the
	// project's deploy/values.yaml at scaffold time:
	//
	//   visibility: visible|hidden|omit
	//     visible — emit the field in the project values.yaml with the
	//       declared default + comment. The dev reads + edits.
	//     hidden  — apply the default via chart_defaults (or values_mapping
	//       when projected from another DSL field) so the chart works
	//       without the dev seeing this knob. The dev can still override
	//       via passthrough.
	//     omit    — neither emit nor inject. The chart's own values.yaml
	//       default applies. Use for fields where rda has no opinion.
	//
	//   default — the literal value to write (visible) or inject (hidden).
	//     Strings, numbers, bools, sequences, maps. Templated strings can
	//     use {{.ProjectName}}, {{.Release.Name}}, {{.Release.Namespace}}
	//     — rendered at scaffold time using the project's metadata.
	//
	//   comment — line-comment to attach above the field in values.yaml
	//     (visible only). Plain text; rda renders to YAML as a head-comment.
	//
	// Backwards-compat: when scaffold is absent for a chart, rda-cli falls
	// back to the hardcoded `case "<chart>":` arm in dslDefaultsFor (the
	// pre-0.1.51 behaviour). Migration is incremental — add scaffold one
	// chart at a time, and the hardcoded case becomes dead code that can
	// be removed after the catalog migration is complete.
	//
	// Override hierarchy (planned, NS Phase G+):
	//   bundle dsl-mappings.yaml (this field)
	//     ↓
	//   overlay default_scaffolds.yaml (per-org/per-stage operator overrides)
	//     ↓
	//   project values.yaml (per-project dev edits)
	Scaffold map[string]ScaffoldField `yaml:"scaffold,omitempty"`

	// Dependencies declares sub-charts the consumer chart embeds that
	// should be disabled when the dev provides an external binding.
	// DO-0004 Phase 1: cross-binding dependency wiring.
	//
	// Example: airflow declares dependencies on postgresql and redis.
	// The dev adds separate services[] entries for those and references
	// them in the airflow entry via the DSL field. rda render then:
	//   1. Disables the internal sub-chart (airflow.postgresql.enabled=false)
	//   2. Wires the external connection from the referenced binding
	//
	// The wiring map translates DSL fields from the referenced binding
	// into chart-values paths on the consumer chart.
	Dependencies []DependencySpec `yaml:"dependencies,omitempty"`

	// SidecarTemplate describes a sidecar container to inject into the
	// app pod. Used by services with `inject: sidecar`. DO-0004 Phase 3b.
	SidecarTemplate *SidecarTemplateSpec `yaml:"sidecar_template,omitempty"`

	// CRDProjection declares how this chart type projects CRDs instead
	// of (or in addition to) binding secrets. Used by infrastructure
	// services like API gateways and service meshes. DO-0004 Phase 2.
	CRDProjection *CRDProjectionSpec `yaml:"crd_projection,omitempty"`

	// ProducesBinding controls whether a binding secret is rendered.
	// Default true. Set to false for infrastructure services that only
	// produce CRDs (e.g. shared APISIX gateway).
	ProducesBinding *bool `yaml:"produces_binding,omitempty"`

	// OperatorManaged marks charts whose workloads are created by a
	// CRD operator rather than directly by helm template. Used by the
	// Tilt extension to skip k8s_resource(workload=...) registration
	// (which requires the workload to exist in helm template output)
	// and instead use extra_pod_selectors to discover operator-created
	// pods at runtime. Example: CloudNativePG creates PostgreSQL pods
	// via its Cluster CR — helm template only outputs the CR, not the
	// pods themselves.
	OperatorManaged bool `yaml:"operator_managed,omitempty"`

	// PodSelector is a label selector map for discovering operator-created
	// pods. Used when OperatorManaged=true to pass extra_pod_selectors
	// to k8s_resource in the Tilt extension.
	// Example: {"cnpg.io/cluster": "{{ .Release.Name }}-cnpg"}
	PodSelector map[string]string `yaml:"pod_selector,omitempty"`

	// CRObject is a Tilt object selector for the operator's CR in helm
	// template output. Format: "name:kind" (e.g.
	// "{{ .Release.Name }}-cnpg:cluster"). Used by the Tilt extension
	// to claim the CR object and attach operator-created pods to the
	// same Tilt resource. Kind is case-insensitive in Tilt.
	CRObject string `yaml:"cr_object,omitempty"`

	// DatasourceType identifies what kind of datasource this chart
	// provides (e.g. "prometheus", "loki", "tempo"). Empty when the
	// chart is not a datasource provider. Used by DO-0002 cross-binding
	// datasource wiring.
	DatasourceType string `yaml:"datasource_type,omitempty"`

	// DatasourcesTarget is the overlay path where datasource provisioning
	// config is written (e.g. "grafana.datasources"). Empty when the
	// chart does not consume datasources. Used by DO-0002.
	DatasourcesTarget string `yaml:"datasources_target,omitempty"`
}

// ScaffoldField is per-DSL-field UX guidance for `rda add-service`.
// See VersionEntry.Scaffold for the contract.
type ScaffoldField struct {
	Default    any    `yaml:"default,omitempty"`
	Comment    string `yaml:"comment,omitempty"`
}

// ServiceSpec describes the in-cluster Service shape per chart version.
//
// Two ways to declare ports:
//
//	# legacy single-port shape (postgresql, redis, valkey, ...)
//	service:
//	  host: "{{ .Release.Name }}-postgresql.{{ .Release.Namespace }}.svc.cluster.local"
//	  port: 5432
//	  scheme: tcp
//
//	# multi-port shape (minio, dex, prometheus when expanded with subcharts)
//	service:
//	  host: "{{ .Release.Name }}-minio.{{ .Release.Namespace }}.svc.cluster.local"
//	  ports:
//	    s3:      { port: 9000, scheme: http, primary: true }
//	    console: { port: 9001, scheme: http }
//
// The two shapes are mutually exclusive; declaring both is a bundle
// bug (the schema validator catches it). The loader normalises the
// single-port shape into a one-entry Ports map keyed `default` with
// primary=true, so downstream consumers always see a Ports map.
//
// The `primary` flag selects the port whose name appears in the
// short binding-secret aliases. For minio, that's the S3 API port
// (`<BINDING>_HOST/PORT/URL`); the console port is exposed as
// `<BINDING>_CONSOLE_HOST/PORT/URL`. Exactly one port per chart MUST
// be primary.
type ServiceSpec struct {
	Host  string `yaml:"host"`
	Port  int    `yaml:"port,omitempty"`   // legacy single-port shape; mutually exclusive with Ports
	Ports map[string]ServicePort `yaml:"ports,omitempty"` // multi-port shape
	Scheme string `yaml:"scheme,omitempty"` // legacy single-port scheme
}

// ServicePort is one named port in the multi-port shape.
type ServicePort struct {
	Port    int    `yaml:"port"`
	Scheme  string `yaml:"scheme,omitempty"`
	Primary bool   `yaml:"primary,omitempty"`
}

// PortsResolved returns the canonical Ports map for the service,
// normalising the legacy single-port shape into a one-entry map keyed
// `default` with primary=true. Always returns a non-empty map when
// the chart declares either shape; nil only when neither Port nor
// Ports is set (malformed entry — the schema validator catches it,
// not the loader).
func (s ServiceSpec) PortsResolved() map[string]ServicePort {
	if len(s.Ports) > 0 {
		return s.Ports
	}
	if s.Port == 0 {
		return nil
	}
	scheme := s.Scheme
	if scheme == "" {
		scheme = "http"
	}
	return map[string]ServicePort{
		"default": {Port: s.Port, Scheme: scheme, Primary: true},
	}
}

// PrimaryPort returns the (name, ServicePort) pair for the primary
// port. Returns ("", zero) when no primary is declared. The legacy
// shape always has a primary (synthesised by PortsResolved).
func (s ServiceSpec) PrimaryPort() (string, ServicePort) {
	for name, p := range s.PortsResolved() {
		if p.Primary {
			return name, p
		}
	}
	return "", ServicePort{}
}

type BindingSecretEntry struct {
	Key      string `yaml:"key"`
	Literal  string `yaml:"literal,omitempty"`
	Template string `yaml:"template,omitempty"`
	FromDSL  string `yaml:"from_dsl,omitempty"`
	Required bool   `yaml:"required,omitempty"`
	Default  string `yaml:"default,omitempty"`
	SkipEnv  bool   `yaml:"skip_env,omitempty"`
}

// SupportedTypes returns the chart names in the catalog, sorted alphabetically.
// Empty when the doc is nil (no mappings file in the bundle).
func (d *Document) SupportedTypes() []string {
	if d == nil {
		return nil
	}
	out := make([]string, 0, len(d.Charts))
	for name, entry := range d.Charts {
		if entry.InfraOnly {
			continue
		}
		out = append(out, name)
	}
	// Stable order — callers iterate this for help text, error messages.
	sortStrings(out)
	return out
}

// FieldsForType returns the union of values_mapping keys and from_dsl paths
// from binding_secret across all versions[] entries for the given chart
// type. Useful for `rda explain services.<binding>` listing.
//
// Returns nil if the type isn't catalogued (caller should verify via
// HasType first if it cares about distinguishing "no fields" from
// "unknown type").
func (d *Document) FieldsForType(chartType string) []string {
	if d == nil {
		return nil
	}
	entry, ok := d.Charts[chartType]
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	for _, v := range entry.Versions {
		for k := range v.ValuesMapping {
			seen[k] = true
		}
		for _, bs := range v.BindingSecret {
			if bs.FromDSL != "" {
				seen[bs.FromDSL] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

// IsCommonField reports whether the DSL path appears in the
// values_mapping of MORE than one chart in the catalog. Used by the
// scaffold formatter to section the emitted values.yaml into "common"
// vs "chart-specific" — auto-derived rather than declared, so adding
// a new chart that shares fields with an existing one (e.g. Keycloak
// gaining `issuer` alongside dex) automatically reclassifies the
// field without operator action.
//
// Walks all charts × all versions × values_mapping. Cheap (catalog
// has ~10 charts × 1-2 versions × ~10 fields = ~200 lookups), and
// the per-chart scaffold call only walks once at scaffold time.
//
// Convention: any field that appears in 0 or 1 chart is "chart-
// specific"; ≥2 is "common". A 0-count field can only happen if a
// scaffold table declares a field with no values_mapping target —
// which is a misconfig and should be caught by the bundle's
// dsl-mappings-target-validity test.
func (d *Document) IsCommonField(dslPath string) bool {
	if d == nil {
		return false
	}
	count := 0
	for _, entry := range d.Charts {
		for _, ver := range entry.Versions {
			if _, ok := ver.ValuesMapping[dslPath]; ok {
				count++
				break // count one hit per chart
			}
		}
		if count >= 2 {
			return true
		}
	}
	return false
}

// HasType reports whether the catalog has an entry for chartType.
func (d *Document) HasType(chartType string) bool {
	if d == nil {
		return false
	}
	_, ok := d.Charts[chartType]
	return ok
}

// ValuesPathFor returns the chart-values path that the given DSL field maps
// to, for chartType, or "" when not found. Walks all versions[] entries
// (first match wins — versions[] is ordered most-specific-first by
// convention).
func (d *Document) ValuesPathFor(chartType, dslField string) string {
	if d == nil {
		return ""
	}
	entry, ok := d.Charts[chartType]
	if !ok {
		return ""
	}
	for _, v := range entry.Versions {
		if path, ok := v.ValuesMapping[dslField]; ok {
			return path
		}
	}
	return ""
}

// sortStrings is a tiny stable in-place sort. Avoid importing "sort" for one
// call when the slice is small; explicit so the loader has no extra deps.
func sortStrings(s []string) {
	// Insertion sort: O(n^2) but n is the catalog size (~5-20 charts).
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// DerivedValue is a computed projection: the rendered template's output
// is written to Target unless SkipIf names a non-empty service field.
// See ChartVersion.DerivedValues docstring for the template inputs.
type DerivedValue struct {
	Target   string `yaml:"target"`
	Template string `yaml:"template"`
	SkipIf   string `yaml:"skip_if,omitempty"`
}

// BindingFieldSpec declares a chart-author-defined field exposed via
// `${binding:NAME.<field>}` references at render time. The Template is
// rendered with the same inputs as DerivedValue.Template (.Service,
// .Release.Name, .Binding, .Type). See the docstring on
// VersionEntry.BindingFields for the design rationale.
type BindingFieldSpec struct {
	Template string `yaml:"template"`
}

// CapabilitySpec is one chart-supported bootstrap data capability —
// a typed list of items the user declares under
// `services[].bootstrap.<cap>:` and the renderer materialises
// according to the spec's Backend.
//
// Phase 2.1: data model only. Phase 2.2 wires the render projection
// for the Backend types listed below. Phase 2.3 adds the CLI dispatch.
type CapabilitySpec struct {
	// Backend names the materialisation strategy. One of:
	//   - file-static : project items into a chart values target
	//                   (e.g. dex.config.staticPasswords). Atomic
	//                   with `helm install`. Most common in V1.
	//   - file-initdb : project items into a ConfigMap mounted at
	//                   /docker-entrypoint-initdb.d (postgres,
	//                   mariadb). One-shot at first start.
	//   - api-job     : emit a Helm post-install Job that calls the
	//                   service's API (keycloak-config-cli, mc admin,
	//                   kafka-topics.sh, vault CLI). Idempotent.
	//   - k8s-resource: emit operator CRs (KeycloakUser, KafkaTopic).
	//                   Continuously reconciled. Rare in V1.
	//
	// V1 implementation covers file-static + file-initdb (Phase 2.2).
	// api-job + k8s-resource are reserved values; the loader accepts
	// them but the projector returns ERR_CAPABILITY_BACKEND_UNSUPPORTED
	// at render time, so chart authors can stage their entries before
	// the backend lands.
	Backend BackendType `yaml:"backend"`

	// Order is the per-cap execution order, lower = earlier. Within a
	// chart, capabilities run in ascending Order. Used to encode
	// dependencies like `db.schemas` (order: 1) before `db.seeds`
	// (order: 2) — schemas must exist before seeds insert into them.
	// Defaults to 100 when unset (most caps don't care).
	Order int `yaml:"order,omitempty"`

	// Schema describes the per-item field validation. Map key is the
	// item field name as the user writes it in their values.yaml
	// (lowercase, snake_case for compound names like `redirect_uris`
	// — though Go-templating-friendly camelCase like `redirectURIs`
	// is also accepted; the projector's field_map handles either).
	Schema map[string]FieldSchema `yaml:"schema,omitempty"`

	// Projection describes how items are written to the chart values
	// overlay (file-static / file-initdb) or to a Job manifest
	// (api-job). The shape varies by Backend; see ProjectionSpec.
	Projection ProjectionSpec `yaml:"projection,omitempty"`

	// Job is set only for Backend=api-job. Path to a Helm-template-
	// shaped YAML that the renderer materialises with the items as
	// input. Phase 2 V2.
	Job *JobSpec `yaml:"job,omitempty"`

	// Init declares the default field values for `rda bootstrap <cap> init`.
	// Map keys match Schema field names. Values are template strings
	// with {{project}}, {{ingress.host}}, {{random:hex:N}} placeholders.
	// List-typed fields use a YAML list of template strings.
	// When absent, `init` is not available for this capability.
	Init map[string]any `yaml:"init,omitempty"`

	// WireOffer marks this capability as automatically invokable via
	// `rda service wire <source> --to <target>`. The value names the
	// wire type (e.g. "oidc"). When a developer runs `wire auth --to app`,
	// the CLI looks up capabilities with a matching WireOffer on the
	// source chart and runs the Init template with the target's context.
	// When a chart has multiple capabilities with different WireOffer
	// values, the user disambiguates with `--as <type>`.
	WireOffer string `yaml:"wire_offer,omitempty"`
}

// BackendType is the string-typed enum for CapabilitySpec.Backend.
// Stringly-typed for YAML readability; a Go-side enum would force
// custom YAML unmarshaling for no readability win.
type BackendType string

const (
	// BackendFileStatic projects into rendered chart values.
	// Implementation: Phase 2.2 (file-static).
	BackendFileStatic BackendType = "file-static"
	// BackendFileInitDB projects into a ConfigMap mounted at
	// /docker-entrypoint-initdb.d. Implementation: Phase 2.2.
	BackendFileInitDB BackendType = "file-initdb"
	// BackendAPIJob emits a Helm post-install Job. Implementation:
	// Phase 2 V2.
	BackendAPIJob BackendType = "api-job"
	// BackendK8sResource emits operator CRs. Implementation: Phase 2 V3+.
	BackendK8sResource BackendType = "k8s-resource"
)

// FieldSchema is the per-field validation rule for a CapabilitySpec
// item. Phase 2.1 stays minimal — Type + Required + Secret. Future
// versions may add MinLength, MaxLength, Pattern (regex), Enum
// (allowed values), but these stay out of V1 to avoid CUE-grade
// schema complexity. The validator can always run more checks in
// code once it has the parsed item.
type FieldSchema struct {
	// Type is one of: string, int, bool, list, list[string], map.
	// Empty defaults to "string" (the most common case).
	Type string `yaml:"type,omitempty"`

	// Required: caller (renderer / CLI) errors when the field is
	// absent from the user's item. Defaults to false (optional).
	Required bool `yaml:"required,omitempty"`

	// Secret marks fields whose value is sensitive (passwords,
	// secrets, tokens). Used by:
	//   - the future render-time lint (Design Orientation 0001 D5)
	//     to flag plaintext leaks into `overrides.<non-dev>`.
	//   - the `rda bootstrap <cap> list` CLI to mask the value in
	//     human-mode output.
	Secret bool `yaml:"secret,omitempty"`

	// Default is the value substituted when the user's item omits
	// this field (and Required is false). Empty string when no
	// default applies.
	Default string `yaml:"default,omitempty"`
}

// ProjectionSpec describes how the renderer maps user-written items
// to the chart's values shape (Backend=file-static / file-initdb) or
// to a Job's input (Backend=api-job). The shape that's relevant
// depends on the Backend; the loader accepts the union and the
// projector picks the right fields per backend.
//
// Phase 2.1: data model only — no projection runs yet.
type ProjectionSpec struct {
	// Target is the dotted path in the chart's values overlay where
	// the materialised items land (e.g. "dex.config.staticPasswords"
	// for auth.users on dex, "postgresql.primary.initdb.scripts" for
	// db.schemas). Required for file-static / file-initdb backends.
	Target string `yaml:"target,omitempty"`

	// FieldMap renames user-side field names to chart-native names.
	// Map key is user-side, value is chart-side. Empty means identity
	// (the chart accepts the user's spelling). Example for dex
	// auth.clients: { id: id, secret: secret, redirectURIs: redirectURIs }
	// (no rename needed; dex's chart matches the user's natural names).
	FieldMap map[string]string `yaml:"field_map,omitempty"`

	// Transform names a built-in transformation applied to each item
	// before projection. Limited to a known set to avoid a
	// templating mini-language (Design Orientation 0001 Q1):
	//   - bcrypt-password-to-hash: items with a `password` field get
	//                              that field replaced by a `hash`
	//                              field with bcrypt(password) (cost
	//                              10). Used by dex auth.users.
	//   - inline-or-file:          items with `sql:` keep as-is;
	//                              items with `sqlFile:` read the
	//                              file's contents into `sql:`. Used
	//                              by db.schemas / db.seeds.
	Transform string `yaml:"transform,omitempty"`

	// KeyTemplate (file-initdb only) names the ConfigMap key each
	// item's content is written under. Templated with the item map as
	// input (e.g. "{{ .name }}.sql" for db.schemas). When unset,
	// items are written as a list under Target; when set, items are
	// written as a map keyed by KeyTemplate.
	KeyTemplate string `yaml:"key_template,omitempty"`

	// ValueSource (file-initdb only) names which field carries the
	// content. "sql" for db.schemas / db.seeds (after the
	// inline-or-file transform inlines sqlFile contents). "sql_or_file"
	// is the convenience alias for "let the transform decide".
	ValueSource string `yaml:"value_source,omitempty"`
}

// JobSpec carries the Helm Job template path for Backend=api-job.
// Reserved for Phase 2 V2; the loader accepts the field so chart
// authors can stage their entries.
type JobSpec struct {
	// Template is the path to the Job YAML inside the library-chart
	// (e.g. "charts/keycloak/jobs/seed-users.yaml"). Rendered with
	// the items list as input.
	Template string `yaml:"template,omitempty"`
}

// SidecarTemplateSpec describes a sidecar container to inject.
type SidecarTemplateSpec struct {
	Image     string         `yaml:"image"`
	Resources map[string]any `yaml:"resources,omitempty"`
}

// CRDProjectionSpec declares how a chart type projects CRDs.
// DO-0004 Phase 2.
type CRDProjectionSpec struct {
	// GroupVersion is the CRD's apiVersion (e.g. "apisix.apache.org/v2").
	GroupVersion string `yaml:"group_version"`
	// Kind is the CRD kind (e.g. "ApisixRoute").
	Kind string `yaml:"kind"`
	// RouteTemplate is a Go template rendered per routes[] entry.
	// Template inputs: .Route (the route entry), .Target (resolved
	// service host:port), .Release.Name, .Binding.
	RouteTemplate string `yaml:"route_template,omitempty"`
}

// DependencySpec declares a sub-chart dependency that the consumer
// chart embeds but should be disabled when the dev provides an
// external binding. DO-0004 Phase 1.
type DependencySpec struct {
	// Chart is the primary sub-chart type (e.g. "postgresql").
	// Kept for backwards compatibility with single-type deps.
	Chart string `yaml:"chart,omitempty"`

	// Charts lists all accepted chart types for this dependency.
	// Example: [postgresql, mariadb] means either can satisfy it.
	// When Charts is set, Chart is ignored. When Charts is empty,
	// Chart is used as a single-element list.
	Charts []string `yaml:"charts,omitempty"`

	// Required: when true, rda render fails loud if the dev hasn't
	// provided a binding for this dependency.
	Required bool `yaml:"required"`

	// DSLField is the field name in the consumer's services[] entry
	// where the dev writes the binding name of the dependency
	// (e.g. "metadb", "broker"). The render step reads
	// services[binding=consumer].DSLField to find the referenced binding.
	DSLField string `yaml:"dsl_field"`

	// Wiring maps DSL fields from the referenced binding to chart-values
	// paths on the consumer chart. Key = consumer chart values path,
	// value = source binding DSL field.
	//
	// Special sentinels:
	//   __host__ → resolved service hostname from dsl-mappings service.host
	//   __port__ → resolved primary port from dsl-mappings service.port
	//
	// Example for airflow→postgresql:
	//   "postgresql.auth.username": "auth.user.name"
	//   "postgresql.auth.password": "auth.user.password"
	//   "externalDatabase.host":    "__host__"
	//   "externalDatabase.port":    "__port__"
	Wiring map[string]string `yaml:"wiring,omitempty"`

	// WireType identifies the kind of cross-binding wiring this dependency
	// represents. Used by `rda service wire` to dispatch the correct
	// action (bootstrap client, inject env vars, etc.). Known values:
	//   "oidc" — bootstrap an OIDC client on the provider, wire credentials
	// When empty, the dependency uses standard value-projection wiring
	// (the DO-0004 Phase 1 behavior).
	WireType string `yaml:"wire_type,omitempty"`

	// RedirectURIPath is the callback path registered with the OIDC provider
	// when wire_type is "oidc". The wire command appends this to the consumer's
	// ingress host to build the redirectURIs entry in the Dex client.
	// Default (when empty): "/login/generic_oauth"
	// Example: "/oauth_callback" for MinIO
	RedirectURIPath string `yaml:"redirect_uri_path,omitempty"`

	// CompatibleVersions is a semver constraint for the dependency chart.
	// When set, the opportunity comment shows BLOCKED if the existing
	// binding's chart version is outside this range. Render also fails
	// loud if the dev binds an incompatible version.
	// Example: ">=0.5.0 <1.0.0"
	CompatibleVersions string `yaml:"compatible_versions,omitempty"`
}

// AcceptedTypes returns the list of chart types this dependency accepts.
func (d DependencySpec) AcceptedTypes() []string {
	if len(d.Charts) > 0 {
		return d.Charts
	}
	if d.Chart != "" {
		return []string{d.Chart}
	}
	return nil
}

// AcceptsType reports whether the given chart type satisfies this dependency.
func (d DependencySpec) AcceptsType(chartType string) bool {
	for _, t := range d.AcceptedTypes() {
		if t == chartType {
			return true
		}
	}
	return false
}

