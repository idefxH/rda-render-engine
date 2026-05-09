// Coverage for Phase 2.2 of Design Orientation 0001 — capability
// projection. Targets the projectCapabilities entry point + each
// backend (file-static, file-initdb) + each transform
// (bcrypt-password-to-hash, inline-or-file).
package render

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/idefxH/rda-render-engine/dslmapping"
	"github.com/idefxH/rda-render-engine/errs"
)

func dexCapsSpec() dslmapping.VersionEntry {
	return dslmapping.VersionEntry{
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
			"auth.clients": {
				Backend: dslmapping.BackendFileStatic,
				Order:   2,
				Schema: map[string]dslmapping.FieldSchema{
					"id":           {Required: true},
					"secret":       {Required: true, Secret: true},
					"redirectURIs": {Type: "list", Required: true},
				},
				Projection: dslmapping.ProjectionSpec{
					Target: "dex.config.staticClients",
				},
			},
		},
	}
}

func pgCapsSpec() dslmapping.VersionEntry {
	return dslmapping.VersionEntry{
		Capabilities: map[string]dslmapping.CapabilitySpec{
			"db.schemas": {
				Backend: dslmapping.BackendFileInitDB,
				Order:   1,
				Schema: map[string]dslmapping.FieldSchema{
					"name":    {Required: true},
					"sql":     {},
					"sqlFile": {},
				},
				Projection: dslmapping.ProjectionSpec{
					Target:      "postgresql.primary.initdb.scripts",
					KeyTemplate: "{{ .name }}.sql",
					ValueSource: "sql_or_file",
					Transform:   "inline-or-file",
				},
			},
			"db.seeds": {
				Backend: dslmapping.BackendFileInitDB,
				Order:   2,
				Schema: map[string]dslmapping.FieldSchema{
					"name": {Required: true},
					"sql":  {},
				},
				Projection: dslmapping.ProjectionSpec{
					Target:      "postgresql.primary.initdb.scripts",
					KeyTemplate: "99-{{ .name }}.sql",
					ValueSource: "sql",
				},
			},
		},
	}
}

func TestProjectCapabilities_FileStatic_DexAuthUsersBcrypt(t *testing.T) {
	svc := map[string]any{
		"binding": "auth",
		"type":    "dex",
		"bootstrap": map[string]any{
			"auth.users": []any{
				map[string]any{"name": "alice@example.com", "password": "wonderland"},
				map[string]any{"name": "bob@example.com", "password": "builder"},
			},
		},
	}
	chartBlock := map[string]any{}
	warnings, err := projectCapabilities(svc, chartBlock, dexCapsSpec(), "auth", "dex", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("expected no warnings, got: %v", warnings)
	}

	// Walk to dex.config.staticPasswords.
	dex := chartBlock["dex"].(map[string]any)
	cfg := dex["config"].(map[string]any)
	users := cfg["staticPasswords"].([]any)
	if len(users) != 2 {
		t.Fatalf("expected 2 users, got %d", len(users))
	}
	for i, raw := range users {
		u := raw.(map[string]any)
		// FieldMap: name → email
		if _, hasName := u["name"]; hasName {
			t.Errorf("user[%d]: name should be renamed to email, got %v", i, u)
		}
		if _, hasEmail := u["email"]; !hasEmail {
			t.Errorf("user[%d]: missing email after FieldMap", i)
		}
		// Bcrypt: password removed, hash present and validates.
		if _, hasPw := u["password"]; hasPw {
			t.Errorf("user[%d]: password should be removed by bcrypt transform", i)
		}
		hash, ok := u["hash"].(string)
		if !ok || !strings.HasPrefix(hash, "$2a$") {
			t.Errorf("user[%d]: hash should be bcrypt $2a$..., got %q", i, hash)
		}
	}
	// Hash actually matches the original password.
	first := users[0].(map[string]any)
	if err := bcrypt.CompareHashAndPassword([]byte(first["hash"].(string)), []byte("wonderland")); err != nil {
		t.Errorf("hash should validate against 'wonderland': %v", err)
	}
}

