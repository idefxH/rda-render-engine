package render

import (
	"testing"

	"github.com/idefxH/rda-render-engine/dslmapping"
)

// TestPassthroughWinsOverWiring verifies that when a service has a
// passthrough value AND a dependency wiring targeting the same path,
// the passthrough value wins (user escape hatch).
func TestPassthroughWinsOverWiring(t *testing.T) {
	values := map[string]any{
		"suse-library": map[string]any{
			"domain": "localtest.me",
			"services": []any{
				map[string]any{
					"binding": "auth",
					"type":    "dex",
					"enabled": true,
					"ingress": map[string]any{
						"enabled": true,
						"host":    "auth.localtest.me",
					},
				},
				map[string]any{
					"binding":       "proxy",
					"type":          "oauth2-proxy",
					"enabled":       true,
					"oidc_provider": "auth",
					"passthrough": map[string]any{
						"extraArgs": map[string]any{
							"oidc-issuer-url": "http://my-dex:5556",
						},
					},
				},
			},
		},
	}

	mappings := &dslmapping.Document{
		Charts: map[string]dslmapping.ChartEntry{
			"dex": {
				Versions: []dslmapping.VersionEntry{{
					Constraint: ">=0.1.0",
					Service:    dslmapping.ServiceSpec{Host: "{{ .Release.Name }}-dex", Port: 5556},
				}},
			},
			"oauth2-proxy": {
				Versions: []dslmapping.VersionEntry{{
					Constraint: ">=10.0.0",
					Service:    dslmapping.ServiceSpec{Host: "{{ .Release.Name }}-oauth2-proxy", Port: 80},
					Dependencies: []dslmapping.DependencySpec{{
						Charts:   []string{"dex"},
						DSLField: "oidc_provider",
						WireType: "oidc",
						Wiring: map[string]string{
							"oauth2-proxy.extraArgs.oidc-issuer-url": "__binding:issuer__",
							"oauth2-proxy.extraArgs.provider":        "__literal:oidc",
						},
					}},
				}},
			},
		},
	}

	result, err := ProjectWithStage(values, mappings, "test", "dev")
	if err != nil {
		t.Fatalf("ProjectWithStage failed: %v", err)
	}

	sl, _ := result.Overlay["suse-library"].(map[string]any)
	if sl == nil {
		t.Fatal("no suse-library in overlay")
	}
	o2p, _ := sl["oauth2-proxy"].(map[string]any)
	if o2p == nil {
		t.Fatal("no oauth2-proxy in overlay")
	}
	ea, _ := o2p["extraArgs"].(map[string]any)
	if ea == nil {
		t.Fatal("no extraArgs in overlay")
	}

	issuerURL, _ := ea["oidc-issuer-url"].(string)
	if issuerURL != "http://my-dex:5556" {
		t.Errorf("passthrough should win: expected http://my-dex:5556, got %q", issuerURL)
	}

	provider, _ := ea["provider"].(string)
	if provider != "oidc" {
		t.Errorf("wiring should set uncontested paths: expected oidc, got %q", provider)
	}
}

