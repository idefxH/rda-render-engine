package render

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// chartYAML is a minimal Chart.yaml with postgresql and redis dependencies.
const chartYAML = `apiVersion: v2
name: test-chart
version: 0.1.0
dependencies:
  - name: postgresql
    version: "16.4.3"
    repository: "oci://dp.apps.rancher.io/charts"
    condition: postgresql.enabled
  - name: redis
    version: "20.6.2"
    repository: "oci://dp.apps.rancher.io/charts"
    condition: redis.enabled
`

// chartYAMLWithExistingAlias has dex-idp aliased as dex (the bundle's canonical pattern).
const chartYAMLWithExistingAlias = `apiVersion: v2
name: test-chart
version: 0.1.0
dependencies:
  - name: postgresql
    version: "16.4.3"
    repository: "oci://dp.apps.rancher.io/charts"
    condition: postgresql.enabled
  - name: dex-idp
    version: "0.19.1"
    repository: "oci://dp.apps.rancher.io/charts"
    condition: dex.enabled
    alias: dex
`

// writeChartYAML writes content to a temp Chart.yaml and returns the path.
func writeChartYAML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "Chart.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp Chart.yaml: %v", err)
	}
	return path
}

// readDeps reads a Chart.yaml and returns the parsed dependency slice.
func readDeps(t *testing.T, path string) []ChartDep {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Chart.yaml: %v", err)
	}
	var chart struct {
		Dependencies []ChartDep `yaml:"dependencies"`
	}
	if err := yaml.Unmarshal(data, &chart); err != nil {
		t.Fatalf("parse Chart.yaml: %v", err)
	}
	return chart.Dependencies
}

// findDep returns the first dependency with the given effective name (alias or name).
func findDep(deps []ChartDep, effectiveName string) *ChartDep {
	for i := range deps {
		ename := deps[i].Alias
		if ename == "" {
			ename = deps[i].Name
		}
		if ename == effectiveName {
			return &deps[i]
		}
	}
	return nil
}

func TestPatchChartDeps_SingleInstance_NoChange(t *testing.T) {
	path := writeChartYAML(t, chartYAML)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Single-instance aliases: no multi-instance types.
	aliases := map[string]string{
		"db":    "postgresql",
		"cache": "redis",
	}

	injected, err := PatchChartDeps(path, aliases)
	if err != nil {
		t.Fatalf("PatchChartDeps: %v", err)
	}
	if injected != 0 {
		t.Errorf("expected 0 injected aliases, got %d", injected)
	}

	// File should not have been modified at all.
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Error("file was modified despite no multi-instance types")
	}
}

func TestPatchChartDeps_MultiInstance_CreatesAliases(t *testing.T) {
	path := writeChartYAML(t, chartYAML)

	// Two postgresql bindings -> multi-instance.
	aliases := map[string]string{
		"db1":   "postgresql-db1",
		"db2":   "postgresql-db2",
		"cache": "redis",
	}

	injected, err := PatchChartDeps(path, aliases)
	if err != nil {
		t.Fatalf("PatchChartDeps: %v", err)
	}
	if injected != 2 {
		t.Errorf("expected 2 injected aliases, got %d", injected)
	}

	deps := readDeps(t, path)

	// Should have 3 deps: postgresql-db1, postgresql-db2, redis.
	if len(deps) != 3 {
		t.Fatalf("expected 3 deps, got %d: %+v", len(deps), deps)
	}

	// Bare postgresql dep should be gone.
	if d := findDep(deps, "postgresql"); d != nil {
		t.Error("bare 'postgresql' dep should have been replaced")
	}

	// Check postgresql-db1.
	d1 := findDep(deps, "postgresql-db1")
	if d1 == nil {
		t.Fatal("missing postgresql-db1 dep")
	}
	if d1.Name != "postgresql" {
		t.Errorf("postgresql-db1.name = %q, want 'postgresql'", d1.Name)
	}
	if d1.Alias != "postgresql-db1" {
		t.Errorf("postgresql-db1.alias = %q, want 'postgresql-db1'", d1.Alias)
	}
	if d1.Condition != "postgresql-db1.enabled" {
		t.Errorf("postgresql-db1.condition = %q, want 'postgresql-db1.enabled'", d1.Condition)
	}
	if d1.Version != "16.4.3" {
		t.Errorf("postgresql-db1.version = %q, want '16.4.3'", d1.Version)
	}

	// Check postgresql-db2.
	d2 := findDep(deps, "postgresql-db2")
	if d2 == nil {
		t.Fatal("missing postgresql-db2 dep")
	}
	if d2.Name != "postgresql" {
		t.Errorf("postgresql-db2.name = %q, want 'postgresql'", d2.Name)
	}
	if d2.Alias != "postgresql-db2" {
		t.Errorf("postgresql-db2.alias = %q, want 'postgresql-db2'", d2.Alias)
	}
	if d2.Condition != "postgresql-db2.enabled" {
		t.Errorf("postgresql-db2.condition = %q, want 'postgresql-db2.enabled'", d2.Condition)
	}

	// Redis should remain unchanged.
	rd := findDep(deps, "redis")
	if rd == nil {
		t.Fatal("missing redis dep")
	}
	if rd.Alias != "" {
		t.Errorf("redis should have no alias, got %q", rd.Alias)
	}
}

