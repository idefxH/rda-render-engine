package dslmapping

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

// loadFromDir reads library-chart/dsl-mappings.yaml from the given root
// directory. This replaces the rda-cli Load(types.OpinionBundle) that
// depends on the CLI-specific types package.
func loadFromDir(root string) (*Document, error) {
	path := filepath.Join(root, "library-chart", "dsl-mappings.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var doc Document
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &doc, nil
}

// writeBundle materialises a minimal opinion-bundle layout under tmp with the
// given dsl-mappings.yaml content (or omitted when content == ""). Returns the
// bundle root path.
func writeBundle(t *testing.T, content string) string {
	t.Helper()
	tmp := t.TempDir()
	libDir := filepath.Join(tmp, "library-chart")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if content != "" {
		path := filepath.Join(libDir, "dsl-mappings.yaml")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	return tmp
}

const sampleMapping = `apiVersion: rda.suse.com/dsl-mapping/v1alpha1
charts:
  postgresql:
    versions:
      - constraint: ">=0.4.0 <1.0.0"
        service: {host: "{{ .Release.Name }}-postgresql", port: 5432}
        values_mapping:
          auth.user.name: postgresql.auth.username
          auth.user.password: postgresql.auth.password
        binding_secret:
          - {key: type, literal: postgresql, skip_env: true}
          - {key: username, from_dsl: auth.user.name, default: app}
          - {key: password, from_dsl: auth.user.password, required: true}
  redis:
    versions:
      - constraint: ">=21.0.0"
        service: {host: "{{ .Release.Name }}-redis-master", port: 6379}
        values_mapping:
          auth.password: redis.auth.password
        binding_secret:
          - {key: password, from_dsl: auth.password, required: true}
`

func TestLoad_MissingFile_ReturnsNilNoError(t *testing.T) {
	bundleDir := writeBundle(t, "")
	doc, err := loadFromDir(bundleDir)
	if err != nil {
		t.Fatalf("Load: unexpected err: %v", err)
	}
	if doc != nil {
		t.Fatalf("Load: expected nil doc for missing file, got %+v", doc)
	}
}

func TestLoad_Parses(t *testing.T) {
	bundleDir := writeBundle(t, sampleMapping)
	doc, err := loadFromDir(bundleDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if doc == nil {
		t.Fatal("Load: expected non-nil doc")
	}
	if doc.APIVersion != "rda.suse.com/dsl-mapping/v1alpha1" {
		t.Errorf("APIVersion = %q, want rda.suse.com/dsl-mapping/v1alpha1", doc.APIVersion)
	}
	if _, ok := doc.Charts["postgresql"]; !ok {
		t.Error("missing postgresql chart entry")
	}
}

func TestLoad_MalformedYAML_ReturnsError(t *testing.T) {
	bundleDir := writeBundle(t, "this is: : : not: valid")
	if _, err := loadFromDir(bundleDir); err == nil {
		t.Fatal("Load: expected error on malformed YAML, got nil")
	}
}

func TestSupportedTypes_SortedAlphabetically(t *testing.T) {
	bundleDir := writeBundle(t, sampleMapping)
	doc, err := loadFromDir(bundleDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := doc.SupportedTypes()
	want := []string{"postgresql", "redis"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SupportedTypes() = %v, want %v", got, want)
	}
}

func TestSupportedTypes_NilDocReturnsNil(t *testing.T) {
	var doc *Document
	if got := doc.SupportedTypes(); got != nil {
		t.Errorf("nil-receiver SupportedTypes = %v, want nil", got)
	}
}

func TestHasType(t *testing.T) {
	bundleDir := writeBundle(t, sampleMapping)
	doc, _ := loadFromDir(bundleDir)
	if !doc.HasType("postgresql") {
		t.Error("HasType(postgresql) = false, want true")
	}
	if doc.HasType("oracle") {
		t.Error("HasType(oracle) = true, want false")
	}
	var nilDoc *Document
	if nilDoc.HasType("postgresql") {
		t.Error("nil-receiver HasType = true, want false")
	}
}

func TestFieldsForType_UnionsValuesMappingAndBindingSecretFromDSL(t *testing.T) {
	bundleDir := writeBundle(t, sampleMapping)
	doc, _ := loadFromDir(bundleDir)
	got := doc.FieldsForType("postgresql")
	want := []string{"auth.user.name", "auth.user.password"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FieldsForType(postgresql) = %v, want %v", got, want)
	}
}

func TestFieldsForType_UnknownType_ReturnsNil(t *testing.T) {
	bundleDir := writeBundle(t, sampleMapping)
	doc, _ := loadFromDir(bundleDir)
	if got := doc.FieldsForType("oracle"); got != nil {
		t.Errorf("FieldsForType(oracle) = %v, want nil", got)
	}
}

func TestValuesPathFor(t *testing.T) {
	bundleDir := writeBundle(t, sampleMapping)
	doc, _ := loadFromDir(bundleDir)
	if got := doc.ValuesPathFor("postgresql", "auth.user.password"); got != "postgresql.auth.password" {
		t.Errorf("ValuesPathFor(postgresql, auth.user.password) = %q, want postgresql.auth.password", got)
	}
	if got := doc.ValuesPathFor("postgresql", "nonexistent.field"); got != "" {
		t.Errorf("ValuesPathFor unknown field = %q, want empty", got)
	}
	if got := doc.ValuesPathFor("oracle", "auth.user.password"); got != "" {
		t.Errorf("ValuesPathFor unknown type = %q, want empty", got)
	}
}
