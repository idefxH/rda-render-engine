package render

import (
	"strings"
	"testing"

	"github.com/idefxH/rda-render-engine/dslmapping"
)

// ── Unit tests for workloads.go (shape resolution & validation) ──

func TestResolveWorkloads_SingleWebShape(t *testing.T) {
	suse := map[string]any{
		"workloads": []any{
			map[string]any{
				"name":  "api",
				"shape": "web",
				"image": map[string]any{"repository": "myapp-api", "tag": "dev"},
				"port":  9090, // override shape default of 8080
			},
		},
	}
	resolved, err := resolveWorkloads(suse)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resolved) != 1 {
		t.Fatalf("expected 1 workload, got %d", len(resolved))
	}
	w := resolved[0]
	if w["name"] != "api" {
		t.Errorf("name = %v; want api", w["name"])
	}
	if w["kind"] != "Deployment" {
		t.Errorf("kind = %v; want Deployment (from web shape)", w["kind"])
	}
	if w["port"] != 9090 {
		t.Errorf("port = %v; want 9090 (explicit override of shape default 8080)", w["port"])
	}
	if w["replicas"] != 1 {
		t.Errorf("replicas = %v; want 1 (from web shape default)", w["replicas"])
	}
	// Probes should come from web shape
	probes, ok := w["probes"].(map[string]any)
	if !ok {
		t.Fatal("probes should be a map from web shape defaults")
	}
	if _, hasLiveness := probes["liveness"]; !hasLiveness {
		t.Error("web shape should provide liveness probe")
	}
	if _, hasReadiness := probes["readiness"]; !hasReadiness {
		t.Error("web shape should provide readiness probe")
	}
	// Resources from web shape
	resources, ok := w["resources"].(map[string]any)
	if !ok {
		t.Fatal("resources should be a map from web shape defaults")
	}
	requests := resources["requests"].(map[string]any)
	if requests["cpu"] != "100m" {
		t.Errorf("resources.requests.cpu = %v; want 100m", requests["cpu"])
	}
}

func TestResolveWorkloads_WorkerShape_NullProbes(t *testing.T) {
	suse := map[string]any{
		"workloads": []any{
			map[string]any{
				"name":  "worker",
				"shape": "worker",
				"image": map[string]any{"repository": "myapp-worker", "tag": "dev"},
			},
		},
	}
	resolved, err := resolveWorkloads(suse)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	w := resolved[0]
	if w["kind"] != "Deployment" {
		t.Errorf("kind = %v; want Deployment", w["kind"])
	}
	// Worker shape sets probes to nil
	if w["probes"] != nil {
		t.Errorf("probes = %v; want nil (worker shape)", w["probes"])
	}
}

func TestResolveWorkloads_CronShape(t *testing.T) {
	suse := map[string]any{
		"workloads": []any{
			map[string]any{
				"name":     "scheduler",
				"shape":    "cron",
				"image":    map[string]any{"repository": "myapp-scheduler", "tag": "dev"},
				"schedule": "*/15 * * * *",
			},
		},
	}
	resolved, err := resolveWorkloads(suse)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	w := resolved[0]
	if w["kind"] != "CronJob" {
		t.Errorf("kind = %v; want CronJob (from cron shape)", w["kind"])
	}
	if w["restartPolicy"] != "OnFailure" {
		t.Errorf("restartPolicy = %v; want OnFailure (from cron shape)", w["restartPolicy"])
	}
	if w["schedule"] != "*/15 * * * *" {
		t.Errorf("schedule = %v; want */15 * * * *", w["schedule"])
	}
}

func TestResolveWorkloads_DaemonShape(t *testing.T) {
	suse := map[string]any{
		"workloads": []any{
			map[string]any{
				"name":  "agent",
				"shape": "daemon",
				"image": map[string]any{"repository": "myapp-agent", "tag": "dev"},
			},
		},
	}
	resolved, err := resolveWorkloads(suse)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	w := resolved[0]
	if w["kind"] != "DaemonSet" {
		t.Errorf("kind = %v; want DaemonSet", w["kind"])
	}
}

