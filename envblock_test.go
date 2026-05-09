// envblock_test.go — exercises the workload env: block resolution.
//
// Coverage targets:
//   - Empty / missing env block → no output (no spurious entry).
//   - Bare ref to a binding-secret key → secret entry, secretRef
//     points at <release>-<binding>-binding, secretKey == field.
//   - Bare ref to a NON-secret-key field (derived: host, *_url) →
//     value entry with the resolved literal.
//   - Composed string with one or more refs → value entry, every ref
//     replaced.
//   - Pure literal (no ref) → value entry, unchanged.
//   - Unknown binding → error mentions binding name + available list.
//   - Unknown field on known binding → error from BindingFields.Get.
//   - Whitespace around bare ref still classifies as bare ref.
//   - ${binding-self:...} in env block → not bare-ref (rejected, falls
//     through to composed-string resolution which fails on missing
//     selfBinding).
//   - Output is sorted by env name for diff stability.
package render

import (
	"strings"
	"testing"
)

func mkBindings() map[string]*BindingFields {
	return map[string]*BindingFields{
		"db": {
			Type:       "postgresql",
			Host:       "demo-db-postgresql",
			Port:       "5432",
			URL:        "tcp://demo-db-postgresql:5432",
			Username:   "app",
			Password:   "s3cret",
			Database:   "demo",
			SecretName: "demo-db-binding",
			Ports: map[string]PortFields{
				"tcp": {Port: "5432", URL: "tcp://demo-db-postgresql:5432"},
			},
		},
		"auth": {
			Type:       "dex",
			Host:       "demo-auth-dex",
			Port:       "5556",
			URL:        "http://demo-auth-dex:5556",
			SecretName: "demo-auth-binding",
			Ports: map[string]PortFields{
				"http": {Port: "5556", URL: "http://demo-auth-dex:5556"},
			},
		},
	}
}

func mkSecretIdx() bindingSecretIndex {
	return bindingSecretIndex{
		"db": {
			"host":     true,
			"port":     true,
			"username": true,
			"password": true,
			"database": true,
		},
		"auth": {
			"host": true,
			"port": true,
			// note: NOT including issuer / public_url — those are
			// derived fields, not stored in the binding-secret. So a
			// bare ref to ${binding:auth.url} resolves to a literal.
		},
	}
}

func TestResolveWorkloadEnv_EmptyMissing(t *testing.T) {
	cases := []struct {
		name string
		suse map[string]any
	}{
		{"missing env key", map[string]any{}},
		{"explicit nil", map[string]any{"env": nil}},
		{"empty map", map[string]any{"env": map[string]any{}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveWorkloadEnv(c.suse, mkBindings(), mkSecretIdx(), nil, nil, "demo")
			if err != nil {
				t.Fatalf("expected nil error, got: %v", err)
			}
			if got != nil {
				t.Errorf("expected nil entries, got %d", len(got))
			}
		})
	}
}

