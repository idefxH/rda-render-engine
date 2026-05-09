// Coverage for Phase 1 #2 of DO 0001-A: chart-author binding_fields
// surface as ${binding:NAME.<field>} computed values.
package render

import (
	"strings"
	"testing"

	"github.com/idefxH/rda-render-engine/dslmapping"
)

func mkMappingsWithBindingFields() *dslmapping.Document {
	return &dslmapping.Document{
		Charts: map[string]dslmapping.ChartEntry{
			"dex": {Versions: []dslmapping.VersionEntry{{
				Service: dslmapping.ServiceSpec{Host: "{{ .Release.Name }}-dex"},
				BindingFields: map[string]dslmapping.BindingFieldSpec{
					"issuer": {Template: `{{ if .Service.ingress }}{{ if .Service.ingress.enabled }}http://{{ index .Service.ingress "host" }}{{ end }}{{ end }}{{ if not (and .Service.ingress (index .Service.ingress "enabled")) }}http://{{ .Release.Name }}-{{ .Binding }}-dex:5556{{ end }}`},
					"public_url": {Template: `same-as-issuer-{{ .Binding }}`},
				},
			}}},
		},
	}
}

func TestCollectBindings_BindingFields_PopulatesComputed(t *testing.T) {
	mappings := mkMappingsWithBindingFields()
	values := map[string]any{
		"suse-library": map[string]any{
			"services": []any{
				map[string]any{
					"binding": "auth",
					"type":    "dex",
					"ingress": map[string]any{
						"enabled": true,
						"host":    "auth.example.com",
					},
				},
			},
		},
	}
	got := collectBindings(values, mappings, "myapp")
	bf, ok := got["auth"]
	if !ok {
		t.Fatalf("expected `auth` binding, got: %v", got)
	}
	if bf.Computed == nil {
		t.Fatal("Computed should be populated, got nil")
	}
	if iss := bf.Computed["issuer"]; iss != "http://auth.example.com" {
		t.Errorf("issuer template should resolve via ingress.host: got %q", iss)
	}
	if pu := bf.Computed["public_url"]; pu != "same-as-issuer-auth" {
		t.Errorf("public_url should expand .Binding to 'auth': got %q", pu)
	}
}

func TestCollectBindings_BindingFields_NoIngressUsesFallback(t *testing.T) {
	mappings := mkMappingsWithBindingFields()
	values := map[string]any{
		"suse-library": map[string]any{
			"services": []any{
				map[string]any{"binding": "auth", "type": "dex"},
			},
		},
	}
	got := collectBindings(values, mappings, "myapp")
	bf := got["auth"]
	want := "http://myapp-auth-dex:5556"
	if got := bf.Computed["issuer"]; got != want {
		t.Errorf("no-ingress fallback: expected %q, got %q", want, got)
	}
}

func TestBindingFields_Get_HardcodedWinsOverComputed(t *testing.T) {
	// A chart-author who declares `binding_fields.host` (a name in
	// the well-known set) gets ignored — the hardcoded host wins.
	bf := &BindingFields{
		Host:     "real-host",
		Computed: map[string]string{"host": "shadowed"},
	}
	got, err := bf.Get("host")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "real-host" {
		t.Errorf("hardcoded should win, got %q", got)
	}
}

func TestBindingFields_Get_ComputedField(t *testing.T) {
	bf := &BindingFields{
		Computed: map[string]string{
			"issuer":     "http://auth.example.com",
			"public_url": "http://auth.example.com",
		},
	}
	got, err := bf.Get("issuer")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "http://auth.example.com" {
		t.Errorf("expected issuer to resolve, got %q", got)
	}
}

func TestBindingFields_Get_UnknownField_ListsComputed(t *testing.T) {
	bf := &BindingFields{
		Host:     "h",
		Computed: map[string]string{"issuer": "x", "public_url": "y"},
	}
	_, err := bf.Get("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
	msg := err.Error()
	for _, want := range []string{"issuer", "public_url", "host"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message should list %q in valid fields, got: %s", want, msg)
		}
	}
}

func TestRenderBindingFieldTemplates_BadTemplateFallsThroughSilently(t *testing.T) {
	specs := map[string]dslmapping.BindingFieldSpec{
		"good":              {Template: `{{ .Binding }}`},
		"bad-syntax":        {Template: `{{ .Binding`},                  // missing }}
		"bad-execute":       {Template: `{{ .DoesNotExist.Field }}`},     // works but resolves to nothing
		"empty":             {Template: ``},                              // skipped
	}
	got := renderBindingFieldTemplates(specs, map[string]any{}, "myapp", "auth", "dex")
	if got["good"] != "auth" {
		t.Errorf("good template should resolve to 'auth', got %q", got["good"])
	}
	if _, ok := got["bad-syntax"]; ok {
		t.Errorf("malformed template should NOT yield a key, got %q", got["bad-syntax"])
	}
	if _, ok := got["empty"]; ok {
		t.Errorf("empty template should be skipped, got %q", got["empty"])
	}
}
