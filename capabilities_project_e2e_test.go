package render

import (
	"strings"
	"testing"

	"github.com/idefxH/rda-render-engine/dslmapping"
)

// TestProject_CapabilitiesLandAtChartPrefixedTarget guards against a
// regression where projectCapabilities was called with chartBlock
// (i.e. suseOut[chartType]) instead of suseOut. With chartBlock as
// root, a capability whose target is "dex.config.staticPasswords"
// would land at suseOut.dex.dex.config.staticPasswords (doubly
// nested), or — more commonly — get silently lost because passthrough
// merge wrote `[]` to suseOut.dex.config.staticPasswords first. This
// test exercises the full Project() path so the wiring stays
// consistent with values_mapping and derived_values, both of which
// use chart-prefixed targets applied at suseOut level.
func TestProject_CapabilitiesLandAtChartPrefixedTarget(t *testing.T) {
	mappings := &dslmapping.Document{
		APIVersion: "rda.suse.com/dsl-mapping/v1alpha1",
		Charts: map[string]dslmapping.ChartEntry{
			"dex": {
				Versions: []dslmapping.VersionEntry{{
					Constraint: ">=0.24.0 <1.0.0",
					Service:    dslmapping.ServiceSpec{Host: "{{ .Release.Name }}-dex", Port: 5556},
					Capabilities: map[string]dslmapping.CapabilitySpec{
						"auth.users": {
							Backend: dslmapping.BackendFileStatic,
							Order:   1,
							Schema: map[string]dslmapping.FieldSchema{
								"name":     {Type: "string", Required: true},
								"password": {Type: "string", Required: true, Secret: true},
							},
							Projection: dslmapping.ProjectionSpec{
								Target:    "dex.config.staticPasswords",
								Transform: "bcrypt-password-to-hash",
								FieldMap:  map[string]string{"name": "email"},
							},
						},
					},
				}},
			},
		},
	}
	values := map[string]any{
		"suse-library": map[string]any{
			"services": []any{
				map[string]any{
					"binding": "auth",
					"type":    "dex",
					"enabled": true,
					"bootstrap": map[string]any{
						"auth.users": []any{
							map[string]any{"name": "alice@example.com", "password": "wonderland"},
						},
					},
				},
			},
		},
	}
	res, err := Project(values, mappings, "demo")
	if err != nil {
		t.Fatalf("Project failed: %v", err)
	}

	// MUST land at suse-library.dex.config.staticPasswords (chart-
	// prefixed target applied at suseOut level — same convention as
	// values_mapping and derived_values).
	suse := res.Overlay["suse-library"].(map[string]any)
	dex, ok := suse["dex"].(map[string]any)
	if !ok {
		t.Fatalf("suse-library.dex missing or wrong shape: %+v", suse)
	}
	cfg, ok := dex["config"].(map[string]any)
	if !ok {
		t.Fatalf("suse-library.dex.config missing or wrong shape: %+v", dex)
	}
	users, ok := cfg["staticPasswords"].([]any)
	if !ok || len(users) != 1 {
		t.Fatalf("suse-library.dex.config.staticPasswords wrong shape/len: %+v", cfg["staticPasswords"])
	}

	// MUST NOT have nested `dex` under suse-library.dex (the bug
	// signature: capability target was treated relative to chartBlock
	// instead of suseOut, producing dex.dex.config.staticPasswords).
	if _, surNested := dex["dex"]; surNested {
		t.Fatalf("regression: capability target landed at dex.dex.config.* — should be dex.config.* (overlay: %+v)", suse)
	}

	// FieldMap + Transform sanity (would catch unrelated wiring breaks).
	u := users[0].(map[string]any)
	if u["email"] != "alice@example.com" {
		t.Errorf("FieldMap name→email: got %v", u)
	}
	hash, _ := u["hash"].(string)
	if !strings.HasPrefix(hash, "$2a$") {
		t.Errorf("bcrypt transform: got hash=%q", hash)
	}
}
