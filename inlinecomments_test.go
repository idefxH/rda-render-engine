package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idefxH/rda-render-engine/dslmapping"
)

func writeTmpYAML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "values.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func mappingsForTest() *dslmapping.Document {
	return &dslmapping.Document{
		Charts: map[string]dslmapping.ChartEntry{
			"postgresql": {Versions: []dslmapping.VersionEntry{{
				Service:       dslmapping.ServiceSpec{Host: "{{ .Release.Name }}-postgresql", Port: 5432},
				BindingSecret: []dslmapping.BindingSecretEntry{{Key: "host"}, {Key: "port"}, {Key: "password"}},
			}}},
			"dex": {Versions: []dslmapping.VersionEntry{{
				Service: dslmapping.ServiceSpec{Host: "{{ .Release.Name }}-dex", Port: 5556},
				BindingFields: map[string]dslmapping.BindingFieldSpec{
					"issuer": {Template: "http://{{ .Release.Name }}-{{ .Binding }}-dex:5556"},
				},
			}}},
		},
	}
}

func TestMaintainInlineComments_NoFile_NoOp(t *testing.T) {
	changed, err := MaintainInlineComments("/tmp/does-not-exist.yaml", mappingsForTest(), "demo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changed {
		t.Errorf("absent file should be no-op")
	}
}

func TestMaintainInlineComments_NoEnvBlock_NoOp(t *testing.T) {
	body := `suse-library:
  services:
    - binding: db
      type: postgresql
`
	p := writeTmpYAML(t, body)
	changed, err := MaintainInlineComments(p, mappingsForTest(), "demo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changed {
		t.Errorf("no env block → no rewrite")
	}
}

func TestMaintainInlineComments_AddsResolvedComment(t *testing.T) {
	body := `suse-library:
  env:
    DB_HOST: ${binding:db.host}
    DB_PORT: ${binding:db.port}
    NOT_A_REF: literal-value
  services:
    - binding: db
      type: postgresql
      auth:
        user:
          name: app
          password: pw
          database: app
`
	p := writeTmpYAML(t, body)
	changed, err := MaintainInlineComments(p, mappingsForTest(), "demo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Fatal("expected change, got no-op")
	}
	out, _ := os.ReadFile(p)
	if !strings.Contains(string(out), "→ demo-postgresql") {
		t.Errorf("DB_HOST should resolve to demo-postgresql FQDN; got:\n%s", out)
	}
	if !strings.Contains(string(out), "→ 5432") {
		t.Errorf("DB_PORT should resolve to 5432; got:\n%s", out)
	}
	if strings.Contains(string(out), "literal-value # →") || strings.Contains(string(out), "literal-value  # →") {
		t.Errorf("non-ref entry NOT_A_REF should be untouched; got:\n%s", out)
	}
}

func TestMaintainInlineComments_DerivedFieldAnnotation(t *testing.T) {
	body := `suse-library:
  env:
    AUTH_HOST: ${binding:auth.host}
    AUTH_ISSUER: ${binding:auth.issuer}
  services:
    - binding: auth
      type: dex
`
	p := writeTmpYAML(t, body)
	changed, _ := MaintainInlineComments(p, mappingsForTest(), "demo")
	if !changed {
		t.Fatal("expected change")
	}
	out, _ := os.ReadFile(p)
	// Derived field should carry the (derived: ...) annotation.
	if !strings.Contains(string(out), "(derived: auth.issuer)") {
		t.Errorf("AUTH_ISSUER should carry (derived: ...) annotation; got:\n%s", out)
	}
	// Hardcoded host should NOT carry the annotation.
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "AUTH_HOST") && strings.Contains(line, "derived:") {
			t.Errorf("AUTH_HOST is hardcoded; should NOT have (derived:) annotation. Line: %q", line)
		}
	}
}

func TestMaintainInlineComments_Idempotent(t *testing.T) {
	body := `suse-library:
  env:
    DB_HOST: ${binding:db.host}
  services:
    - binding: db
      type: postgresql
`
	p := writeTmpYAML(t, body)
	changed1, _ := MaintainInlineComments(p, mappingsForTest(), "demo")
	if !changed1 {
		t.Fatal("first call should mutate")
	}
	changed2, _ := MaintainInlineComments(p, mappingsForTest(), "demo")
	if changed2 {
		t.Errorf("second call must be no-op (idempotent)")
	}
}

func TestMaintainInlineComments_OverwritesStaleComment(t *testing.T) {
	// Pre-existing `# → old-value` comment should be replaced when
	// the resolved value changes.
	body := `suse-library:
  env:
    DB_HOST: ${binding:db.host} # → stale-host
  services:
    - binding: db
      type: postgresql
`
	p := writeTmpYAML(t, body)
	changed, _ := MaintainInlineComments(p, mappingsForTest(), "freshname")
	if !changed {
		t.Fatal("expected mutation when comment is stale")
	}
	out, _ := os.ReadFile(p)
	if strings.Contains(string(out), "stale-host") {
		t.Errorf("stale value should be replaced; got:\n%s", out)
	}
	if !strings.Contains(string(out), "freshname-postgresql") {
		t.Errorf("expected fresh resolved value; got:\n%s", out)
	}
}

func TestMaintainInlineComments_ErrorRefShowsErrInComment(t *testing.T) {
	body := `suse-library:
  env:
    BAD_REF: ${binding:nonexistent.host}
  services:
    - binding: db
      type: postgresql
`
	p := writeTmpYAML(t, body)
	changed, _ := MaintainInlineComments(p, mappingsForTest(), "demo")
	if !changed {
		t.Fatal("expected mutation (error comment is still a write)")
	}
	out, _ := os.ReadFile(p)
	if !strings.Contains(string(out), "ERROR:") {
		t.Errorf("typo'd ref should surface ERROR: in the comment; got:\n%s", out)
	}
}

func TestMaintainInlineComments_PreservesOtherComments(t *testing.T) {
	// HeadComments and unrelated LineComments must survive.
	body := `# This is a head comment for suse-library
suse-library:
  # Another comment
  env:
    # Comment above DB_HOST
    DB_HOST: ${binding:db.host}
  services:
    - binding: db
      type: postgresql
`
	p := writeTmpYAML(t, body)
	MaintainInlineComments(p, mappingsForTest(), "demo")
	out, _ := os.ReadFile(p)
	for _, want := range []string{
		"# This is a head comment for suse-library",
		"# Another comment",
		"# Comment above DB_HOST",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("preserve comment %q; got:\n%s", want, out)
		}
	}
}
