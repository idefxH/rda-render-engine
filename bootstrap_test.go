package render

import (
	"strings"
	"testing"
)

// TestBootstrapJobs_DexMigrationFlyway is the canonical case: a dex
// binding with a Flyway migration job that references the postgres
// binding's host/port/credentials via ${binding:NAME.field}.
func TestBootstrapJobs_DexMigrationFlyway(t *testing.T) {
	values := map[string]any{
		"suse-library": map[string]any{
			"services": []any{
				map[string]any{
					"binding": "state",
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
					"binding": "auth",
					"type":    "dex",
					"enabled": true,
					"bootstrap": map[string]any{
						"jobs": []any{
							map[string]any{
								"name":  "migrate",
								"image": "ghcr.io/myorg/payments:flyway-1.2.3",
								"env": map[string]any{
									"DB_HOST":     "${binding:state.host}",
									"DB_PORT":     "${binding:state.port}",
									"DB_USER":     "${binding:state.username}",
									"DB_PASSWORD": "${binding:state.password}",
									"SCHEMA":      "${binding:state.database}",
								},
								"command": []any{"flyway", "migrate"},
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
	jobs, ok := suse["bootstrap_jobs"].([]any)
	if !ok || len(jobs) != 1 {
		t.Fatalf("bootstrap_jobs = %v; want 1 entry", suse["bootstrap_jobs"])
	}
	entry := jobs[0].(map[string]any)
	if entry["binding"] != "auth" {
		t.Errorf("entry.binding = %v; want auth", entry["binding"])
	}
	if entry["type"] != "dex" {
		t.Errorf("entry.type = %v; want dex", entry["type"])
	}
	job := entry["job"].(map[string]any)
	env := job["env"].(map[string]any)

	checks := map[string]string{
		"DB_HOST":     "payments-postgresql.{{ .Release.Namespace }}.svc.cluster.local",
		"DB_PORT":     "5432",
		"DB_USER":     "dex",
		"DB_PASSWORD": "s3cret",
		"SCHEMA":      "dex",
	}
	for k, want := range checks {
		if got := env[k]; got != want {
			t.Errorf("env[%s] = %v; want %v", k, got, want)
		}
	}
}

// TestBootstrapJobs_BindingSelfRef verifies ${binding-self:...}
// resolution inside a bootstrap job. Useful when the migration job
// targets the very same binding it lives under.
func TestBootstrapJobs_BindingSelfRef(t *testing.T) {
	values := map[string]any{
		"suse-library": map[string]any{
			"services": []any{
				map[string]any{
					"binding": "appdb",
					"type":    "postgresql",
					"enabled": true,
					"auth": map[string]any{
						"user": map[string]any{
							"name":     "app",
							"password": "pw",
							"database": "appdb",
						},
					},
					"bootstrap": map[string]any{
						"jobs": []any{
							map[string]any{
								"name":  "init-schema",
								"image": "ghcr.io/myorg/migrations:0.0.1",
								"env": map[string]any{
									"PGHOST":     "${binding-self:host}",
									"PGUSER":     "${binding-self:username}",
									"PGPASSWORD": "${binding-self:password}",
									"PGDATABASE": "${binding-self:database}",
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
	jobs := res.Overlay["suse-library"].(map[string]any)["bootstrap_jobs"].([]any)
	job := jobs[0].(map[string]any)["job"].(map[string]any)
	env := job["env"].(map[string]any)
	if got := env["PGHOST"]; got != "payments-postgresql.{{ .Release.Namespace }}.svc.cluster.local" {
		t.Errorf("PGHOST = %v", got)
	}
	if got := env["PGUSER"]; got != "app" {
		t.Errorf("PGUSER = %v; want app", got)
	}
	if got := env["PGPASSWORD"]; got != "pw" {
		t.Errorf("PGPASSWORD = %v; want pw", got)
	}
	if got := env["PGDATABASE"]; got != "appdb" {
		t.Errorf("PGDATABASE = %v; want appdb", got)
	}
}

// TestBootstrapJobs_DisabledServiceSkipped — disabled services'
// bootstrap.jobs MUST NOT project. They're inert until the dev flips
// enabled: true (matches the projection contract for values_mapping).
func TestBootstrapJobs_DisabledServiceSkipped(t *testing.T) {
	values := map[string]any{
		"suse-library": map[string]any{
			"services": []any{
				map[string]any{
					"binding": "db",
					"type":    "postgresql",
					"enabled": false,
					"bootstrap": map[string]any{
						"jobs": []any{
							map[string]any{
								"name":  "migrate",
								"image": "img",
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
	suse, _ := res.Overlay["suse-library"].(map[string]any)
	if jobs, present := suse["bootstrap_jobs"]; present {
		t.Errorf("bootstrap_jobs present for disabled service: %v", jobs)
	}
}

// TestBootstrapJobs_SharedProvisioningSkipped — shared/external
// bindings rely on credentials managed elsewhere; running post-install
// Jobs on the dev cluster against them doesn't make sense.
func TestBootstrapJobs_SharedProvisioningSkipped(t *testing.T) {
	values := map[string]any{
		"suse-library": map[string]any{
			"services": []any{
				map[string]any{
					"binding":      "vault",
					"type":         "postgresql", // doesn't matter for the test
					"enabled":      true,
					"provisioning": "shared",
					"bootstrap": map[string]any{
						"jobs": []any{
							map[string]any{
								"name":  "init",
								"image": "img",
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
	suse, _ := res.Overlay["suse-library"].(map[string]any)
	if jobs, present := suse["bootstrap_jobs"]; present {
		t.Errorf("bootstrap_jobs present for shared service: %v", jobs)
	}
}

// TestBootstrapJobs_UnknownBindingFailsLoud — a typo in a cross-binding
// reference inside a bootstrap job must surface at render time, not
// silently produce a Job that fails at runtime.
func TestBootstrapJobs_UnknownBindingFailsLoud(t *testing.T) {
	values := map[string]any{
		"suse-library": map[string]any{
			"services": []any{
				map[string]any{
					"binding": "auth",
					"type":    "dex",
					"enabled": true,
					"bootstrap": map[string]any{
						"jobs": []any{
							map[string]any{
								"name":  "migrate",
								"image": "img",
								"env": map[string]any{
									"DB_HOST": "${binding:nonexistent.host}",
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
		t.Fatal("expected error on unknown binding ref in bootstrap.jobs[]")
	}
	if !strings.Contains(err.Error(), "unknown binding") {
		t.Errorf("error = %q; want it to mention 'unknown binding'", err.Error())
	}
}
