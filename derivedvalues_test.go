package render

import (
	"testing"

	"github.com/idefxH/rda-render-engine/dslmapping"
)

// Test the derived_values projection: it should derive a chart value from
// the service entry's other fields when the user hasn't explicitly set the
// override field. Three cases:
//   1. SkipIf field empty + ingress enabled → derive from ingress.host
//   2. SkipIf field empty + no ingress       → in-cluster URL fallback
//   3. SkipIf field set                      → user override wins, no derivation
//
// Real-world use: dex's `config.issuer` should equal the URL the browser
// hits (the Ingress URL when enabled, the in-cluster Service URL when not).
// User typing both `ingress.host` and `issuer` and mismatching them is the
// footgun this feature eliminates.
func TestProject_DerivedValues_DexIssuer(t *testing.T) {
	// One mappings doc, three test cases. The values_mapping projects the
	// user-set `issuer` (when present) to dex.config.issuer; the
	// derived_values entry FILLS dex.config.issuer when issuer is empty.
	dexMappings := func() *dslmapping.Document {
		return &dslmapping.Document{
			APIVersion: "rda.suse.com/dsl-mapping/v1alpha1",
			Charts: map[string]dslmapping.ChartEntry{
				"dex": {
					Versions: []dslmapping.VersionEntry{{
						Constraint: ">=0.24.0",
						Service:    dslmapping.ServiceSpec{Host: "{{ .Release.Name }}-dex"},
						ValuesMapping: map[string]string{
							"issuer": "dex.config.issuer",
						},
						DerivedValues: []dslmapping.DerivedValue{{
							Target: "dex.config.issuer",
							SkipIf: "issuer",
							Template: `{{- if .Service.ingress.enabled -}}` +
								`http://{{ .Service.ingress.host }}` +
								`{{- else -}}` +
								`http://{{ .Release.Name }}-dex:5556` +
								`{{- end -}}`,
						}},
					}},
				},
			},
		}
	}

	getIssuer := func(t *testing.T, res Result) string {
		t.Helper()
		sl, ok := res.Overlay["suse-library"].(map[string]any)
		if !ok {
			t.Fatalf("Overlay missing suse-library")
		}
		dex, ok := sl["dex"].(map[string]any)
		if !ok {
			t.Fatalf("Overlay missing dex block: %#v", sl)
		}
		cfg, ok := dex["config"].(map[string]any)
		if !ok {
			t.Fatalf("Overlay missing dex.config block: %#v", dex)
		}
		s, _ := cfg["issuer"].(string)
		return s
	}

	t.Run("ingress enabled → derived from host", func(t *testing.T) {
		values := valuesWith(map[string]any{
			"binding": "auth",
			"type":    "dex",
			"ingress": map[string]any{
				"enabled": true,
				"host":    "auth.app.localtest.me",
			},
		})
		res, err := Project(values, dexMappings(), "app")
		if err != nil {
			t.Fatal(err)
		}
		got := getIssuer(t, res)
		want := "http://auth.app.localtest.me"
		if got != want {
			t.Fatalf("issuer derived from ingress: want %q, got %q", want, got)
		}
	})

	t.Run("ingress disabled → in-cluster fallback", func(t *testing.T) {
		values := valuesWith(map[string]any{
			"binding": "auth",
			"type":    "dex",
			"ingress": map[string]any{
				"enabled": false,
			},
		})
		res, err := Project(values, dexMappings(), "app")
		if err != nil {
			t.Fatal(err)
		}
		got := getIssuer(t, res)
		want := "http://app-dex:5556"
		if got != want {
			t.Fatalf("issuer in-cluster fallback: want %q, got %q", want, got)
		}
	})

	t.Run("explicit issuer override → derived skipped", func(t *testing.T) {
		// User explicitly sets services[].issuer. values_mapping projects
		// it. derived_values's skip_if=issuer sees non-empty → no override.
		values := valuesWith(map[string]any{
			"binding": "auth",
			"type":    "dex",
			"issuer":  "https://sso.example.com/dex",
			"ingress": map[string]any{
				"enabled": true,
				"host":    "auth.app.localtest.me",
			},
		})
		res, err := Project(values, dexMappings(), "app")
		if err != nil {
			t.Fatal(err)
		}
		got := getIssuer(t, res)
		want := "https://sso.example.com/dex"
		if got != want {
			t.Fatalf("user override should win: want %q, got %q", want, got)
		}
	})
}

// SkipIf points at a missing path → behaves as if empty → derivation fires.
func TestProject_DerivedValues_SkipIfMissing(t *testing.T) {
	mappings := &dslmapping.Document{
		Charts: map[string]dslmapping.ChartEntry{
			"dex": {
				Versions: []dslmapping.VersionEntry{{
					Service: dslmapping.ServiceSpec{Host: "{{ .Release.Name }}-dex"},
					DerivedValues: []dslmapping.DerivedValue{{
						Target:   "dex.derived.value",
						SkipIf:   "nonexistent.path",
						Template: "fallback-{{ .Release.Name }}",
					}},
				}},
			},
		},
	}
	values := valuesWith(map[string]any{"binding": "auth", "type": "dex"})
	res, err := Project(values, mappings, "myapp")
	if err != nil {
		t.Fatal(err)
	}
	sl := res.Overlay["suse-library"].(map[string]any)
	dex := sl["dex"].(map[string]any)
	derived := dex["derived"].(map[string]any)
	if derived["value"] != "fallback-myapp" {
		t.Fatalf("missing skip_if path should NOT skip: got %v", derived["value"])
	}
}

// Empty Template → render fails loud (catches dsl-mappings authoring bug).
func TestProject_DerivedValues_EmptyTemplate(t *testing.T) {
	mappings := &dslmapping.Document{
		Charts: map[string]dslmapping.ChartEntry{
			"dex": {
				Versions: []dslmapping.VersionEntry{{
					Service: dslmapping.ServiceSpec{Host: "{{ .Release.Name }}-dex"},
					DerivedValues: []dslmapping.DerivedValue{{
						Target: "dex.config.issuer",
						// Template intentionally blank.
					}},
				}},
			},
		},
	}
	values := valuesWith(map[string]any{"binding": "auth", "type": "dex"})
	_, err := Project(values, mappings, "app")
	if err == nil {
		t.Fatal("expected error on empty template, got nil")
	}
}