func TestResolveWorkloads_NoShape_BareDefaults(t *testing.T) {
	suse := map[string]any{
		"workloads": []any{
			map[string]any{
				"name":  "app",
				"image": map[string]any{"repository": "myapp", "tag": "dev"},
				"port":  3000,
			},
		},
	}
	resolved, err := resolveWorkloads(suse)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	w := resolved[0]
	if w["kind"] != "Deployment" {
		t.Errorf("kind = %v; want Deployment (bare default)", w["kind"])
	}
	if w["replicas"] != 1 {
		t.Errorf("replicas = %v; want 1 (bare default)", w["replicas"])
	}
	if w["port"] != 3000 {
		t.Errorf("port = %v; want 3000 (explicit)", w["port"])
	}
}

func TestResolveWorkloads_ShapeDefaultsOverridable(t *testing.T) {
	suse := map[string]any{
		"workloads": []any{
			map[string]any{
				"name":     "api",
				"shape":    "web",
				"image":    map[string]any{"repository": "myapp-api", "tag": "dev"},
				"replicas": 5,
				"resources": map[string]any{
					"requests": map[string]any{"cpu": "500m", "memory": "256Mi"},
					// limits NOT specified — should inherit from shape
				},
			},
		},
	}
	resolved, err := resolveWorkloads(suse)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	w := resolved[0]
	if w["replicas"] != 5 {
		t.Errorf("replicas = %v; want 5 (explicit override)", w["replicas"])
	}
	resources := w["resources"].(map[string]any)
	requests := resources["requests"].(map[string]any)
	if requests["cpu"] != "500m" {
		t.Errorf("resources.requests.cpu = %v; want 500m (explicit override)", requests["cpu"])
	}
	if requests["memory"] != "256Mi" {
		t.Errorf("resources.requests.memory = %v; want 256Mi (explicit override)", requests["memory"])
	}
	// limits should come from shape default
	limits := resources["limits"].(map[string]any)
	if limits["memory"] != "512Mi" {
		t.Errorf("resources.limits.memory = %v; want 512Mi (from web shape)", limits["memory"])
	}
}

func TestResolveWorkloads_MissingName(t *testing.T) {
	suse := map[string]any{
		"workloads": []any{
			map[string]any{
				"image": map[string]any{"repository": "myapp", "tag": "dev"},
			},
		},
	}
	_, err := resolveWorkloads(suse)
	if err == nil {
		t.Fatal("expected error for missing name")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Errorf("error should mention 'name': %v", err)
	}
}

func TestResolveWorkloads_MissingImage(t *testing.T) {
	suse := map[string]any{
		"workloads": []any{
			map[string]any{
				"name": "api",
			},
		},
	}
	_, err := resolveWorkloads(suse)
	if err == nil {
		t.Fatal("expected error for missing image")
	}
	if !strings.Contains(err.Error(), "image") {
		t.Errorf("error should mention 'image': %v", err)
	}
}

func TestResolveWorkloads_DuplicateName(t *testing.T) {
	suse := map[string]any{
		"workloads": []any{
			map[string]any{"name": "api", "image": map[string]any{"repository": "a"}},
			map[string]any{"name": "api", "image": map[string]any{"repository": "b"}},
		},
	}
	_, err := resolveWorkloads(suse)
	if err == nil {
		t.Fatal("expected error for duplicate name")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error should mention 'duplicate': %v", err)
	}
}

func TestResolveWorkloads_UnknownShape(t *testing.T) {
	suse := map[string]any{
		"workloads": []any{
			map[string]any{
				"name":  "api",
				"shape": "nonexistent",
				"image": map[string]any{"repository": "myapp"},
			},
		},
	}
	_, err := resolveWorkloads(suse)
	if err == nil {
		t.Fatal("expected error for unknown shape")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error should mention the unknown shape: %v", err)
	}
}

func TestResolveWorkloads_EmptyBlock(t *testing.T) {
	suse := map[string]any{}
	resolved, err := resolveWorkloads(suse)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved != nil {
		t.Errorf("expected nil for empty workloads, got %v", resolved)
	}
}

// ── Integration tests: ProjectWithStage + workloads[] ──