// TestStageOverridesMergeIntoPassthrough verifies that
// overrides.dev.passthrough.extraArgs.X is merged into
// passthrough.extraArgs.X when stage=dev.
func TestStageOverridesMergeIntoPassthrough(t *testing.T) {
	values := map[string]any{
		"suse-library": map[string]any{
			"domain": "localtest.me",
			"services": []any{
				map[string]any{
					"binding": "proxy",
					"type":    "oauth2-proxy",
					"enabled": true,
					"overrides": map[string]any{
						"dev": map[string]any{
							"passthrough": map[string]any{
								"extraArgs": map[string]any{
									"skip-oidc-discovery": "true",
								},
							},
						},
					},
				},
			},
		},
	}

	mappings := &dslmapping.Document{
		Charts: map[string]dslmapping.ChartEntry{
			"oauth2-proxy": {
				Versions: []dslmapping.VersionEntry{{
					Constraint: ">=10.0.0",
					Service:    dslmapping.ServiceSpec{Host: "{{ .Release.Name }}-oauth2-proxy", Port: 80},
				}},
			},
		},
	}

	result, err := ProjectWithStage(values, mappings, "test", "dev")
	if err != nil {
		t.Fatalf("ProjectWithStage failed: %v", err)
	}

	sl, _ := result.Overlay["suse-library"].(map[string]any)
	o2p, _ := sl["oauth2-proxy"].(map[string]any)
	ea, _ := o2p["extraArgs"].(map[string]any)

	if ea == nil {
		t.Fatal("extraArgs not in overlay — stage override passthrough not merged")
	}
	if ea["skip-oidc-discovery"] != "true" {
		t.Errorf("expected skip-oidc-discovery=true from dev override, got %v", ea["skip-oidc-discovery"])
	}
}

// TestDerivedValuesHaveDomain verifies that .Domain is available
// in derived_values templates.
func TestDerivedValuesHaveDomain(t *testing.T) {
	values := map[string]any{
		"suse-library": map[string]any{
			"domain": "example.com",
			"services": []any{
				map[string]any{
					"binding": "proxy",
					"type":    "oauth2-proxy",
					"enabled": true,
					"ingress": map[string]any{
						"hosts": []any{"proxy.example.com"},
					},
				},
			},
		},
	}

	mappings := &dslmapping.Document{
		Charts: map[string]dslmapping.ChartEntry{
			"oauth2-proxy": {
				Versions: []dslmapping.VersionEntry{{
					Constraint: ">=10.0.0",
					Service:    dslmapping.ServiceSpec{Host: "{{ .Release.Name }}-oauth2-proxy", Port: 80},
					DerivedValues: []dslmapping.DerivedValue{
						{Target: "oauth2-proxy.extraArgs.cookie-domain", Template: ".{{ .Domain }}"},
					},
				}},
			},
		},
	}

	result, err := ProjectWithStage(values, mappings, "test", "")
	if err != nil {
		t.Fatalf("ProjectWithStage failed: %v", err)
	}

	sl, _ := result.Overlay["suse-library"].(map[string]any)
	o2p, _ := sl["oauth2-proxy"].(map[string]any)
	ea, _ := o2p["extraArgs"].(map[string]any)

	if ea == nil || ea["cookie-domain"] != ".example.com" {
		t.Errorf("expected .Domain=example.com → cookie-domain=.example.com, got %v", ea)
	}
}

// TestConnectionURLComposed verifies that postgresql bindings
// auto-compose connection_url from individual fields.
func TestConnectionURLComposed(t *testing.T) {
	values := map[string]any{
		"suse-library": map[string]any{
			"services": []any{
				map[string]any{
					"binding": "db",
					"type":    "postgresql",
					"enabled": true,
					"auth": map[string]any{
						"user": map[string]any{
							"name":     "myuser",
							"password": "mypass",
							"database": "mydb",
						},
					},
				},
			},
		},
	}

	mappings := &dslmapping.Document{
		Charts: map[string]dslmapping.ChartEntry{
			"postgresql": {
				Versions: []dslmapping.VersionEntry{{
					Constraint: ">=0.1.0",
					Service:    dslmapping.ServiceSpec{Host: "{{ .Release.Name }}-postgresql", Port: 5432},
				}},
			},
		},
	}

	bindings := collectBindings(values, mappings, "test")
	db := bindings["db"]
	if db == nil {
		t.Fatal("no db binding")
	}

	connURL, ok := db.Secret["connection_url"]
	if !ok {
		t.Fatal("connection_url not in Secret map")
	}
	expected := "postgresql://myuser:mypass@test-postgresql:5432/mydb"
	if connURL != expected {
		t.Errorf("expected %q, got %q", expected, connURL)
	}
}
