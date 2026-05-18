package render

import (
	"testing"

	"github.com/idefxH/rda-render-engine/dslmapping"
)

func TestProjectDependencies_DisablesInternalSubchart(t *testing.T) {
	ver := dslmapping.VersionEntry{
		Dependencies: []dslmapping.DependencySpec{
			{
				Chart:    "postgresql",
				Required: true,
				DSLField: "metadb",
				Wiring: map[string]string{
					"airflow.postgresql.auth.username": "auth.user.name",
					"airflow.externalDatabase.host":    "__host__",
				},
			},
		},
	}

	svc := map[string]any{
		"binding":  "orchestrator",
		"type":     "airflow",
		"enabled":  true,
		"metadb":   "db",
	}

	refSvc := map[string]any{
		"binding": "db",
		"type":    "postgresql",
		"enabled": true,
		"auth": map[string]any{
			"user": map[string]any{
				"name": "airflow-user",
			},
		},
	}

	allServices := []any{svc, refSvc}
	bindings := map[string]*BindingFields{
		"db": {Host: "app-db-postgresql.ns.svc.cluster.local", Port: "5432"},
	}
	suseOut := map[string]any{}

	err := projectDependencies(svc, suseOut, ver, "orchestrator", "airflow", "airflow", bindings, allServices, nil, "app")
	if err != nil {
		t.Fatalf("projectDependencies failed: %v", err)
	}

	// Check internal sub-chart disabled
	airflow, ok := suseOut["airflow"].(map[string]any)
	if !ok {
		t.Fatal("expected airflow block in overlay")
	}
	pg, ok := airflow["postgresql"].(map[string]any)
	if !ok {
		t.Fatal("expected airflow.postgresql block")
	}
	if pg["enabled"] != false {
		t.Fatalf("expected airflow.postgresql.enabled=false, got %v", pg["enabled"])
	}

	// Check wiring
	extDB, _ := digPath(suseOut, "airflow.externalDatabase.host")
	if extDB != "app-db-postgresql.ns.svc.cluster.local" {
		t.Fatalf("expected host wiring, got %v", extDB)
	}
}

func TestProjectDependencies_MissingRequiredFails(t *testing.T) {
	ver := dslmapping.VersionEntry{
		Dependencies: []dslmapping.DependencySpec{
			{Chart: "postgresql", Required: true, DSLField: "metadb"},
		},
	}
	svc := map[string]any{
		"binding": "orchestrator",
		"type":    "airflow",
		"enabled": true,
	}
	allServices := []any{svc}
	bindings := map[string]*BindingFields{}
	suseOut := map[string]any{}

	err := projectDependencies(svc, suseOut, ver, "orchestrator", "airflow", "airflow", bindings, allServices, nil, "app")
	if err == nil {
		t.Fatal("expected error for missing required dependency")
	}
}

func TestProjectDependencies_WrongTypeFails(t *testing.T) {
	ver := dslmapping.VersionEntry{
		Dependencies: []dslmapping.DependencySpec{
			{Chart: "postgresql", Required: true, DSLField: "metadb"},
		},
	}
	svc := map[string]any{
		"binding": "orchestrator",
		"type":    "airflow",
		"enabled": true,
		"metadb":  "cache",
	}
	refSvc := map[string]any{
		"binding": "cache",
		"type":    "redis",
		"enabled": true,
	}
	allServices := []any{svc, refSvc}
	bindings := map[string]*BindingFields{}
	suseOut := map[string]any{}

	err := projectDependencies(svc, suseOut, ver, "orchestrator", "airflow", "airflow", bindings, allServices, nil, "app")
	if err == nil {
		t.Fatal("expected error for wrong dependency type")
	}
}

