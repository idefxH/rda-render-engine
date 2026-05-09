// Coverage for Phase 2.1 of Design Orientation 0001: dsl-mappings
// capabilities schema parses correctly. Phase 2.1 is data-model only
// — no projection / CLI yet — so these tests assert the loaded shape,
// not runtime behaviour.
package dslmapping

import (
	"strings"
	"testing"
)

func TestLoad_Capabilities_DexAuthUsers(t *testing.T) {
	body := `
charts:
  dex:
    versions:
      - constraint: ">=0.24.0 <1.0.0"
        capabilities:
          auth.users:
            backend: file-static
            order: 1
            schema:
              name:     { type: string, required: true }
              password: { type: string, required: true, secret: true }
              hash:     { type: string }
            projection:
              target: dex.config.staticPasswords
              transform: bcrypt-password-to-hash
              field_map:
                name: email
          auth.clients:
            backend: file-static
            order: 2
            schema:
              id:           { type: string, required: true }
              secret:       { type: string, required: true, secret: true }
              redirectURIs: { type: list, required: true }
            projection:
              target: dex.config.staticClients
`
	bundleDir := writeBundle(t, body)
	doc, err := loadFromDir(bundleDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	dex, ok := doc.Charts["dex"]
	if !ok || len(dex.Versions) == 0 {
		t.Fatal("expected dex chart in mapping")
	}
	caps := dex.Versions[0].Capabilities
	if len(caps) != 2 {
		t.Fatalf("expected 2 capabilities, got %d: %v", len(caps), caps)
	}

	users, ok := caps["auth.users"]
	if !ok {
		t.Fatal("expected auth.users capability")
	}
	if users.Backend != BackendFileStatic {
		t.Errorf("backend: expected file-static, got %q", users.Backend)
	}
	if users.Order != 1 {
		t.Errorf("order: expected 1, got %d", users.Order)
	}
	if users.Projection.Target != "dex.config.staticPasswords" {
		t.Errorf("target: %q", users.Projection.Target)
	}
	if users.Projection.Transform != "bcrypt-password-to-hash" {
		t.Errorf("transform: %q", users.Projection.Transform)
	}
	if users.Projection.FieldMap["name"] != "email" {
		t.Errorf("field_map: %v", users.Projection.FieldMap)
	}
	pwSchema, ok := users.Schema["password"]
	if !ok || !pwSchema.Required || !pwSchema.Secret {
		t.Errorf("password schema should be required+secret, got: %+v", pwSchema)
	}

	clients, ok := caps["auth.clients"]
	if !ok {
		t.Fatal("expected auth.clients capability")
	}
	if clients.Order != 2 {
		t.Errorf("clients order: expected 2, got %d", clients.Order)
	}
	uriSchema := clients.Schema["redirectURIs"]
	if uriSchema.Type != "list" || !uriSchema.Required {
		t.Errorf("redirectURIs schema: expected list/required, got: %+v", uriSchema)
	}
}

func TestLoad_Capabilities_PostgresqlInitDB(t *testing.T) {
	body := `
charts:
  postgresql:
    versions:
      - constraint: ">=0.4.0 <1.0.0"
        capabilities:
          db.schemas:
            backend: file-initdb
            order: 1
            schema:
              name:    { type: string, required: true }
              sql:     { type: string }
              sqlFile: { type: string }
            projection:
              target: postgresql.primary.initdb.scripts
              key_template: "{{ .name }}.sql"
              value_source: sql_or_file
              transform: inline-or-file
          db.seeds:
            backend: file-initdb
            order: 2
            schema:
              name: { type: string, required: true }
              sql:  { type: string }
            projection:
              target: postgresql.primary.initdb.scripts
              key_template: "99-{{ .name }}.sql"
              value_source: sql
`
	bundleDir := writeBundle(t, body)
	doc, err := loadFromDir(bundleDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	caps := doc.Charts["postgresql"].Versions[0].Capabilities
	schemas := caps["db.schemas"]
	if schemas.Backend != BackendFileInitDB {
		t.Errorf("backend: expected file-initdb, got %q", schemas.Backend)
	}
	if schemas.Projection.KeyTemplate != "{{ .name }}.sql" {
		t.Errorf("key_template: %q", schemas.Projection.KeyTemplate)
	}
	if schemas.Projection.ValueSource != "sql_or_file" {
		t.Errorf("value_source: %q", schemas.Projection.ValueSource)
	}
	if schemas.Projection.Transform != "inline-or-file" {
		t.Errorf("transform: %q", schemas.Projection.Transform)
	}

	// db.seeds runs AFTER db.schemas (Order 2 > Order 1).
	seeds := caps["db.seeds"]
	if seeds.Order <= schemas.Order {
		t.Errorf("seeds.Order (%d) should be > schemas.Order (%d)", seeds.Order, schemas.Order)
	}
	// `99-{{ .name }}.sql` prefixes seeds in the lexical order
	// postgres uses for /docker-entrypoint-initdb.d execution.
	if !strings.HasPrefix(seeds.Projection.KeyTemplate, "99-") {
		t.Errorf("seeds key should be prefixed for execution order, got %q", seeds.Projection.KeyTemplate)
	}
}

func TestLoad_Capabilities_AcceptsReservedBackends(t *testing.T) {
	// api-job and k8s-resource are reserved values. The loader
	// accepts them; the projector (Phase 2.2+) will return a clear
	// error at render time.
	body := `
charts:
  keycloak:
    versions:
      - constraint: ">=1.0.0"
        capabilities:
          auth.users:
            backend: api-job
            job:
              template: charts/keycloak/jobs/seed-users.yaml
          auth.clients:
            backend: k8s-resource
`
	bundleDir := writeBundle(t, body)
	doc, err := loadFromDir(bundleDir)
	if err != nil {
		t.Fatalf("loader must accept reserved backends, got: %v", err)
	}
	caps := doc.Charts["keycloak"].Versions[0].Capabilities
	if caps["auth.users"].Backend != BackendAPIJob {
		t.Errorf("api-job backend not parsed")
	}
	if caps["auth.users"].Job == nil || caps["auth.users"].Job.Template == "" {
		t.Errorf("api-job should carry a Job template")
	}
	if caps["auth.clients"].Backend != BackendK8sResource {
		t.Errorf("k8s-resource backend not parsed")
	}
}

func TestLoad_Capabilities_AbsentMeansNoCapabilities(t *testing.T) {
	// Charts predating Phase 2 (everything currently in main) have
	// no `capabilities:` block. The loader must accept that as the
	// normal case — capabilities map is nil/empty, no error.
	body := `
charts:
  redis:
    versions:
      - constraint: ">=2.0.0 <3.0.0"
        service: { host: "{{ .Release.Name }}-redis", port: 6379 }
`
	bundleDir := writeBundle(t, body)
	doc, err := loadFromDir(bundleDir)
	if err != nil {
		t.Fatalf("absent capabilities: must NOT be an error, got: %v", err)
	}
	caps := doc.Charts["redis"].Versions[0].Capabilities
	if len(caps) != 0 {
		t.Errorf("expected no capabilities, got %d: %v", len(caps), caps)
	}
}

func TestLoad_Capabilities_FieldSchemaDefaults(t *testing.T) {
	// Empty Type on a field defaults to "string" (caller-side
	// convenience); Required defaults to false; Secret defaults to
	// false. The loader keeps the YAML faithful — the projector /
	// CLI applies defaults when reading the parsed struct.
	body := `
charts:
  minio:
    versions:
      - constraint: ">=8.0.0"
        capabilities:
          storage.buckets:
            backend: api-job
            schema:
              name:   { required: true }
              public: { type: bool }
`
	bundleDir := writeBundle(t, body)
	doc, err := loadFromDir(bundleDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	caps := doc.Charts["minio"].Versions[0].Capabilities
	bucket := caps["storage.buckets"].Schema
	// `name` had no Type → loader stores "" (empty); caller defaults
	// to "string" when it sees empty.
	if bucket["name"].Type != "" {
		t.Errorf("empty Type should round-trip as empty, got %q", bucket["name"].Type)
	}
	if !bucket["name"].Required {
		t.Errorf("name should be required")
	}
	if bucket["public"].Type != "bool" {
		t.Errorf("public type: %q", bucket["public"].Type)
	}
	if bucket["public"].Required {
		t.Errorf("public should NOT be required (default false)")
	}
}
