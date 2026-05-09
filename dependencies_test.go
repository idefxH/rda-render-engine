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