func TestProjectDependencies_DisabledRefFails(t *testing.T) {
	ver := dslmapping.VersionEntry{
		Dependencies: []dslmapping.DependencySpec{
			{Chart: "postgresql", Required: true, DSLField: "metadb"},
		},
	}
	svc := map[string]any{
		"binding": "orchestrator",
		"type":    "airflow",
		"enabled": true,
		"metadb":  "db",
	}
	refSvc := map[string]any{
		"binding": "db",
		"type":    "postgresql",
		"enabled": false,
	}
	allServices := []any{svc, refSvc}
	bindings := map[string]*BindingFields{}
	suseOut := map[string]any{}

	err := projectDependencies(svc, suseOut, ver, "orchestrator", "airflow", "airflow", bindings, allServices, nil, "app")
	if err == nil {
		t.Fatal("expected error for disabled dependency")
	}
}

func TestDependencyHints(t *testing.T) {
	doc := &dslmapping.Document{
		Charts: map[string]dslmapping.ChartEntry{
			"airflow": {
				Versions: []dslmapping.VersionEntry{{
					Dependencies: []dslmapping.DependencySpec{
						{Chart: "postgresql", Required: true, DSLField: "metadb"},
						{Chart: "redis", Required: true, DSLField: "broker"},
					},
				}},
			},
		},
	}

	hints := DependencyHints("airflow", doc)
	if len(hints) != 2 {
		t.Fatalf("expected 2 hints, got %d", len(hints))
	}
}

// digPath navigates a nested map by dotted path. Test helper.
func digPath(m map[string]any, path string) (any, bool) {
	return digDSL(m, path)
}

// TestProjectDependencies_EnvInject_SecretRef covers the dex+postgres
// state_db case when the source binding has provisioning=connect with
// credentials.secretRef. The wiring should:
//   - Emit secretKeyRef env entries on dex.env referencing the external
//     Secret directly (so dex resolves credentials at pod start, not at
//     render time via kubectl).
//   - Substitute $RDA_DEP_<FIELD>_<KEY> at each wiring target path so
//     dex's config-file env expansion picks up the real values.
//
// Reproduces the bug where dex stayed wired to the in-cluster default
// when kubectl failed to read shared-pg silently at render time.
func TestProjectDependencies_EnvInject_SecretRef(t *testing.T) {
	ver := dslmapping.VersionEntry{
		Dependencies: []dslmapping.DependencySpec{
			{
				Charts:    []string{"postgresql"},
				Required:  false,
				DSLField:  "state_db",
				EnvInject: "dex.env",
				Wiring: map[string]string{
					"dex.config.storage.type":            "__literal:postgres",
					"dex.config.storage.config.host":     "__host_short__",
					"dex.config.storage.config.port":     "__port__",
					"dex.config.storage.config.user":     "auth.user.name",
					"dex.config.storage.config.password": "auth.user.password",
					"dex.config.storage.config.database": "auth.user.database",
				},
			},
		},
	}

	svc := map[string]any{
		"binding":  "auth",
		"type":     "dex",
		"enabled":  true,
		"state_db": "db",
	}
	refSvc := map[string]any{
		"binding":      "db",
		"type":         "postgresql",
		"enabled":      true,
		"provisioning": "connect",
		"credentials": map[string]any{
			"secretRef": "shared-pg",
		},
	}
	allServices := []any{svc, refSvc}
	// bf intentionally has the in-cluster default Host — simulating the
	// silent-fallback case where kubectl couldn't read shared-pg at
	// render time. The fix must NOT read this; it must produce env refs
	// pointing directly at shared-pg.
	bindings := map[string]*BindingFields{
		"db": {Host: "app-db-postgresql.ns.svc.cluster.local", Port: "5432"},
	}
	suseOut := map[string]any{}

	if err := projectDependencies(svc, suseOut, ver, "auth", "dex", "dex", bindings, allServices, nil, "app"); err != nil {
		t.Fatalf("projectDependencies failed: %v", err)
	}

	// __literal: always bakes — the storage type is not a secret value.
	if v, _ := digPath(suseOut, "dex.config.storage.type"); v != "postgres" {
		t.Errorf("storage.type should be literal 'postgres', got %v", v)
	}

	// Every fetchable wiring target should land as $RDA_DEP_STATE_DB_<KEY>
	// — NOT the in-cluster fallback values.
	// Exception: __port__ bypasses env_inject (charts like Dex require
	// an integer, not a $VAR string) and is resolved at render time.
	wantString := map[string]string{
		"dex.config.storage.config.host":     "$RDA_DEP_STATE_DB_HOST",
		"dex.config.storage.config.user":     "$RDA_DEP_STATE_DB_USERNAME",
		"dex.config.storage.config.password": "$RDA_DEP_STATE_DB_PASSWORD",
		"dex.config.storage.config.database": "$RDA_DEP_STATE_DB_DATABASE",
	}
	for path, expect := range wantString {
		got, _ := digPath(suseOut, path)
		if got != expect {
			t.Errorf("%s = %v; want %q", path, got, expect)
		}
	}
	// Port is resolved as integer at render time, not via env_inject.
	portRaw, _ := digPath(suseOut, "dex.config.storage.config.port")
	if portRaw != 5432 {
		t.Errorf("dex.config.storage.config.port = %v (%T); want integer 5432", portRaw, portRaw)
	}

	// dex.env list must reference the EXTERNAL secret name (shared-pg),
	// not the auto-generated `<release>-<binding>-binding` name.
	// Port is excluded (resolved as integer, not via secretKeyRef).
	envRaw, _ := digPath(suseOut, "dex.env")
	envList, ok := envRaw.([]any)
	if !ok {
		t.Fatalf("dex.env not a list: %T", envRaw)
	}
	if len(envList) != 4 {
		t.Fatalf("expected 4 dex.env entries (host, username, password, database), got %d: %v",
			len(envList), envList)
	}
	for _, raw := range envList {
		e := raw.(map[string]any)
		vf, _ := e["valueFrom"].(map[string]any)
		skr, _ := vf["secretKeyRef"].(map[string]any)
		if skr["name"] != "shared-pg" {
			t.Errorf("env entry %v secretKeyRef.name = %v; want shared-pg",
				e["name"], skr["name"])
		}
	}
}