func TestPatchChartDeps_Idempotent(t *testing.T) {
	path := writeChartYAML(t, chartYAML)

	aliases := map[string]string{
		"db1":   "postgresql-db1",
		"db2":   "postgresql-db2",
		"cache": "redis",
	}

	// First run.
	n1, err := PatchChartDeps(path, aliases)
	if err != nil {
		t.Fatalf("first PatchChartDeps: %v", err)
	}
	after1, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Second run on already-patched file.
	_, err = PatchChartDeps(path, aliases)
	if err != nil {
		t.Fatalf("second PatchChartDeps: %v", err)
	}
	after2, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if n1 != 2 {
		t.Errorf("first run: expected 2 injected, got %d", n1)
	}
	// Second run should produce no new injections or the same result.
	// The function is idempotent: running on already-aliased deps should
	// not duplicate entries.
	if string(after1) != string(after2) {
		t.Error("second run changed the file — not idempotent")
	}

	// Verify dep count is still 3.
	deps := readDeps(t, path)
	if len(deps) != 3 {
		t.Errorf("expected 3 deps after second run, got %d", len(deps))
	}
}

func TestPatchChartDeps_MixedTypes(t *testing.T) {
	path := writeChartYAML(t, chartYAML)

	// Multi-instance postgresql + single-instance redis.
	aliases := map[string]string{
		"primary-db": "postgresql-primary-db",
		"events-db":  "postgresql-events-db",
		"cache":      "redis",
	}

	injected, err := PatchChartDeps(path, aliases)
	if err != nil {
		t.Fatalf("PatchChartDeps: %v", err)
	}
	if injected != 2 {
		t.Errorf("expected 2 injected aliases, got %d", injected)
	}

	deps := readDeps(t, path)

	// Expect: postgresql-events-db, postgresql-primary-db (sorted), redis.
	if len(deps) != 3 {
		t.Fatalf("expected 3 deps, got %d: %+v", len(deps), deps)
	}

	// postgresql aliased entries.
	pdb := findDep(deps, "postgresql-primary-db")
	if pdb == nil {
		t.Fatal("missing postgresql-primary-db")
	}
	if pdb.Name != "postgresql" {
		t.Errorf("postgresql-primary-db.name = %q, want 'postgresql'", pdb.Name)
	}

	edb := findDep(deps, "postgresql-events-db")
	if edb == nil {
		t.Fatal("missing postgresql-events-db")
	}
	if edb.Name != "postgresql" {
		t.Errorf("postgresql-events-db.name = %q, want 'postgresql'", edb.Name)
	}

	// redis stays single-instance.
	rd := findDep(deps, "redis")
	if rd == nil {
		t.Fatal("missing redis dep")
	}
	if rd.Alias != "" {
		t.Errorf("redis should have no alias, got %q", rd.Alias)
	}
	if rd.Condition != "redis.enabled" {
		t.Errorf("redis.condition = %q, want 'redis.enabled'", rd.Condition)
	}
}

func TestPatchChartDeps_PreservesExistingAliases(t *testing.T) {
	path := writeChartYAML(t, chartYAMLWithExistingAlias)

	// Single-instance aliases: dex-idp aliased as dex, postgresql single.
	aliases := map[string]string{
		"db":   "postgresql",
		"auth": "dex",
	}

	injected, err := PatchChartDeps(path, aliases)
	if err != nil {
		t.Fatalf("PatchChartDeps: %v", err)
	}
	if injected != 0 {
		t.Errorf("expected 0 injected (no multi-instance), got %d", injected)
	}

	deps := readDeps(t, path)
	if len(deps) != 2 {
		t.Fatalf("expected 2 deps, got %d: %+v", len(deps), deps)
	}

	// dex-idp with alias "dex" should be preserved.
	dex := findDep(deps, "dex")
	if dex == nil {
		t.Fatal("missing dex (alias of dex-idp)")
	}
	if dex.Name != "dex-idp" {
		t.Errorf("dex.name = %q, want 'dex-idp'", dex.Name)
	}
	if dex.Alias != "dex" {
		t.Errorf("dex.alias = %q, want 'dex'", dex.Alias)
	}
	if dex.Condition != "dex.enabled" {
		t.Errorf("dex.condition = %q, want 'dex.enabled'", dex.Condition)
	}

	// postgresql should be preserved as-is.
	pg := findDep(deps, "postgresql")
	if pg == nil {
		t.Fatal("missing postgresql dep")
	}
	if pg.Alias != "" {
		t.Errorf("postgresql should have no alias, got %q", pg.Alias)
	}
}
