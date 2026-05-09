package render

import (
	"testing"

	"github.com/idefxH/rda-render-engine/dslmapping"
)

func TestProjectCRDs_RendersRoutes(t *testing.T) {
	ver := dslmapping.VersionEntry{
		CRDProjection: &dslmapping.CRDProjectionSpec{
			GroupVersion: "apisix.apache.org/v2",
			Kind:         "ApisixRoute",
		},
	}

	svc := map[string]any{
		"binding": "gateway",
		"type":    "apisix",
		"routes": []any{
			map[string]any{
				"name":   "api",
				"path":   "/api/*",
				"target": "self",
			},
			map[string]any{
				"name":   "auth",
				"path":   "/auth/*",
				"target": "auth-binding",
			},
		},
	}

	bindings := map[string]*BindingFields{
		"auth-binding": {Host: "app-auth-dex", Port: "5556"},
	}
	suseOut := map[string]any{}

	err := projectCRDs(svc, suseOut, ver, "gateway", bindings, "myapp", 8080)
	if err != nil {
		t.Fatalf("projectCRDs failed: %v", err)
	}

	crds, ok := suseOut["crds"].([]any)
	if !ok || len(crds) != 2 {
		t.Fatalf("expected 2 CRDs, got %v", suseOut["crds"])
	}

	// Check first CRD (self target)
	crd0 := crds[0].(map[string]any)
	if crd0["apiVersion"] != "apisix.apache.org/v2" {
		t.Fatalf("expected apisix apiVersion, got %v", crd0["apiVersion"])
	}
	if crd0["kind"] != "ApisixRoute" {
		t.Fatalf("expected ApisixRoute kind, got %v", crd0["kind"])
	}
	meta := crd0["metadata"].(map[string]any)
	if meta["name"] != "myapp-gateway-api" {
		t.Fatalf("expected myapp-gateway-api, got %v", meta["name"])
	}

	// Check second CRD (cross-binding target)
	crd1 := crds[1].(map[string]any)
	spec1 := crd1["spec"].(map[string]any)
	route1 := spec1["route"].(map[string]any)
	target1 := route1["target"].(map[string]any)
	if target1["host"] != "app-auth-dex" {
		t.Fatalf("expected auth host, got %v", target1["host"])
	}
}

func TestProjectCRDs_MissingTargetFails(t *testing.T) {
	ver := dslmapping.VersionEntry{
		CRDProjection: &dslmapping.CRDProjectionSpec{
			GroupVersion: "gateway.networking.k8s.io/v1",
			Kind:         "HTTPRoute",
		},
	}

	svc := map[string]any{
		"binding": "gw",
		"routes": []any{
			map[string]any{
				"name":   "api",
				"path":   "/api",
				"target": "nonexistent",
			},
		},
	}

	bindings := map[string]*BindingFields{}
	suseOut := map[string]any{}

	err := projectCRDs(svc, suseOut, ver, "gw", bindings, "app", 8080)
	if err == nil {
		t.Fatal("expected error for missing target binding")
	}
}

func TestProjectCRDs_NoRoutesIsNoop(t *testing.T) {
	ver := dslmapping.VersionEntry{
		CRDProjection: &dslmapping.CRDProjectionSpec{
			GroupVersion: "test/v1",
			Kind:         "TestRoute",
		},
	}

	svc := map[string]any{
		"binding": "gw",
	}

	suseOut := map[string]any{}
	err := projectCRDs(svc, suseOut, ver, "gw", nil, "app", 8080)
	if err != nil {
		t.Fatalf("expected no error for no routes, got %v", err)
	}
	if _, ok := suseOut["crds"]; ok {
		t.Fatal("expected no crds in output when routes absent")
	}
}