// TestProjectDependencies_EnvInject_DeployMode confirms env_inject also
// works for provisioning=deploy: the wiring should reference the
// auto-generated binding-secret (`<release>-<binding>-binding`). This
// keeps deploy/connect symmetric and removes the password literal from
// helm values.
func TestProjectDependencies_EnvInject_DeployMode(t *testing.T) {
	ver := dslmapping.VersionEntry{
		Dependencies: []dslmapping.DependencySpec{
			{
				Charts:    []string{"postgresql"},
				DSLField:  "state_db",
				EnvInject: "dex.env",
				Wiring: map[string]string{
					"dex.config.storage.config.host":     "__host_short__",
					"dex.config.storage.config.password": "auth.user.password",
				},
			},
		},
	}
	svc := map[string]any{
		"binding":  "auth",
		"type":     "dex",
		"enabled":  true,
		"state_db": "db",
	}
	refSvc := map[string]any{
		"binding": "db",
		"type":    "postgresql",
		"enabled": true,
		// no provisioning → defaults to deploy
		"auth": map[string]any{"user": map[string]any{"password": "s3cret"}},
	}
	allServices := []any{svc, refSvc}
	bindings := map[string]*BindingFields{
		"db": {Host: "app-db-postgresql.ns.svc.cluster.local", Port: "5432"},
	}
	suseOut := map[string]any{}

	if err := projectDependencies(svc, suseOut, ver, "auth", "dex", "dex", bindings, allServices, nil, "app"); err != nil {
		t.Fatalf("projectDependencies failed: %v", err)
	}

	if got, _ := digPath(suseOut, "dex.config.storage.config.host"); got != "$RDA_DEP_STATE_DB_HOST" {
		t.Errorf("expected $RDA_DEP_STATE_DB_HOST, got %v", got)
	}
	if got, _ := digPath(suseOut, "dex.config.storage.config.password"); got != "$RDA_DEP_STATE_DB_PASSWORD" {
		t.Errorf("expected $RDA_DEP_STATE_DB_PASSWORD, got %v", got)
	}

	envList, _ := digPath(suseOut, "dex.env")
	entries, ok := envList.([]any)
	if !ok || len(entries) != 2 {
		t.Fatalf("expected 2 dex.env entries, got %v", envList)
	}
	for _, raw := range entries {
		e := raw.(map[string]any)
		vf := e["valueFrom"].(map[string]any)
		skr := vf["secretKeyRef"].(map[string]any)
		if skr["name"] != "app-db-binding" {
			t.Errorf("expected secretKeyRef.name=app-db-binding, got %v", skr["name"])
		}
	}
}

