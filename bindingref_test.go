package render

import (
	"strings"
	"testing"

	"github.com/idefxH/rda-render-engine/dslmapping"
)

// crossBindingMappings — minimal catalogue with postgresql + dex that
// fits the common cross-binding case: dex's storage backend is
// postgres, address resolved via ${binding:postgres-state.host}.
func crossBindingMappings() *dslmapping.Document {
	return &dslmapping.Document{
		APIVersion: "rda.suse.com/dsl-mapping/v1alpha1",
		Charts: map[string]dslmapping.ChartEntry{
			"postgresql": {
				Versions: []dslmapping.VersionEntry{
					{
						Constraint: ">=0.4.0 <1.0.0",
						Service: dslmapping.ServiceSpec{
							Host: "{{ .Release.Name }}-postgresql.{{ .Release.Namespace }}.svc.cluster.local",
							Port: 5432,
						},
						ValuesMapping: map[string]string{
							"auth.user.name":     "postgresql.auth.username",
							"auth.user.password": "postgresql.auth.password",
							"auth.user.database": "postgresql.auth.database",
						},
					},
				},
			},
			"dex": {
				Versions: []dslmapping.VersionEntry{
					{
						Constraint: ">=0.24.0 <1.0.0",
						Service: dslmapping.ServiceSpec{
							Host: "{{ .Release.Name }}-dex.{{ .Release.Namespace }}.svc.cluster.local",
							Port: 5556,
						},
						ValuesMapping: map[string]string{
							"issuer": "dex.config.issuer",
						},
					},
				},
			},
		},
	}
}

func TestCrossBinding_DexStoragePostgres(t *testing.T) {
	values := map[string]any{
		"suse-library": map[string]any{
			"services": []any{
				map[string]any{
					"binding": "postgres-state",
					"type":    "postgresql",
					"enabled": true,
					"auth": map[string]any{
						"user": map[string]any{
							"name":     "dex",
							"password": "s3cret",
							"database": "dex",
						},
					},
				},
				map[string]any{
					"binding": "corp-sso",
					"type":    "dex",
					"enabled": true,
					"issuer":  "http://payments-dex.payments.svc.cluster.local:5556",
					"passthrough": map[string]any{
						"config": map[string]any{
							"storage": map[string]any{
								"type": "postgres",
								"config": map[string]any{
									"host":     "${binding:postgres-state.host}",
									"port":     "${binding:postgres-state.port}",
									"database": "${binding:postgres-state.database}",
									"user":     "${binding:postgres-state.username}",
									"password": "${binding:postgres-state.password}",
								},
							},
						},
					},
				},
			},
		},
	}

	res, err := Project(values, crossBindingMappings(), "payments")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	suse := res.Overlay["suse-library"].(map[string]any)
	dex := suse["dex"].(map[string]any)
	cfg := dex["config"].(map[string]any)
	storage := cfg["storage"].(map[string]any)
	storageCfg := storage["config"].(map[string]any)

	if got := storageCfg["host"]; got != "payments-postgresql.{{ .Release.Namespace }}.svc.cluster.local" {
		t.Errorf("host = %v; want payments-postgresql.{{ .Release.Namespace }}.svc.cluster.local", got)
	}
	if got := storageCfg["port"]; got != "5432" {
		t.Errorf("port = %v; want 5432", got)
	}
	if got := storageCfg["database"]; got != "dex" {
		t.Errorf("database = %v; want dex", got)
	}
	if got := storageCfg["user"]; got != "dex" {
		t.Errorf("user = %v; want dex", got)
	}
	if got := storageCfg["password"]; got != "s3cret" {
		t.Errorf("password = %v; want s3cret", got)
	}
}

func TestCrossBinding_BindingSelf(t *testing.T) {
	values := map[string]any{
		"suse-library": map[string]any{
			"services": []any{
				map[string]any{
					"binding": "events-db",
					"type":    "postgresql",
					"enabled": true,
					"auth": map[string]any{
						"user": map[string]any{
							"name": "events",
						},
					},
					"passthrough": map[string]any{
						"customField": "self-host=${binding-self:host}",
					},
				},
			},
		},
	}
	res, err := Project(values, crossBindingMappings(), "payments")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pg := res.Overlay["suse-library"].(map[string]any)["postgresql"].(map[string]any)
	got := pg["customField"].(string)
	want := "self-host=payments-postgresql.{{ .Release.Namespace }}.svc.cluster.local"
	if got != want {
		t.Errorf("customField = %q; want %q", got, want)
	}
}

func TestCrossBinding_UnknownBinding_Fails(t *testing.T) {
	values := map[string]any{
		"suse-library": map[string]any{
			"services": []any{
				map[string]any{
					"binding": "corp-sso",
					"type":    "dex",
					"enabled": true,
					"passthrough": map[string]any{
						"config": map[string]any{
							"storage": map[string]any{
								"config": map[string]any{
									"host": "${binding:nonexistent.host}",
								},
							},
						},
					},
				},
			},
		},
	}
	_, err := Project(values, crossBindingMappings(), "payments")
	if err == nil {
		t.Fatal("expected error on unknown binding reference, got nil")
	}
	if !strings.Contains(err.Error(), "unknown binding") {
		t.Errorf("error = %q; want it to mention 'unknown binding'", err.Error())
	}
}

func TestCrossBinding_UnknownField_Fails(t *testing.T) {
	values := map[string]any{
		"suse-library": map[string]any{
			"services": []any{
				map[string]any{
					"binding": "db",
					"type":    "postgresql",
					"enabled": true,
				},
				map[string]any{
					"binding": "corp-sso",
					"type":    "dex",
					"enabled": true,
					"passthrough": map[string]any{
						"customField": "${binding:db.bogus_field}",
					},
				},
			},
		},
	}
	_, err := Project(values, crossBindingMappings(), "payments")
	if err == nil {
		t.Fatal("expected error on unknown field reference, got nil")
	}
	if !strings.Contains(err.Error(), "unknown field") {
		t.Errorf("error = %q; want it to mention 'unknown field'", err.Error())
	}
}