func TestProjectCapabilities_FileStatic_DexAuthClientsIdentityFieldMap(t *testing.T) {
	svc := map[string]any{
		"binding": "auth",
		"type":    "dex",
		"bootstrap": map[string]any{
			"auth.clients": []any{
				map[string]any{
					"id":           "web-app",
					"secret":       "dev-secret",
					"redirectURIs": []any{"http://app.localhost/callback"},
				},
			},
		},
	}
	chartBlock := map[string]any{}
	if _, err := projectCapabilities(svc, chartBlock, dexCapsSpec(), "auth", "dex", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	clients := chartBlock["dex"].(map[string]any)["config"].(map[string]any)["staticClients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("expected 1 client, got %d", len(clients))
	}
	c := clients[0].(map[string]any)
	if c["id"] != "web-app" || c["secret"] != "dev-secret" {
		t.Errorf("client fields not preserved: %+v", c)
	}
}

func TestProjectCapabilities_FileInitDB_PostgresInline(t *testing.T) {
	svc := map[string]any{
		"binding": "db",
		"type":    "postgresql",
		"bootstrap": map[string]any{
			"db.schemas": []any{
				map[string]any{"name": "01-users", "sql": "CREATE TABLE users (id INT);"},
			},
			"db.seeds": []any{
				map[string]any{"name": "fixtures", "sql": "INSERT INTO users VALUES (1);"},
			},
		},
	}
	chartBlock := map[string]any{}
	if _, err := projectCapabilities(svc, chartBlock, pgCapsSpec(), "db", "postgresql", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pg := chartBlock["postgresql"].(map[string]any)
	primary := pg["primary"].(map[string]any)
	initdb := primary["initdb"].(map[string]any)
	scripts := initdb["scripts"].(map[string]any)
	if scripts["01-users.sql"] != "CREATE TABLE users (id INT);" {
		t.Errorf("schema script: %v", scripts["01-users.sql"])
	}
	if scripts["99-fixtures.sql"] != "INSERT INTO users VALUES (1);" {
		t.Errorf("seed script: %v", scripts["99-fixtures.sql"])
	}
}

func TestProjectCapabilities_FileInitDB_SqlFileResolution(t *testing.T) {
	tmp := t.TempDir()
	sqlPath := filepath.Join(tmp, "users.sql")
	if err := os.WriteFile(sqlPath, []byte("CREATE TABLE users (id INT);"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := map[string]any{
		"binding": "db",
		"bootstrap": map[string]any{
			"db.schemas": []any{
				map[string]any{"name": "01-users", "sqlFile": "users.sql"},
			},
		},
	}
	chartBlock := map[string]any{}
	if _, err := projectCapabilities(svc, chartBlock, pgCapsSpec(), "db", "postgresql", tmp); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	scripts := chartBlock["postgresql"].(map[string]any)["primary"].(map[string]any)["initdb"].(map[string]any)["scripts"].(map[string]any)
	if scripts["01-users.sql"] != "CREATE TABLE users (id INT);" {
		t.Errorf("sqlFile not inlined: %v", scripts["01-users.sql"])
	}
}

func TestProjectCapabilities_RequiredFieldMissing(t *testing.T) {
	svc := map[string]any{
		"bootstrap": map[string]any{
			"auth.users": []any{
				map[string]any{"name": "alice@example.com"}, // missing password (Required)
			},
		},
	}
	_, err := projectCapabilities(svc, map[string]any{}, dexCapsSpec(), "auth", "dex", "")
	if err == nil {
		t.Fatal("expected error for missing required field")
	}
	if !errors.Is(err, errs.ErrInvocation) {
		t.Errorf("expected ErrInvocation, got: %v", err)
	}
	for _, want := range []string{"auth", "auth.users", "[0]", "password"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %s", want, err)
		}
	}
}

func TestProjectCapabilities_ReservedBackendErrorsLoud(t *testing.T) {
	ver := dslmapping.VersionEntry{
		Capabilities: map[string]dslmapping.CapabilitySpec{
			"auth.users": {Backend: dslmapping.BackendAPIJob},
		},
	}
	svc := map[string]any{
		"bootstrap": map[string]any{
			"auth.users": []any{map[string]any{"name": "x"}},
		},
	}
	_, err := projectCapabilities(svc, map[string]any{}, ver, "auth", "keycloak", "")
	if err == nil {
		t.Fatal("expected error for api-job backend in Phase 2.2")
	}
	if !errors.Is(err, errs.ErrCapabilityBackendUnsupported) {
		t.Errorf("expected ErrCapabilityBackendUnsupported, got: %v", err)
	}
}

func TestProjectCapabilities_UnknownBackendErrorsLoud(t *testing.T) {
	ver := dslmapping.VersionEntry{
		Capabilities: map[string]dslmapping.CapabilitySpec{
			"x": {Backend: "made-up"},
		},
	}
	svc := map[string]any{
		"bootstrap": map[string]any{
			"x": []any{map[string]any{"name": "x"}},
		},
	}
	_, err := projectCapabilities(svc, map[string]any{}, ver, "x", "x", "")
	if err == nil {
		t.Fatal("expected error for unknown backend")
	}
	if !errors.Is(err, errs.ErrCapabilityBackendUnknown) {
		t.Errorf("expected ErrCapabilityBackendUnknown, got: %v", err)
	}
}

func TestProjectCapabilities_BootstrapNullDropsCap(t *testing.T) {
	// `bootstrap.auth.users: ~` (nil) is the documented "drop this
	// cap" pattern (DO 0001 §3.5). Must NOT error and must NOT
	// project anything.
	svc := map[string]any{
		"bootstrap": map[string]any{
			"auth.users": nil,
		},
	}
	chartBlock := map[string]any{}
	if _, err := projectCapabilities(svc, chartBlock, dexCapsSpec(), "auth", "dex", ""); err != nil {
		t.Fatalf("nil cap should be silent drop, got: %v", err)
	}
	if len(chartBlock) != 0 {
		t.Errorf("expected empty chartBlock after nil cap, got: %v", chartBlock)
	}
}

func TestProjectCapabilities_UnknownCapWarnsAndSkips(t *testing.T) {
	// User declares `bootstrap.unknown.cap` but dsl-mappings has
	// no such cap. Forward-compat: warn + skip, don't fail loud
	// (bundle authors land caps before users upgrade, vice versa).
	svc := map[string]any{
		"bootstrap": map[string]any{
			"unknown.cap": []any{map[string]any{"foo": "bar"}},
		},
	}
	chartBlock := map[string]any{}
	warnings, err := projectCapabilities(svc, chartBlock, dexCapsSpec(), "auth", "dex", "")
	if err != nil {
		t.Fatalf("unknown cap should warn, not error: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "unknown.cap") {
		t.Errorf("expected warning about unknown.cap, got: %v", warnings)
	}
}

func TestProjectCapabilities_BootstrapMustBeMap(t *testing.T) {
	svc := map[string]any{
		"bootstrap": "should-be-map-not-string",
	}
	_, err := projectCapabilities(svc, map[string]any{}, dexCapsSpec(), "auth", "dex", "")
	if err == nil || !errors.Is(err, errs.ErrInvocation) {
		t.Errorf("expected ErrInvocation for non-map bootstrap, got: %v", err)
	}
}

func TestProjectCapabilities_ItemsMustBeList(t *testing.T) {
	// Common typo: `bootstrap.auth.users: { name: alice, password: x }`
	// instead of `- name: alice` (a list of one).
	svc := map[string]any{
		"bootstrap": map[string]any{
			"auth.users": map[string]any{"name": "alice", "password": "x"},
		},
	}
	_, err := projectCapabilities(svc, map[string]any{}, dexCapsSpec(), "auth", "dex", "")
	if err == nil || !errors.Is(err, errs.ErrInvocation) {
		t.Errorf("expected ErrInvocation for non-list items, got: %v", err)
	}
	if !strings.Contains(err.Error(), "list") {
		t.Errorf("error should mention 'list', got: %s", err)
	}
}

func TestProjectCapabilities_OrderRespected(t *testing.T) {
	// Custom spec: users order=2, clients order=1. Despite alphabetical
	// order putting clients first, projection must follow Order.
	ver := dslmapping.VersionEntry{
		Capabilities: map[string]dslmapping.CapabilitySpec{
			"a.first":  {Backend: dslmapping.BackendFileStatic, Order: 1, Projection: dslmapping.ProjectionSpec{Target: "out.first"}},
			"a.second": {Backend: dslmapping.BackendFileStatic, Order: 2, Projection: dslmapping.ProjectionSpec{Target: "out.second"}},
		},
	}
	svc := map[string]any{
		"bootstrap": map[string]any{
			"a.first":  []any{map[string]any{"x": 1}},
			"a.second": []any{map[string]any{"x": 2}},
		},
	}
	chartBlock := map[string]any{}
	if _, err := projectCapabilities(svc, chartBlock, ver, "x", "x", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := chartBlock["out"].(map[string]any)
	if _, ok := out["first"]; !ok {
		t.Errorf("first should be projected")
	}
	if _, ok := out["second"]; !ok {
		t.Errorf("second should be projected")
	}
}

func TestProjectCapabilities_LegacyJobsKeyIgnored(t *testing.T) {
	// `bootstrap.jobs[]` is the legacy mechanism (rda-cli #92).
	// Must coexist — projectCapabilities must NOT try to consume
	// `jobs` as a capability (no spec exists for it).
	svc := map[string]any{
		"bootstrap": map[string]any{
			"jobs": []any{map[string]any{"name": "legacy-job"}},
			"auth.users": []any{
				map[string]any{"name": "alice", "password": "pw"},
			},
		},
	}
	chartBlock := map[string]any{}
	warnings, err := projectCapabilities(svc, chartBlock, dexCapsSpec(), "auth", "dex", "")
	if err != nil {
		t.Fatalf("legacy jobs key must coexist, got: %v", err)
	}
	for _, w := range warnings {
		if strings.Contains(w, "jobs") {
			t.Errorf("legacy jobs should be silently skipped, got warning: %s", w)
		}
	}
	// auth.users still projected.
	if chartBlock["dex"] == nil {
		t.Errorf("auth.users projection should fire alongside legacy jobs")
	}
}