func TestResolveWorkloadEnv_BareRef_SecretKey(t *testing.T) {
	suse := map[string]any{
		"env": map[string]any{
			"DB_HOST":     "${binding:db.host}",
			"DB_PASSWORD": "${binding:db.password}",
		},
	}
	got, err := resolveWorkloadEnv(suse, mkBindings(), mkSecretIdx(), nil, nil, "demo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	// Sorted by name.
	if got[0].Name != "DB_HOST" {
		t.Errorf("expected DB_HOST first, got %s", got[0].Name)
	}
	if got[0].Kind != "secret" || got[0].SecretRef != "demo-db-binding" || got[0].SecretKey != "host" {
		t.Errorf("DB_HOST: unexpected entry shape: %+v", got[0])
	}
	if got[1].Kind != "secret" || got[1].SecretKey != "password" {
		t.Errorf("DB_PASSWORD: unexpected entry shape: %+v", got[1])
	}
}

func TestResolveWorkloadEnv_BareRef_DerivedField(t *testing.T) {
	// `auth.url` is NOT in the binding-secret of `auth` (see mkSecretIdx).
	// It's a derived field exposed by BindingFields. The resolver must
	// fall through to literal-value emission.
	suse := map[string]any{
		"env": map[string]any{
			"AUTH_URL": "${binding:auth.url}",
		},
	}
	got, err := resolveWorkloadEnv(suse, mkBindings(), mkSecretIdx(), nil, nil, "demo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	if got[0].Kind != "value" || got[0].Value != "http://demo-auth-dex:5556" {
		t.Errorf("AUTH_URL: expected resolved literal, got: %+v", got[0])
	}
}

func TestResolveWorkloadEnv_ComposedString(t *testing.T) {
	suse := map[string]any{
		"env": map[string]any{
			"DATABASE_URL": "postgres://${binding:db.username}:${binding:db.password}@${binding:db.host}:${binding:db.port}/${binding:db.database}",
		},
	}
	got, err := resolveWorkloadEnv(suse, mkBindings(), mkSecretIdx(), nil, nil, "demo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	want := "postgres://app:s3cret@demo-db-postgresql:5432/demo"
	if got[0].Kind != "value" || got[0].Value != want {
		t.Errorf("DATABASE_URL: expected resolved %q, got: %+v", want, got[0])
	}
}

func TestResolveWorkloadEnv_Literal(t *testing.T) {
	suse := map[string]any{
		"env": map[string]any{
			"NODE_ENV":   "production",
			"DEBUG_MODE": "false",
		},
	}
	got, err := resolveWorkloadEnv(suse, mkBindings(), mkSecretIdx(), nil, nil, "demo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	for _, e := range got {
		if e.Kind != "value" {
			t.Errorf("%s: expected kind=value, got %q", e.Name, e.Kind)
		}
	}
}

func TestResolveWorkloadEnv_NonStringScalars(t *testing.T) {
	// Numbers / bools must be coerced to their string form, not
	// silently dropped or panicked on.
	suse := map[string]any{
		"env": map[string]any{
			"PORT":      8080,
			"IS_DEV":    true,
			"NULL_VAR":  nil,
		},
	}
	got, err := resolveWorkloadEnv(suse, mkBindings(), mkSecretIdx(), nil, nil, "demo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(got))
	}
	want := map[string]string{
		"PORT":     "8080",
		"IS_DEV":   "true",
		"NULL_VAR": "",
	}
	for _, e := range got {
		if e.Value != want[e.Name] {
			t.Errorf("%s: expected value %q, got %q", e.Name, want[e.Name], e.Value)
		}
	}
}

func TestResolveWorkloadEnv_UnknownBinding(t *testing.T) {
	suse := map[string]any{
		"env": map[string]any{
			"FOO": "${binding:nonexistent.host}",
		},
	}
	_, err := resolveWorkloadEnv(suse, mkBindings(), mkSecretIdx(), nil, nil, "demo")
	if err == nil {
		t.Fatal("expected error for unknown binding, got nil")
	}
	if !strings.Contains(err.Error(), "FOO") {
		t.Errorf("error must mention the env name: %v", err)
	}
	if !strings.Contains(err.Error(), `"nonexistent"`) {
		t.Errorf("error must mention the binding name: %v", err)
	}
	if !strings.Contains(err.Error(), "available bindings") {
		t.Errorf("error must list available bindings: %v", err)
	}
}

func TestResolveWorkloadEnv_UnknownField(t *testing.T) {
	suse := map[string]any{
		"env": map[string]any{
			"BAR": "${binding:db.bogus_field}",
		},
	}
	_, err := resolveWorkloadEnv(suse, mkBindings(), mkSecretIdx(), nil, nil, "demo")
	if err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
	if !strings.Contains(err.Error(), "bogus_field") {
		t.Errorf("error must mention the field name: %v", err)
	}
}

func TestResolveWorkloadEnv_OutputSortedByName(t *testing.T) {
	suse := map[string]any{
		"env": map[string]any{
			"Z_LAST":  "z",
			"A_FIRST": "a",
			"M_MID":   "m",
		},
	}
	got, err := resolveWorkloadEnv(suse, mkBindings(), mkSecretIdx(), nil, nil, "demo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantOrder := []string{"A_FIRST", "M_MID", "Z_LAST"}
	for i, want := range wantOrder {
		if got[i].Name != want {
			t.Errorf("sort: pos %d expected %s, got %s", i, want, got[i].Name)
		}
	}
}

func TestParseBareBindingRef(t *testing.T) {
	cases := []struct {
		in           string
		wantBinding  string
		wantField    string
		wantIsBare   bool
	}{
		{"${binding:db.host}", "db", "host", true},
		{"  ${binding:db.host}  ", "", "", false},          // whitespace inside the string makes it composed; trim is caller's job
		{"${binding:db.host}/extra", "", "", false},        // suffix
		{"prefix${binding:db.host}", "", "", false},        // prefix
		{"literal", "", "", false},
		{"", "", "", false},
		{"${binding-self:host}", "", "", false},            // self-form not allowed in workload env
		{"${binding:dotted.field.path}", "dotted", "field.path", true}, // first dot wins
		{"${binding:db.url}", "db", "url", true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			gotB, gotF, gotIsBare := parseBareBindingRef(c.in)
			if gotIsBare != c.wantIsBare || gotB != c.wantBinding || gotF != c.wantField {
				t.Errorf("parseBareBindingRef(%q) = (%q, %q, %v); want (%q, %q, %v)",
					c.in, gotB, gotF, gotIsBare, c.wantBinding, c.wantField, c.wantIsBare)
			}
		})
	}
}

func TestEnvEntriesToValues_ShapeForYAML(t *testing.T) {
	// The deployment.yaml template needs to read `name`, `kind`, and
	// either `secretRef`/`secretKey` or `value` from each entry. Make
	// sure the conversion produces those exact keys (Helm uses the
	// raw YAML keys via `index $entry "..."`).
	entries := []EnvEntry{
		{Name: "DB_HOST", Kind: "secret", SecretRef: "demo-db-binding", SecretKey: "host"},
		{Name: "DATABASE_URL", Kind: "value", Value: "postgres://..."},
	}
	got := envEntriesToValues(entries)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	first, _ := got[0].(map[string]any)
	if first["name"] != "DB_HOST" || first["kind"] != "secret" ||
		first["secretRef"] != "demo-db-binding" || first["secretKey"] != "host" {
		t.Errorf("secret entry shape wrong: %+v", first)
	}
	second, _ := got[1].(map[string]any)
	if second["name"] != "DATABASE_URL" || second["kind"] != "value" ||
		second["value"] != "postgres://..." {
		t.Errorf("value entry shape wrong: %+v", second)
	}
	// secret-only fields must NOT appear on a value entry.
	if _, hasSecretRef := second["secretRef"]; hasSecretRef {
		t.Error("value entry should not include secretRef")
	}
	if _, hasSecretKey := second["secretKey"]; hasSecretKey {
		t.Error("value entry should not include secretKey")
	}
}

func TestEnvResolvedUsesSecretRefOverride(t *testing.T) {
	// When endpoint.secretRef is set for a binding, env vars referencing
	// that binding's secret keys should point at the external Secret name
	// (the secretRef value), not the auto-generated <release>-<binding>-binding.
	suse := map[string]any{
		"env": map[string]any{
			"DB_HOST":     "${binding:db.host}",
			"DB_PASSWORD": "${binding:db.password}",
		},
	}
	overrides := map[string]string{
		"db": "shared-pg-credentials",
	}
	got, err := resolveWorkloadEnv(suse, mkBindings(), mkSecretIdx(), overrides, nil, "demo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	// Both entries should be secret refs pointing at the override name.
	for _, e := range got {
		if e.Kind != "secret" {
			t.Errorf("%s: expected kind=secret, got %q", e.Name, e.Kind)
			continue
		}
		if e.SecretRef != "shared-pg-credentials" {
			t.Errorf("%s: expected secretRef='shared-pg-credentials', got %q", e.Name, e.SecretRef)
		}
	}
	// Verify DB_HOST uses the override (not demo-db-binding)
	if got[0].Name != "DB_HOST" {
		t.Errorf("expected first entry DB_HOST, got %s", got[0].Name)
	}
	if got[0].SecretRef == "demo-db-binding" {
		t.Error("DB_HOST should use the secretRef override, not the auto-generated name")
	}
}

func TestEnvResolvedWithoutSecretRefOverride(t *testing.T) {
	// When no secretRef override is set, the auto-generated name is used.
	suse := map[string]any{
		"env": map[string]any{
			"DB_HOST": "${binding:db.host}",
		},
	}
	got, err := resolveWorkloadEnv(suse, mkBindings(), mkSecretIdx(), nil, nil, "demo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	if got[0].SecretRef != "demo-db-binding" {
		t.Errorf("expected auto-generated secretRef 'demo-db-binding', got %q", got[0].SecretRef)
	}
}

func TestResolveWorkloadEnv_SecretRefOverride(t *testing.T) {
	suse := map[string]any{
		"env": map[string]any{
			"DB_HOST": "${binding:db.host}",
		},
	}
	overrides := map[string]string{
		"db": "shared-pg",
	}
	got, err := resolveWorkloadEnv(suse, mkBindings(), mkSecretIdx(), overrides, nil, "demo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	if got[0].Kind != "secret" {
		t.Fatalf("expected secret kind, got %s", got[0].Kind)
	}
	if got[0].SecretRef != "shared-pg" {
		t.Errorf("expected secretRef 'shared-pg', got %q", got[0].SecretRef)
	}
}

func TestResolveWorkloadEnv_SecretRefOverride_NoOverride(t *testing.T) {
	suse := map[string]any{
		"env": map[string]any{
			"DB_HOST": "${binding:db.host}",
		},
	}
	got, err := resolveWorkloadEnv(suse, mkBindings(), mkSecretIdx(), nil, nil, "demo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got[0].SecretRef != "demo-db-binding" {
		t.Errorf("expected auto-generated secretRef 'demo-db-binding', got %q", got[0].SecretRef)
	}
}

func TestBuildBindingSecretIndex_ReturnsOverrides(t *testing.T) {
	values := map[string]any{
		"suse-library": map[string]any{
			"services": []any{
				map[string]any{
					"binding": "db",
					"type":    "postgresql",
					"endpoint": map[string]any{
						"secretRef": "external-pg",
					},
				},
			},
		},
	}
	_, overrides := buildBindingSecretIndex(values, nil)
	if overrides == nil {
		t.Fatal("expected non-nil overrides map")
	}
}

func TestSortedKeys(t *testing.T) {
	m := map[string]int{"c": 3, "a": 1, "b": 2}
	got := sortedKeys(m)
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("expected %d keys, got %d", len(want), len(got))
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("pos %d: expected %q, got %q", i, v, got[i])
		}
	}
}