// TestProjectDependencies_EnvInject_AliasedConsumer confirms env_inject
// honors multi-instance chart aliases — a second dex (binding=auth2,
// aliased to dex-auth2) gets its env list at the aliased path, not the
// chart-type path.
func TestProjectDependencies_EnvInject_AliasedConsumer(t *testing.T) {
	ver := dslmapping.VersionEntry{
		Dependencies: []dslmapping.DependencySpec{
			{
				Charts:    []string{"postgresql"},
				DSLField:  "state_db",
				EnvInject: "dex.env",
				Wiring: map[string]string{
					"dex.config.storage.config.host": "__host_short__",
				},
			},
		},
	}
	svc := map[string]any{
		"binding": "auth2", "type": "dex", "enabled": true, "state_db": "db",
	}
	refSvc := map[string]any{
		"binding": "db", "type": "postgresql", "enabled": true,
		"provisioning": "connect",
		"credentials":  map[string]any{"secretRef": "shared-pg"},
	}
	bindings := map[string]*BindingFields{
		"db": {Host: "app-db-postgresql.ns.svc.cluster.local", Port: "5432"},
	}
	suseOut := map[string]any{}

	if err := projectDependencies(svc, suseOut, ver, "auth2", "dex-auth2", "dex", bindings, []any{svc, refSvc}, nil, "app"); err != nil {
		t.Fatalf("projectDependencies failed: %v", err)
	}

	// Target path rewritten to dex-auth2.*; env list under dex-auth2.env.
	if got, _ := digPath(suseOut, "dex-auth2.config.storage.config.host"); got != "$RDA_DEP_STATE_DB_HOST" {
		t.Errorf("aliased target wrong: %v", got)
	}
	if got, _ := digPath(suseOut, "dex-auth2.env"); got == nil {
		t.Errorf("expected dex-auth2.env list, got nil")
	}
	if got, _ := digPath(suseOut, "dex.env"); got != nil {
		t.Errorf("expected no dex.env (aliased), got %v", got)
	}
}

// TestProjectDependencies_EnvInject_InlineConnect_FallsBack confirms
// that inline credentials (provisioning=connect + credentials.host/port
// but no secretRef) keep the existing render-time-literal behavior —
// there's no Secret to reference, so env_inject must not produce
// dangling $VAR refs.
func TestProjectDependencies_EnvInject_InlineConnect_FallsBack(t *testing.T) {
	ver := dslmapping.VersionEntry{
		Dependencies: []dslmapping.DependencySpec{
			{
				Charts:    []string{"postgresql"},
				DSLField:  "state_db",
				EnvInject: "dex.env",
				Wiring: map[string]string{
					"dex.config.storage.config.host": "__host_short__",
				},
			},
		},
	}
	svc := map[string]any{
		"binding": "auth", "type": "dex", "enabled": true, "state_db": "db",
	}
	refSvc := map[string]any{
		"binding": "db", "type": "postgresql", "enabled": true,
		"provisioning": "connect",
		"credentials":  map[string]any{"host": "pg.external", "port": "5432"},
	}
	bindings := map[string]*BindingFields{
		"db": {Host: "pg.external", Port: "5432"},
	}
	suseOut := map[string]any{}
	if err := projectDependencies(svc, suseOut, ver, "auth", "dex", "dex", bindings, []any{svc, refSvc}, nil, "app"); err != nil {
		t.Fatalf("projectDependencies failed: %v", err)
	}
	if got, _ := digPath(suseOut, "dex.config.storage.config.host"); got != "pg.external" {
		t.Errorf("inline connect should bake literal host, got %v", got)
	}
	if got, _ := digPath(suseOut, "dex.env"); got != nil {
		t.Errorf("inline connect should not emit env_inject entries, got %v", got)
	}
}