// valuesWithWorkloads builds a project-shape values map with both
// services[] and workloads[].
func valuesWithWorkloads(services []map[string]any, workloads []map[string]any) map[string]any {
	servicesAny := make([]any, len(services))
	for i, s := range services {
		servicesAny[i] = s
	}
	workloadsAny := make([]any, len(workloads))
	for i, w := range workloads {
		workloadsAny[i] = w
	}
	return map[string]any{
		"suse-library": map[string]any{
			"services":  servicesAny,
			"workloads": workloadsAny,
		},
	}
}

func TestProjectWithStage_SingleWorkload_EnvResolved(t *testing.T) {
	values := valuesWithWorkloads(
		[]map[string]any{
			{
				"binding": "db",
				"type":    "postgresql",
				"enabled": true,
				"auth": map[string]any{
					"user": map[string]any{"name": "app", "password": "pw", "database": "demo"},
				},
			},
		},
		[]map[string]any{
			{
				"name":  "api",
				"shape": "web",
				"image": map[string]any{"repository": "myapp-api", "tag": "dev"},
				"port":  8080,
				"env": map[string]any{
					"DB_HOST": "${binding:db.host}",
				},
			},
		},
	)

	res, err := ProjectWithStage(values, crossBindingMappings(), "myrelease", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	suseOut := res.Overlay["suse-library"].(map[string]any)

	// workloads_resolved should exist
	wrRaw, ok := suseOut["workloads_resolved"].([]any)
	if !ok {
		t.Fatal("expected workloads_resolved in output")
	}
	if len(wrRaw) != 1 {
		t.Fatalf("expected 1 resolved workload, got %d", len(wrRaw))
	}

	w := wrRaw[0].(map[string]any)
	if w["name"] != "api" {
		t.Errorf("name = %v; want api", w["name"])
	}
	if w["kind"] != "Deployment" {
		t.Errorf("kind = %v; want Deployment", w["kind"])
	}
	if w["port"] != 8080 {
		t.Errorf("port = %v; want 8080", w["port"])
	}

	// env_resolved should be per-workload
	envResolved, ok := w["env_resolved"].([]any)
	if !ok {
		t.Fatal("expected env_resolved in workload")
	}
	if len(envResolved) != 1 {
		t.Fatalf("expected 1 env entry, got %d", len(envResolved))
	}
	entry := envResolved[0].(map[string]any)
	if entry["name"] != "DB_HOST" {
		t.Errorf("env name = %v; want DB_HOST", entry["name"])
	}

	// Old top-level env_resolved should NOT exist
	if _, hasTopLevel := suseOut["env_resolved"]; hasTopLevel {
		t.Error("top-level env_resolved should not exist with workloads[] — it's per-workload now")
	}

	// shape key should be removed from output
	if _, hasShape := w["shape"]; hasShape {
		t.Error("shape key should be removed from resolved output")
	}
	// raw env should be removed
	if _, hasEnv := w["env"]; hasEnv {
		t.Error("raw env key should be removed from resolved output (replaced by env_resolved)")
	}
}

func TestProjectWithStage_MultiWorkload(t *testing.T) {
	values := valuesWithWorkloads(
		[]map[string]any{
			{
				"binding": "db",
				"type":    "postgresql",
				"enabled": true,
				"auth": map[string]any{
					"user": map[string]any{"name": "app", "password": "pw", "database": "demo"},
				},
			},
			{
				"binding": "events",
				"type":    "redis",
				"enabled": true,
				"auth":    map[string]any{"password": "redis-pw"},
			},
		},
		[]map[string]any{
			{
				"name":     "api",
				"shape":    "web",
				"image":    map[string]any{"repository": "myapp-api", "tag": "dev"},
				"port":     8080,
				"replicas": 2,
				"env": map[string]any{
					"DB_HOST": "${binding:db.host}",
				},
			},
			{
				"name":  "worker",
				"shape": "worker",
				"image": map[string]any{"repository": "myapp-worker", "tag": "dev"},
				"env": map[string]any{
					"QUEUE_URL": "${binding:events.url}",
				},
			},
		},
	)

	res, err := ProjectWithStage(values, fixtureMappings(), "myrelease", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	suseOut := res.Overlay["suse-library"].(map[string]any)
	wrRaw := suseOut["workloads_resolved"].([]any)
	if len(wrRaw) != 2 {
		t.Fatalf("expected 2 resolved workloads, got %d", len(wrRaw))
	}

	api := wrRaw[0].(map[string]any)
	worker := wrRaw[1].(map[string]any)

	if api["name"] != "api" {
		t.Errorf("first workload name = %v; want api", api["name"])
	}
	if worker["name"] != "worker" {
		t.Errorf("second workload name = %v; want worker", worker["name"])
	}

	// API should have web shape defaults
	if api["kind"] != "Deployment" {
		t.Errorf("api kind = %v; want Deployment", api["kind"])
	}
	if api["replicas"] != 2 {
		t.Errorf("api replicas = %v; want 2 (explicit)", api["replicas"])
	}
	apiProbes, ok := api["probes"].(map[string]any)
	if !ok {
		t.Error("api should have probes from web shape")
	} else if _, hasLiveness := apiProbes["liveness"]; !hasLiveness {
		t.Error("api should have liveness probe from web shape")
	}

	// Worker should have worker shape defaults
	if worker["kind"] != "Deployment" {
		t.Errorf("worker kind = %v; want Deployment", worker["kind"])
	}
	if worker["probes"] != nil {
		t.Errorf("worker probes = %v; want nil (worker shape)", worker["probes"])
	}

	// Each workload should have its own env_resolved
	apiEnv := api["env_resolved"].([]any)
	if len(apiEnv) != 1 {
		t.Fatalf("api env_resolved: expected 1 entry, got %d", len(apiEnv))
	}
	if apiEnv[0].(map[string]any)["name"] != "DB_HOST" {
		t.Errorf("api env_resolved[0].name = %v; want DB_HOST", apiEnv[0].(map[string]any)["name"])
	}

	workerEnv := worker["env_resolved"].([]any)
	if len(workerEnv) != 1 {
		t.Fatalf("worker env_resolved: expected 1 entry, got %d", len(workerEnv))
	}
	if workerEnv[0].(map[string]any)["name"] != "QUEUE_URL" {
		t.Errorf("worker env_resolved[0].name = %v; want QUEUE_URL", workerEnv[0].(map[string]any)["name"])
	}
}

func TestProjectWithStage_CronJobKind(t *testing.T) {
	values := valuesWithWorkloads(
		[]map[string]any{
			{
				"binding": "db",
				"type":    "postgresql",
				"enabled": true,
				"auth": map[string]any{
					"user": map[string]any{"name": "app", "password": "pw", "database": "demo"},
				},
			},
		},
		[]map[string]any{
			{
				"name":     "scheduler",
				"shape":    "cron",
				"image":    map[string]any{"repository": "myapp-scheduler", "tag": "dev"},
				"schedule": "*/15 * * * *",
				"env": map[string]any{
					"DB_HOST": "${binding:db.host}",
				},
			},
		},
	)

	res, err := ProjectWithStage(values, crossBindingMappings(), "myrelease", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wrRaw := res.Overlay["suse-library"].(map[string]any)["workloads_resolved"].([]any)
	w := wrRaw[0].(map[string]any)
	if w["kind"] != "CronJob" {
		t.Errorf("kind = %v; want CronJob", w["kind"])
	}
	if w["schedule"] != "*/15 * * * *" {
		t.Errorf("schedule = %v; want */15 * * * *", w["schedule"])
	}
	if w["restartPolicy"] != "OnFailure" {
		t.Errorf("restartPolicy = %v; want OnFailure (from cron shape)", w["restartPolicy"])
	}
}

func TestProjectWithStage_StageOverrides_PerWorkload(t *testing.T) {
	values := map[string]any{
		"suse-library": map[string]any{
			"services": []any{
				map[string]any{
					"binding": "db",
					"type":    "postgresql",
					"auth": map[string]any{
						"user": map[string]any{"name": "app", "password": "pw", "database": "demo"},
					},
				},
			},
			"workloads": []any{
				map[string]any{
					"name":     "api",
					"shape":    "web",
					"image":    map[string]any{"repository": "myapp-api", "tag": "dev"},
					"port":     8080,
					"replicas": 1,
				},
				map[string]any{
					"name":     "worker",
					"shape":    "worker",
					"image":    map[string]any{"repository": "myapp-worker", "tag": "dev"},
					"replicas": 1,
				},
			},
			"overrides": map[string]any{
				"staging": map[string]any{
					"workloads": map[string]any{
						"api":    map[string]any{"replicas": 3},
						"worker": map[string]any{"replicas": 2},
					},
				},
			},
		},
	}

	res, err := ProjectWithStage(values, crossBindingMappings(), "myrelease", "staging")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wrRaw := res.Overlay["suse-library"].(map[string]any)["workloads_resolved"].([]any)
	if len(wrRaw) != 2 {
		t.Fatalf("expected 2 workloads, got %d", len(wrRaw))
	}

	api := wrRaw[0].(map[string]any)
	worker := wrRaw[1].(map[string]any)

	if api["replicas"] != 3 {
		t.Errorf("api replicas = %v; want 3 (staging override)", api["replicas"])
	}
	if worker["replicas"] != 2 {
		t.Errorf("worker replicas = %v; want 2 (staging override)", worker["replicas"])
	}
}

func TestProjectWithStage_InlineWorkloadOverrides(t *testing.T) {
	values := map[string]any{
		"suse-library": map[string]any{
			"services": []any{
				map[string]any{
					"binding": "db",
					"type":    "postgresql",
					"auth": map[string]any{
						"user": map[string]any{"name": "app", "password": "pw", "database": "demo"},
					},
				},
			},
			"workloads": []any{
				map[string]any{
					"name":     "api",
					"shape":    "web",
					"image":    map[string]any{"repository": "myapp-api", "tag": "dev"},
					"replicas": 1,
					"overrides": map[string]any{
						"prod": map[string]any{
							"replicas": 5,
							"resources": map[string]any{
								"limits": map[string]any{"memory": "2Gi"},
							},
						},
					},
				},
			},
		},
	}

	res, err := ProjectWithStage(values, crossBindingMappings(), "myrelease", "prod")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wrRaw := res.Overlay["suse-library"].(map[string]any)["workloads_resolved"].([]any)
	w := wrRaw[0].(map[string]any)
	if w["replicas"] != 5 {
		t.Errorf("replicas = %v; want 5 (prod override)", w["replicas"])
	}
	resources := w["resources"].(map[string]any)
	limits := resources["limits"].(map[string]any)
	if limits["memory"] != "2Gi" {
		t.Errorf("resources.limits.memory = %v; want 2Gi (prod override)", limits["memory"])
	}
}

func TestProjectWithStage_NoWorkloads_NoWorkloadsResolved(t *testing.T) {
	// Projects without workloads[] should produce no workloads_resolved.
	values := valuesWith(map[string]any{
		"binding": "db",
		"type":    "postgresql",
		"auth": map[string]any{
			"user": map[string]any{"name": "app", "password": "pw", "database": "demo"},
		},
	})

	res, err := ProjectWithStage(values, fixtureMappings(), "myrelease", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	suseOut, ok := res.Overlay["suse-library"].(map[string]any)
	if !ok {
		return // no suse-library output is fine
	}
	if _, has := suseOut["workloads_resolved"]; has {
		t.Error("workloads_resolved should not exist when no workloads[] is defined")
	}
}

func TestProjectWithStage_WorkloadEnvSharedBindings(t *testing.T) {
	// Multiple workloads referencing the same binding should work.
	values := valuesWithWorkloads(
		[]map[string]any{
			{
				"binding": "db",
				"type":    "postgresql",
				"enabled": true,
				"auth": map[string]any{
					"user": map[string]any{"name": "app", "password": "pw", "database": "demo"},
				},
			},
		},
		[]map[string]any{
			{
				"name":  "api",
				"image": map[string]any{"repository": "myapp-api"},
				"env": map[string]any{
					"DB_HOST": "${binding:db.host}",
				},
			},
			{
				"name":  "worker",
				"image": map[string]any{"repository": "myapp-worker"},
				"env": map[string]any{
					"DB_HOST": "${binding:db.host}",
					"DB_PORT": "${binding:db.port}",
				},
			},
		},
	)

	res, err := ProjectWithStage(values, crossBindingMappings(), "myrelease", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wrRaw := res.Overlay["suse-library"].(map[string]any)["workloads_resolved"].([]any)
	apiEnv := wrRaw[0].(map[string]any)["env_resolved"].([]any)
	workerEnv := wrRaw[1].(map[string]any)["env_resolved"].([]any)

	if len(apiEnv) != 1 {
		t.Errorf("api should have 1 env entry, got %d", len(apiEnv))
	}
	if len(workerEnv) != 2 {
		t.Errorf("worker should have 2 env entries, got %d", len(workerEnv))
	}
}

// crossBindingMappings provides the test fixture that existing tests use
// (from projection_test.go and bindingref_test.go). It needs to be
// accessible here — it's the same function referenced by the existing
// test files.
func crossBindingMappingsForWorkloads() *dslmapping.Document {
	return crossBindingMappings()
}

// ── Workload override unit tests ──

func TestPreProcessWorkloadOverrides(t *testing.T) {
	values := map[string]any{
		"suse-library": map[string]any{
			"workloads": []any{
				map[string]any{"name": "api", "replicas": 1, "image": map[string]any{"repository": "a"}},
				map[string]any{"name": "worker", "replicas": 1, "image": map[string]any{"repository": "b"}},
			},
			"overrides": map[string]any{
				"staging": map[string]any{
					"domain": "staging.example.com",
					"workloads": map[string]any{
						"api":    map[string]any{"replicas": 3},
						"worker": map[string]any{"replicas": 2},
					},
				},
			},
		},
	}

	preProcessWorkloadOverrides(values, "staging")

	suse := values["suse-library"].(map[string]any)

	// workloads should still be an array (not clobbered by the map)
	workloads, ok := suse["workloads"].([]any)
	if !ok {
		t.Fatal("workloads should remain an array after preProcessWorkloadOverrides")
	}

	api := workloads[0].(map[string]any)
	if api["replicas"] != 3 {
		t.Errorf("api replicas = %v; want 3 (staging override)", api["replicas"])
	}
	worker := workloads[1].(map[string]any)
	if worker["replicas"] != 2 {
		t.Errorf("worker replicas = %v; want 2 (staging override)", worker["replicas"])
	}

	// The workloads key should be removed from the stage override
	stageOv := suse["overrides"].(map[string]any)["staging"].(map[string]any)
	if _, has := stageOv["workloads"]; has {
		t.Error("workloads key should be removed from stage overrides after pre-processing")
	}
	// domain should remain for applyAppOverrides
	if stageOv["domain"] != "staging.example.com" {
		t.Errorf("domain should remain in stage overrides: %v", stageOv["domain"])
	}
}

func TestApplyWorkloadOverrides_InlinePerWorkload(t *testing.T) {
	values := map[string]any{
		"suse-library": map[string]any{
			"workloads": []any{
				map[string]any{
					"name":     "api",
					"replicas": 1,
					"image":    map[string]any{"repository": "a"},
					"overrides": map[string]any{
						"prod": map[string]any{"replicas": 10},
					},
				},
			},
		},
	}

	applyWorkloadOverrides(values, "prod")

	w := values["suse-library"].(map[string]any)["workloads"].([]any)[0].(map[string]any)
	if w["replicas"] != 10 {
		t.Errorf("replicas = %v; want 10 (prod override)", w["replicas"])
	}
	if _, has := w["overrides"]; has {
		t.Error("overrides key should be removed after merge")
	}
}

// ── Deep-merge workload tests ──

func TestDeepMergeWorkload_NilOverridesProbes(t *testing.T) {
	dst := map[string]any{
		"probes": map[string]any{
			"liveness": map[string]any{"path": "/health"},
		},
	}
	src := map[string]any{
		"probes": nil, // explicitly null probes
	}
	deepMergeWorkload(dst, src)
	if dst["probes"] != nil {
		t.Errorf("probes should be nil after merge with nil, got %v", dst["probes"])
	}
}

func TestDeepCopyWorkloadMap_Independence(t *testing.T) {
	original := map[string]any{
		"resources": map[string]any{
			"requests": map[string]any{"cpu": "100m"},
		},
	}
	copied := deepCopyWorkloadMap(original)
	// Mutate copy
	copied["resources"].(map[string]any)["requests"].(map[string]any)["cpu"] = "999m"
	// Original should be unchanged
	if original["resources"].(map[string]any)["requests"].(map[string]any)["cpu"] != "100m" {
		t.Error("deepCopyWorkloadMap did not produce an independent copy")
	}
}