// TestCrossBinding_MultiPort verifies that named ports surface as
// `<port_name>_port` and `<port_name>_url` cross-binding fields. The
// canonical case is minio (s3 + console) and dex (http + grpc).
func TestCrossBinding_MultiPort(t *testing.T) {
	mappings := &dslmapping.Document{
		APIVersion: "rda.suse.com/dsl-mapping/v1alpha1",
		Charts: map[string]dslmapping.ChartEntry{
			"minio": {
				Versions: []dslmapping.VersionEntry{
					{
						Constraint: ">=5.0.0 <6.0.0",
						Service: dslmapping.ServiceSpec{
							Host: "{{ .Release.Name }}-minio.{{ .Release.Namespace }}.svc.cluster.local",
							Ports: map[string]dslmapping.ServicePort{
								"s3":      {Port: 9000, Scheme: "http", Primary: true},
								"console": {Port: 9001, Scheme: "http"},
							},
						},
					},
				},
			},
			"app": {
				Versions: []dslmapping.VersionEntry{
					{
						Constraint: ">=0.0.0",
						Service: dslmapping.ServiceSpec{
							Host: "{{ .Release.Name }}-app.{{ .Release.Namespace }}.svc.cluster.local",
							Port: 8080,
						},
					},
				},
			},
		},
	}
	values := map[string]any{
		"suse-library": map[string]any{
			"services": []any{
				map[string]any{
					"binding": "buckets",
					"type":    "minio",
					"enabled": true,
				},
				map[string]any{
					"binding": "ui",
					"type":    "app",
					"enabled": true,
					"passthrough": map[string]any{
						"env": map[string]any{
							"S3_URL":      "${binding:buckets.s3_url}",
							"CONSOLE_URL": "${binding:buckets.console_url}",
							"S3_PORT":     "${binding:buckets.s3_port}",
							// Primary alias still works
							"PRIMARY_HOST": "${binding:buckets.host}",
							"PRIMARY_PORT": "${binding:buckets.port}",
						},
					},
				},
			},
		},
	}
	res, err := Project(values, mappings, "myrelease")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	app := res.Overlay["suse-library"].(map[string]any)["app"].(map[string]any)
	env := app["env"].(map[string]any)
	checks := map[string]string{
		"S3_URL":       "http://myrelease-minio.{{ .Release.Namespace }}.svc.cluster.local:9000",
		"CONSOLE_URL":  "http://myrelease-minio.{{ .Release.Namespace }}.svc.cluster.local:9001",
		"S3_PORT":      "9000",
		"PRIMARY_HOST": "myrelease-minio.{{ .Release.Namespace }}.svc.cluster.local",
		"PRIMARY_PORT": "9000",
	}
	for k, want := range checks {
		if got := env[k]; got != want {
			t.Errorf("env[%s] = %v; want %v", k, got, want)
		}
	}
}

// TestCrossBinding_LegacySinglePortBackcompat verifies that a chart
// declaring the legacy `service.port: <int>` shape is normalised to
// a single primary port keyed `default` — so existing entries keep
// working unchanged.
func TestCrossBinding_LegacySinglePortBackcompat(t *testing.T) {
	values := map[string]any{
		"suse-library": map[string]any{
			"services": []any{
				map[string]any{
					"binding": "db",
					"type":    "postgresql",
					"enabled": true,
				},
				map[string]any{
					"binding": "app",
					"type":    "dex",
					"enabled": true,
					"passthrough": map[string]any{
						"primary":     "${binding:db.url}",
						"named":       "${binding:db.default_url}",
						"primaryPort": "${binding:db.port}",
					},
				},
			},
		},
	}
	res, err := Project(values, crossBindingMappings(), "myrelease")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	dex := res.Overlay["suse-library"].(map[string]any)["dex"].(map[string]any)
	want := "http://myrelease-postgresql.{{ .Release.Namespace }}.svc.cluster.local:5432"
	if got := dex["primary"]; got != want {
		t.Errorf("primary = %v; want %v", got, want)
	}
	if got := dex["named"]; got != want {
		t.Errorf("named (default_url) = %v; want %v", got, want)
	}
	if got := dex["primaryPort"]; got != "5432" {
		t.Errorf("primaryPort = %v; want 5432", got)
	}
}

func TestCrossBinding_NoRefsIsNoOp(t *testing.T) {
	// Plain passthrough without ${binding:...} refs should still work
	// (regression guard for the resolveBindingRefs hook).
	values := map[string]any{
		"suse-library": map[string]any{
			"services": []any{
				map[string]any{
					"binding": "corp-sso",
					"type":    "dex",
					"enabled": true,
					"passthrough": map[string]any{
						"config": map[string]any{
							"enablePasswordDB": true,
							"storage":          map[string]any{"type": "memory"},
						},
					},
				},
			},
		},
	}
	res, err := Project(values, crossBindingMappings(), "payments")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	dex := res.Overlay["suse-library"].(map[string]any)["dex"].(map[string]any)
	cfg := dex["config"].(map[string]any)
	if got := cfg["enablePasswordDB"]; got != true {
		t.Errorf("enablePasswordDB = %v; want true", got)
	}
	storage := cfg["storage"].(map[string]any)
	if got := storage["type"]; got != "memory" {
		t.Errorf("storage.type = %v; want memory", got)
	}
}
