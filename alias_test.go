package render

import (
	"testing"
)

func TestComputeAliases_SingleInstance(t *testing.T) {
	services := []map[string]any{
		{"binding": "db", "type": "postgresql"},
		{"binding": "cache", "type": "redis"},
	}
	aliases := ComputeAliases(services)

	if aliases["db"] != "postgresql" {
		t.Errorf("expected alias 'postgresql' for binding 'db', got %q", aliases["db"])
	}
	if aliases["cache"] != "redis" {
		t.Errorf("expected alias 'redis' for binding 'cache', got %q", aliases["cache"])
	}
}

func TestComputeAliases_MultiInstance(t *testing.T) {
	services := []map[string]any{
		{"binding": "payments-db", "type": "postgresql"},
		{"binding": "users-db", "type": "postgresql"},
		{"binding": "cache", "type": "redis"},
	}
	aliases := ComputeAliases(services)

	if aliases["payments-db"] != "postgresql-payments-db" {
		t.Errorf("expected 'postgresql-payments-db', got %q", aliases["payments-db"])
	}
	if aliases["users-db"] != "postgresql-users-db" {
		t.Errorf("expected 'postgresql-users-db', got %q", aliases["users-db"])
	}
	if aliases["cache"] != "redis" {
		t.Errorf("single-instance redis should stay 'redis', got %q", aliases["cache"])
	}
}

func TestComputeAliases_TripleInstance(t *testing.T) {
	services := []map[string]any{
		{"binding": "a", "type": "grafana"},
		{"binding": "b", "type": "grafana"},
		{"binding": "c", "type": "grafana"},
	}
	aliases := ComputeAliases(services)

	if aliases["a"] != "grafana-a" {
		t.Errorf("expected 'grafana-a', got %q", aliases["a"])
	}
	if aliases["b"] != "grafana-b" {
		t.Errorf("expected 'grafana-b', got %q", aliases["b"])
	}
	if aliases["c"] != "grafana-c" {
		t.Errorf("expected 'grafana-c', got %q", aliases["c"])
	}
}

func TestComputeAliases_SkipsEmpty(t *testing.T) {
	services := []map[string]any{
		{"binding": "", "type": "postgresql"},
		{"binding": "db", "type": ""},
		{"type": "redis"},
	}
	aliases := ComputeAliases(services)
	if len(aliases) != 0 {
		t.Errorf("expected empty aliases for entries with missing binding/type, got %v", aliases)
	}
}

func TestMultiInstanceTypes(t *testing.T) {
	services := []map[string]any{
		{"binding": "a", "type": "postgresql"},
		{"binding": "b", "type": "postgresql"},
		{"binding": "c", "type": "redis"},
	}
	multi := MultiInstanceTypes(services)
	if !multi["postgresql"] {
		t.Error("postgresql should be multi-instance")
	}
	if multi["redis"] {
		t.Error("redis should NOT be multi-instance")
	}
}

func TestAliasedPath(t *testing.T) {
	tests := []struct {
		path, chartType, alias, want string
	}{
		{"postgresql.auth.username", "postgresql", "postgresql-payments-db", "postgresql-payments-db.auth.username"},
		{"postgresql.auth.username", "postgresql", "postgresql", "postgresql.auth.username"},
		{"postgresql", "postgresql", "postgresql-db", "postgresql-db"},
		{"grafana.sidecar.dashboards.enabled", "grafana", "grafana-metrics", "grafana-metrics.sidecar.dashboards.enabled"},
		{"unrelated.path.here", "postgresql", "postgresql-db", "unrelated.path.here"},
	}
	for _, tt := range tests {
		got := AliasedPath(tt.path, tt.chartType, tt.alias)
		if got != tt.want {
			t.Errorf("AliasedPath(%q, %q, %q) = %q, want %q",
				tt.path, tt.chartType, tt.alias, got, tt.want)
		}
	}
}

func TestAliasedHost(t *testing.T) {
	tests := []struct {
		host, chartType, alias, want string
	}{
		{
			"{{ .Release.Name }}-postgresql.{{ .Release.Namespace }}.svc.cluster.local",
			"postgresql", "postgresql-payments-db",
			"{{ .Release.Name }}-postgresql-payments-db.{{ .Release.Namespace }}.svc.cluster.local",
		},
		{
			"{{ .Release.Name }}-postgresql.{{ .Release.Namespace }}.svc.cluster.local",
			"postgresql", "postgresql",
			"{{ .Release.Name }}-postgresql.{{ .Release.Namespace }}.svc.cluster.local",
		},
		{
			"{{ .Release.Name }}-grafana.{{ .Release.Namespace }}.svc.cluster.local",
			"grafana", "grafana-metrics",
			"{{ .Release.Name }}-grafana-metrics.{{ .Release.Namespace }}.svc.cluster.local",
		},
	}
	for _, tt := range tests {
		got := AliasedHost(tt.host, tt.chartType, tt.alias)
		if got != tt.want {
			t.Errorf("AliasedHost(..., %q, %q) = %q, want %q",
				tt.chartType, tt.alias, got, tt.want)
		}
	}
}
