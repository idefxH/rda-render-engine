package render

import "testing"

func TestGetAtPath_NestedHyphenKeys(t *testing.T) {
	m := map[string]any{
		"oauth2-proxy": map[string]any{
			"extraArgs": map[string]any{
				"oidc-issuer-url": "http://ha-core-dex:5556",
			},
		},
	}
	result := getAtPath(m, "oauth2-proxy.extraArgs.oidc-issuer-url")
	if result == nil {
		t.Fatal("getAtPath returned nil, expected http://ha-core-dex:5556")
	}
	if result != "http://ha-core-dex:5556" {
		t.Fatalf("getAtPath returned %v, expected http://ha-core-dex:5556", result)
	}
}

func TestGetAtPath_Missing(t *testing.T) {
	m := map[string]any{
		"oauth2-proxy": map[string]any{},
	}
	result := getAtPath(m, "oauth2-proxy.extraArgs.oidc-issuer-url")
	if result != nil {
		t.Fatalf("expected nil for missing path, got %v", result)
	}
}
