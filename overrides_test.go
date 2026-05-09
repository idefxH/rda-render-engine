package render

import (
	"reflect"
	"testing"
)

func TestApplyStageOverrides_Staging(t *testing.T) {
	values := map[string]any{
		"suse-library": map[string]any{
			"services": []any{
				map[string]any{
					"binding": "db",
					"type":    "postgresql",
					"enabled": true,
					"persistence": map[string]any{
						"enabled": true,
						"size":    "1Gi",
					},
					"overrides": map[string]any{
						"staging": map[string]any{
							"persistence": map[string]any{"size": "50Gi"},
						},
						"prod": map[string]any{
							"persistence": map[string]any{"size": "500Gi", "storageClass": "io1"},
						},
					},
				},
			},
		},
	}
	applyStageOverrides(values, "staging")
	svc := values["suse-library"].(map[string]any)["services"].([]any)[0].(map[string]any)
	persistence := svc["persistence"].(map[string]any)
	if got := persistence["size"]; got != "50Gi" {
		t.Errorf("persistence.size = %v; want 50Gi (override)", got)
	}
	if got := persistence["enabled"]; got != true {
		t.Errorf("persistence.enabled = %v; want true (kept from base)", got)
	}
	if _, present := svc["overrides"]; present {
		t.Errorf("overrides key still present after merge — should be dropped")
	}
}

func TestApplyStageOverrides_Prod(t *testing.T) {
	values := map[string]any{
		"suse-library": map[string]any{
			"services": []any{
				map[string]any{
					"binding": "db",
					"type":    "postgresql",
					"persistence": map[string]any{
						"enabled": true,
						"size":    "1Gi",
					},
					"resources": map[string]any{
						"requests": map[string]any{"cpu": "100m", "memory": "256Mi"},
					},
					"overrides": map[string]any{
						"prod": map[string]any{
							"persistence": map[string]any{"size": "500Gi", "storageClass": "io1"},
							"resources":   map[string]any{"requests": map[string]any{"memory": "4Gi", "cpu": "1"}},
						},
					},
				},
			},
		},
	}
	applyStageOverrides(values, "prod")
	svc := values["suse-library"].(map[string]any)["services"].([]any)[0].(map[string]any)
	persistence := svc["persistence"].(map[string]any)
	if persistence["size"] != "500Gi" || persistence["storageClass"] != "io1" || persistence["enabled"] != true {
		t.Errorf("persistence = %v; want {enabled: true, size: 500Gi, storageClass: io1}", persistence)
	}
	requests := svc["resources"].(map[string]any)["requests"].(map[string]any)
	if requests["cpu"] != "1" || requests["memory"] != "4Gi" {
		t.Errorf("resources.requests = %v; want {cpu: 1, memory: 4Gi}", requests)
	}
}

func TestApplyStageOverrides_StageNotDeclared_DropsKey(t *testing.T) {
	values := map[string]any{
		"suse-library": map[string]any{
			"services": []any{
				map[string]any{
					"binding": "db",
					"type":    "postgresql",
					"persistence": map[string]any{
						"size": "1Gi",
					},
					"overrides": map[string]any{
						"prod": map[string]any{"persistence": map[string]any{"size": "500Gi"}},
					},
				},
			},
		},
	}
	// staging not declared in overrides → no merge, but key is dropped
	applyStageOverrides(values, "staging")
	svc := values["suse-library"].(map[string]any)["services"].([]any)[0].(map[string]any)
	if _, present := svc["overrides"]; present {
		t.Errorf("overrides key should be dropped after merge attempt")
	}
	persistence := svc["persistence"].(map[string]any)
	if persistence["size"] != "1Gi" {
		t.Errorf("persistence.size = %v; want 1Gi (no merge for undeclared stage)", persistence["size"])
	}
}

func TestApplyStageOverrides_NoOverridesKey_NoOp(t *testing.T) {
	values := map[string]any{
		"suse-library": map[string]any{
			"services": []any{
				map[string]any{
					"binding": "db",
					"type":    "postgresql",
					"persistence": map[string]any{
						"size": "1Gi",
					},
				},
			},
		},
	}
	expected := deepCopyMap(values)
	applyStageOverrides(values, "staging")
	if !reflect.DeepEqual(values, expected) {
		t.Errorf("values changed despite no overrides key; got %v want %v", values, expected)
	}
}

func TestApplyStageOverrides_DeepMergeNotReplacement(t *testing.T) {
	// Verify deep-merge: a partial override of a nested map shouldn't
	// wipe sibling keys.
	values := map[string]any{
		"suse-library": map[string]any{
			"services": []any{
				map[string]any{
					"binding": "db",
					"auth": map[string]any{
						"user": map[string]any{"name": "app", "password": "pw"},
					},
					"overrides": map[string]any{
						"staging": map[string]any{
							"auth": map[string]any{"user": map[string]any{"password": "stagingpw"}},
						},
					},
				},
			},
		},
	}
	applyStageOverrides(values, "staging")
	svc := values["suse-library"].(map[string]any)["services"].([]any)[0].(map[string]any)
	user := svc["auth"].(map[string]any)["user"].(map[string]any)
	if user["name"] != "app" {
		t.Errorf("user.name = %v; want app (preserved by deep merge)", user["name"])
	}
	if user["password"] != "stagingpw" {
		t.Errorf("user.password = %v; want stagingpw (overridden)", user["password"])
	}
}

func TestProject_StagedOverrides_AffectsProjection(t *testing.T) {
	// Verify ProjectWithStage threads the override through to the
	// projected overlay. Use auth.user.password since it has a
	// values_mapping in the test fixture (crossBindingMappings).
	values := map[string]any{
		"suse-library": map[string]any{
			"services": []any{
				map[string]any{
					"binding": "db",
					"type":    "postgresql",
					"enabled": true,
					"auth": map[string]any{
						"user": map[string]any{"name": "app", "password": "dev-pw", "database": "appdb"},
					},
					"overrides": map[string]any{
						"prod": map[string]any{
							"auth": map[string]any{
								"user": map[string]any{"password": "prod-pw"},
							},
						},
					},
				},
			},
		},
	}
	res, err := ProjectWithStage(values, crossBindingMappings(), "myrelease", "prod")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pg := res.Overlay["suse-library"].(map[string]any)["postgresql"].(map[string]any)
	auth := pg["auth"].(map[string]any)
	if auth["password"] != "prod-pw" {
		t.Errorf("postgresql.auth.password = %v; want prod-pw (prod override won)", auth["password"])
	}
	if auth["username"] != "app" {
		t.Errorf("postgresql.auth.username = %v; want app (preserved by deep merge)", auth["username"])
	}
	if auth["database"] != "appdb" {
		t.Errorf("postgresql.auth.database = %v; want appdb (preserved)", auth["database"])
	}
}

// deepCopyMap is a small helper for tests that need an independent
// copy of values to compare against.
func deepCopyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		switch t := v.(type) {
		case map[string]any:
			out[k] = deepCopyMap(t)
		case []any:
			out[k] = deepCopySlice(t)
		default:
			out[k] = v
		}
	}
	return out
}

func deepCopySlice(s []any) []any {
	out := make([]any, len(s))
	for i, v := range s {
		switch t := v.(type) {
		case map[string]any:
			out[i] = deepCopyMap(t)
		case []any:
			out[i] = deepCopySlice(t)
		default:
			out[i] = v
		}
	}
	return out
}
