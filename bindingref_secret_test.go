// bindingref_secret_test.go — closes idefxH/rda-cli#122. Verifies that
// every binding_secret[] key declared by a chart is reachable as
// ${binding:NAME.<key>}, populated via Literal / Template / FromDSL
// resolution at collectBindings time.

package render

import (
	"strings"
	"testing"

	"github.com/idefxH/rda-render-engine/dslmapping"
)

func TestBindingFields_Secret_LiteralTemplateFromDSL(t *testing.T) {
	// Mock chart entry: 3 binding_secret keys covering all 3
	// resolution paths.
	doc := &dslmapping.Document{
		Charts: map[string]dslmapping.ChartEntry{
			"grafana": {
				Versions: []dslmapping.VersionEntry{
					{
						Constraint: ">=12.0.0",
						Service: dslmapping.ServiceSpec{
							Host: "{{ .Release.Name }}-grafana",
							Port: 80,
						},
						BindingSecret: []dslmapping.BindingSecretEntry{
							// Literal: chart-fixed string.
							{Key: "type", Literal: "grafana"},
							// Template: rendered via Go templating with .Release/.Binding.
							{Key: "host", Template: "{{ .Release.Name }}-grafana"},
							// FromDSL: looked up in the user's services[] entry.
							{Key: "adminPassword", FromDSL: "auth.admin.password", Required: true},
							// FromDSL with default.
							{Key: "adminUser", FromDSL: "auth.admin.name", Default: "admin"},
						},
					},
				},
			},
		},
	}

	values := map[string]any{
		"suse-library": map[string]any{
			"services": []any{
				map[string]any{
					"binding": "dash-x",
					"type":    "grafana",
					"auth": map[string]any{
						"admin": map[string]any{
							"password": "s3cret",
							// Intentionally no name → default "admin" applies.
						},
					},
				},
			},
		},
	}

	bindings := collectBindings(values, doc, "demo")
	bf, ok := bindings["dash-x"]
	if !ok {
		t.Fatalf("collectBindings missed binding=dash-x; got: %v", bindings)
	}

	cases := []struct {
		field, want string
	}{
		{"type", "grafana"},
		{"host", "demo-grafana"},
		{"adminPassword", "s3cret"},
		{"adminUser", "admin"}, // default applied
	}
	for _, c := range cases {
		got, err := bf.Get(c.field)
		if err != nil {
			t.Errorf("Get(%q) returned error: %v", c.field, err)
			continue
		}
		if got != c.want {
			t.Errorf("Get(%q) = %q; want %q", c.field, got, c.want)
		}
	}
}

func TestBindingFields_Secret_PriorityOrder(t *testing.T) {
	// When a chart declares the SAME key in both binding_fields AND
	// binding_secret, binding_fields should win. Keeps the existing
	// chart-author override semantics intact.
	doc := &dslmapping.Document{
		Charts: map[string]dslmapping.ChartEntry{
			"dex": {
				Versions: []dslmapping.VersionEntry{
					{
						Service: dslmapping.ServiceSpec{
							Host: "{{ .Release.Name }}-dex",
							Port: 5556,
						},
						BindingSecret: []dslmapping.BindingSecretEntry{
							{Key: "issuer", Literal: "from-binding-secret"},
						},
						BindingFields: map[string]dslmapping.BindingFieldSpec{
							"issuer": {Template: "from-binding-fields"},
						},
					},
				},
			},
		},
	}
	values := map[string]any{
		"suse-library": map[string]any{
			"services": []any{
				map[string]any{"binding": "auth", "type": "dex"},
			},
		},
	}
	bf := collectBindings(values, doc, "demo")["auth"]
	got, err := bf.Get("issuer")
	if err != nil {
		t.Fatalf("Get(issuer) error: %v", err)
	}
	if got != "from-binding-fields" {
		t.Errorf("priority order wrong: got %q, want from-binding-fields", got)
	}
}

func TestBindingFields_Secret_UnknownKey_ListsValid(t *testing.T) {
	// When the user references a key that's not in any source (hardcoded,
	// computed, OR secret), the error should list everything available.
	doc := &dslmapping.Document{
		Charts: map[string]dslmapping.ChartEntry{
			"dex": {
				Versions: []dslmapping.VersionEntry{
					{
						Service: dslmapping.ServiceSpec{
							Host: "{{ .Release.Name }}-dex",
							Port: 5556,
						},
						BindingSecret: []dslmapping.BindingSecretEntry{
							{Key: "issuer", Literal: "x"},
							{Key: "client_id", Literal: "y"},
						},
					},
				},
			},
		},
	}
	values := map[string]any{
		"suse-library": map[string]any{
			"services": []any{
				map[string]any{"binding": "auth", "type": "dex"},
			},
		},
	}
	bf := collectBindings(values, doc, "demo")["auth"]
	_, err := bf.Get("ghost_field")
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
	for _, want := range []string{"unknown field", "client_id", "issuer", "host"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message missing %q:\n%s", want, err.Error())
		}
	}
}
