package render

import (
	"strings"
	"testing"

	"github.com/idefxH/rda-render-engine/dslmapping"
)

// fixture mappings — minimal, enough to cover the projection paths we care
// about. Mirrors the real bundle's dsl-mappings.yaml shape but trimmed.
func fixtureMappings() *dslmapping.Document {
	return &dslmapping.Document{
		APIVersion: "rda.suse.com/dsl-mapping/v1alpha1",
		Charts: map[string]dslmapping.ChartEntry{
			"postgresql": {
				Versions: []dslmapping.VersionEntry{{
					Constraint: ">=0.4.0 <1.0.0",
					Service:    dslmapping.ServiceSpec{Host: "{{ .Release.Name }}-postgresql", Port: 5432},
					ValuesMapping: map[string]string{
						"auth.user.name":             "postgresql.auth.username",
						"auth.user.password":         "postgresql.auth.password",
						"auth.user.database":         "postgresql.auth.database",
						"auth.admin.password":        "postgresql.auth.postgresPassword",
						"persistence.enabled":        "postgresql.persistence.enabled",
						"persistence.size":           "postgresql.persistence.resources.requests.storage",
						"metrics.enabled":            "postgresql.metrics.enabled",
						"resources.requests.cpu":     "postgresql.primary.resources.requests.cpu",
						"resources.requests.memory":  "postgresql.primary.resources.requests.memory",
						"resources.limits.cpu":       "postgresql.primary.resources.limits.cpu",
						"resources.limits.memory":    "postgresql.primary.resources.limits.memory",
					},
				}},
			},
			"redis": {
				Versions: []dslmapping.VersionEntry{{
					Constraint: ">=21.0.0",
					Service:    dslmapping.ServiceSpec{Host: "{{ .Release.Name }}-redis-master", Port: 6379},
					ValuesMapping: map[string]string{
						"auth.password":       "redis.auth.password",
						"persistence.enabled": "redis.master.persistence.enabled",
					},
				}},
			},
			"grafana": {
				Versions: []dslmapping.VersionEntry{{
					Constraint: ">=12.0.0",
					Service:    dslmapping.ServiceSpec{Host: "{{ .Release.Name }}-grafana", Port: 80},
					ValuesMapping: map[string]string{
						"auth.admin.name":     "grafana.adminUser",
						"auth.admin.password": "grafana.adminPassword",
						"ingress.enabled":     "grafana.ingress.enabled",
					},
				}},
			},
		},
	}
}

// build a project-shape values map for one DSL service entry.
func valuesWith(services ...map[string]any) map[string]any {
	servicesAny := make([]any, len(services))
	for i, s := range services {
		servicesAny[i] = s
	}
	return map[string]any{
		"suse-library": map[string]any{
			"services": servicesAny,
		},
	}
}

func TestProject_PostgresHappyPath(t *testing.T) {
	values := valuesWith(map[string]any{
		"binding": "db",
		"type":    "postgresql",
		"auth": map[string]any{
			"admin": map[string]any{"password": "admin-pw"},
			"user": map[string]any{
				"name":     "app",
				"password": "app-pw",
				"database": "demo",
			},
		},
		"persistence": map[string]any{"enabled": true, "size": "1Gi"},
		"metrics":     map[string]any{"enabled": true},
	})

	res, err := Project(values, fixtureMappings(), "payments")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ProjectionsCount != 1 {
		t.Errorf("expected 1 projection, got %d", res.ProjectionsCount)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("expected no warnings, got: %v", res.Warnings)
	}

	pg := digMap(t, res.Overlay, "suse-library", "postgresql")
	if pg["enabled"] != true {
		t.Errorf("expected postgresql.enabled=true, got %v", pg["enabled"])
	}
	auth := pg["auth"].(map[string]any)
	if auth["username"] != "app" {
		t.Errorf("expected postgresql.auth.username=app, got %v", auth["username"])
	}
	if auth["password"] != "app-pw" {
		t.Errorf("expected postgresql.auth.password=app-pw, got %v", auth["password"])
	}
	if auth["database"] != "demo" {
		t.Errorf("expected postgresql.auth.database=demo, got %v", auth["database"])
	}
	if auth["postgresPassword"] != "admin-pw" {
		t.Errorf("expected postgresql.auth.postgresPassword=admin-pw, got %v", auth["postgresPassword"])
	}
	persistence := pg["persistence"].(map[string]any)
	if persistence["enabled"] != true {
		t.Errorf("expected persistence.enabled=true, got %v", persistence["enabled"])
	}
	// AppCo postgres maps DSL persistence.size -> postgresql.persistence.resources.requests.storage
	storage := digMap(t, pg, "persistence", "resources", "requests")
	if storage["storage"] != "1Gi" {
		t.Errorf("expected postgresql.persistence.resources.requests.storage=1Gi, got %v", storage["storage"])
	}
}

func TestProject_RedisHappyPath(t *testing.T) {
	values := valuesWith(map[string]any{
		"binding": "cache",
		"type":    "redis",
		"auth":    map[string]any{"password": "redis-pw"},
		"persistence": map[string]any{
			"enabled": false, // master.persistence.enabled
		},
	})

	res, err := Project(values, fixtureMappings(), "payments")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ProjectionsCount != 1 {
		t.Fatalf("expected 1 projection, got %d", res.ProjectionsCount)
	}
	redis := digMap(t, res.Overlay, "suse-library", "redis")
	if redis["enabled"] != true {
		t.Errorf("expected redis.enabled=true, got %v", redis["enabled"])
	}
	auth := redis["auth"].(map[string]any)
	if auth["password"] != "redis-pw" {
		t.Errorf("expected redis.auth.password=redis-pw, got %v", auth["password"])
	}
	master := redis["master"].(map[string]any)
	persistence := master["persistence"].(map[string]any)
	if persistence["enabled"] != false {
		t.Errorf("expected redis.master.persistence.enabled=false, got %v", persistence["enabled"])
	}
}

func TestProject_GrafanaIngress(t *testing.T) {
	values := valuesWith(map[string]any{
		"binding": "dashboards",
		"type":    "grafana",
		"auth":    map[string]any{"admin": map[string]any{"name": "admin", "password": "admin-pw"}},
		"ingress": map[string]any{"enabled": true},
	})

	res, err := Project(values, fixtureMappings(), "payments")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	graf := digMap(t, res.Overlay, "suse-library", "grafana")
	if graf["adminUser"] != "admin" {
		t.Errorf("expected grafana.adminUser=admin, got %v", graf["adminUser"])
	}
	if graf["adminPassword"] != "admin-pw" {
		t.Errorf("expected grafana.adminPassword=admin-pw, got %v", graf["adminPassword"])
	}
	ing := graf["ingress"].(map[string]any)
	if ing["enabled"] != true {
		t.Errorf("expected grafana.ingress.enabled=true, got %v", ing["enabled"])
	}
}

// TestProject_BracketNotation_HostsList covers the dsl-mappings convention
// for list-shaped chart values: `prometheus.server.ingress.hosts[0]` must
// project to a real list element (`hosts: [<value>]`), NOT a literal map
// key `hosts[0]`. Live bug Apr 30: with literal key, helm reads no
// ingress.hosts → no Ingress resource → no clickable link in Tilt UI.
func TestProject_BracketNotation_HostsList(t *testing.T) {
	mappings := &dslmapping.Document{
		APIVersion: "rda.suse.com/dsl-mapping/v1alpha1",
		Charts: map[string]dslmapping.ChartEntry{
			"prometheus": {
				Versions: []dslmapping.VersionEntry{{
					Constraint: ">=29.0.0",
					Service:    dslmapping.ServiceSpec{Host: "{{ .Release.Name }}-prometheus-server", Port: 80},
					ValuesMapping: map[string]string{
						"ingress.enabled": "prometheus.server.ingress.enabled",
						"ingress.host":    "prometheus.server.ingress.hosts[0]",
					},
				}},
			},
		},
	}
	values := valuesWith(map[string]any{
		"binding": "metrics",
		"type":    "prometheus",
		"ingress": map[string]any{
			"enabled": true,
			"host":    "prometheus.localhost",
		},
	})

	res, err := Project(values, mappings, "payments")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	server := digMap(t, res.Overlay, "suse-library", "prometheus", "server")
	ing, ok := server["ingress"].(map[string]any)
	if !ok {
		t.Fatalf("prometheus.server.ingress should be a map, got %T", server["ingress"])
	}
	if ing["enabled"] != true {
		t.Errorf("ingress.enabled=true expected, got %v", ing["enabled"])
	}
	hosts, ok := ing["hosts"].([]any)
	if !ok {
		t.Fatalf("ingress.hosts should be a list (was the bracket-notation fix applied?); got %T = %v",
			ing["hosts"], ing["hosts"])
	}
	if len(hosts) != 1 {
		t.Fatalf("expected 1 host, got %d: %v", len(hosts), hosts)
	}
	if hosts[0] != "prometheus.localhost" {
		t.Errorf("expected hosts[0]=prometheus.localhost, got %v", hosts[0])
	}
	// Belt-and-braces: there should be NO literal key 'hosts[0]' in the
	// map (which would be the pre-fix behaviour).
	if _, bad := ing["hosts[0]"]; bad {
		t.Errorf("regression: literal key 'hosts[0]' present in projection — bracket parsing broke")
	}
}

// TestProject_BracketNotation_MultipleIndices covers the case where the
// projection writes to two indices of the same list (e.g. hosts[0] and
// hosts[1]). The list grows; both elements end up populated; nil
// placeholders fill any gap.
func TestProject_BracketNotation_MultipleIndices(t *testing.T) {
	// Direct setAtPath drive — no DSL ceremony, just validate the helper.
	m := map[string]any{}
	if err := setAtPath(m, "ing.hosts[0]", "a.local"); err != nil {
		t.Fatalf("set hosts[0]: %v", err)
	}
	if err := setAtPath(m, "ing.hosts[2]", "c.local"); err != nil {
		t.Fatalf("set hosts[2]: %v", err)
	}
	hosts := m["ing"].(map[string]any)["hosts"].([]any)
	if len(hosts) != 3 {
		t.Fatalf("expected list length 3 (with nil at index 1), got %d: %v", len(hosts), hosts)
	}
	if hosts[0] != "a.local" || hosts[2] != "c.local" || hosts[1] != nil {
		t.Errorf("unexpected list contents: %v", hosts)
	}
}

// TestProject_BracketNotation_MalformedRejected covers the malformed
// bracket cases — `hosts[`, `hosts[]`, `hosts[abc]`. Each should fail with
// an error; the projection should not silently fall back to literal-key
// writes (which would mask the typo).
func TestProject_BracketNotation_MalformedRejected(t *testing.T) {
	cases := []string{
		"ing.hosts[",
		"ing.hosts[]",
		"ing.hosts[abc]",
	}
	for _, p := range cases {
		m := map[string]any{}
		err := setAtPath(m, p, "x")
		if err == nil {
			t.Errorf("path %q: expected error on malformed bracket, got nil; map=%v", p, m)
		}
	}
}

func TestProject_SharedProvisioningSkipped(t *testing.T) {
	values := valuesWith(map[string]any{
		"binding":      "db",
		"type":         "postgresql",
		"provisioning": "shared",
		"auth": map[string]any{
			"user": map[string]any{"name": "app", "password": "x", "database": "y"},
		},
	})

	res, err := Project(values, fixtureMappings(), "payments")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ProjectionsCount != 0 {
		t.Errorf("expected 0 projections (shared skipped), got %d", res.ProjectionsCount)
	}
	if len(res.Overlay) != 0 {
		t.Errorf("expected empty overlay for shared, got %v", res.Overlay)
	}
}

func TestProject_ExternalProvisioningSkipped(t *testing.T) {
	values := valuesWith(map[string]any{
		"binding":      "db",
		"type":         "postgresql",
		"provisioning": "external",
		"endpoint":     map[string]any{"host": "pg.corp", "port": "5432", "scheme": "postgres"},
	})

	res, err := Project(values, fixtureMappings(), "payments")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ProjectionsCount != 0 {
		t.Errorf("expected 0 projections, got %d", res.ProjectionsCount)
	}
}

func TestProject_TypeNotInCatalogueWarns(t *testing.T) {
	values := valuesWith(map[string]any{
		"binding": "events",
		"type":    "kafka", // not in fixtureMappings
	})

	res, err := Project(values, fixtureMappings(), "payments")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ProjectionsCount != 0 {
		t.Errorf("expected 0 projections, got %d", res.ProjectionsCount)
	}
	if len(res.Warnings) == 0 {
		t.Fatalf("expected a warning about unknown type, got none")
	}
	if !strings.Contains(res.Warnings[0], "kafka") {
		t.Errorf("warning should name the unknown type; got: %q", res.Warnings[0])
	}
}

// TestProject_DisabledServiceSkipped covers the inert-by-default
// scaffold contract from rda-cli#67 / bundle 0.11.6+: a services[]
// entry with `enabled: false` produces no projection, no chart-level
// enable flip, and is invisible to the overlay. The Tilt extension's
// auto-discovery + library helpers also iterate the enabled subset,
// so the contract is end-to-end consistent.
//
// Live e2e regression target: rda-cli v0.1.38's first scaffold of
// inert services made it through render without producing the
// chart-level `enabled: true` — so helm template / kubectl apply
// went through with no postgres/redis/etc. That's the desired
// outcome (the dev hasn't filled FILL MEs yet); breaking this test
// would mean an inert scaffold accidentally deploys.
func TestProject_DisabledServiceSkipped(t *testing.T) {
	values := valuesWith(map[string]any{
		"binding": "db",
		"type":    "postgresql",
		"enabled": false,
		"auth": map[string]any{
			"user": map[string]any{
				"name":     "app",
				"password": "should-be-ignored",
				"database": "should-be-ignored",
			},
		},
	})

	res, err := Project(values, fixtureMappings(), "payments")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ProjectionsCount != 0 {
		t.Errorf("expected 0 projections for disabled service; got %d",
			res.ProjectionsCount)
	}
	if len(res.Overlay) != 0 {
		t.Errorf("expected empty overlay for disabled service; got: %v",
			res.Overlay)
	}
}

// TestProject_EnabledFalseExplicitVsAbsent guards against a subtle
// regression: missing `enabled` (pre-0.1.38 scaffolds) MUST default
// to true (back-compat), but explicit `enabled: false` MUST skip.
// This is the contract that lets new bundle helpers ship without
// breaking projects scaffolded against older ones.
func TestProject_EnabledFalseExplicitVsAbsent(t *testing.T) {
	cases := []struct {
		name             string
		entry            map[string]any
		wantProjections  int
	}{
		{
			name: "enabled absent (back-compat: defaults true → projects)",
			entry: map[string]any{
				"binding": "db",
				"type":    "postgresql",
				"auth": map[string]any{
					"user": map[string]any{"name": "app", "password": "p", "database": "d"},
				},
			},
			wantProjections: 1,
		},
		{
			name: "enabled true (new contract, explicit)",
			entry: map[string]any{
				"binding": "db",
				"type":    "postgresql",
				"enabled": true,
				"auth": map[string]any{
					"user": map[string]any{"name": "app", "password": "p", "database": "d"},
				},
			},
			wantProjections: 1,
		},
		{
			name: "enabled false (inert scaffold)",
			entry: map[string]any{
				"binding": "db",
				"type":    "postgresql",
				"enabled": false,
				"auth": map[string]any{
					"user": map[string]any{"name": "app", "password": "p", "database": "d"},
				},
			},
			wantProjections: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Project(valuesWith(tc.entry), fixtureMappings(), "payments")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.ProjectionsCount != tc.wantProjections {
				t.Errorf("ProjectionsCount = %d, want %d (overlay: %v)",
					res.ProjectionsCount, tc.wantProjections, res.Overlay)
			}
		})
	}
}

// TestProject_DisabledServiceDoesNotFlipChartEnable guards the
// services-iteration invariant from the rda-cli side: a disabled
// services[] entry must NOT cause `<chart>.enabled: true` to be
// written to the overlay (otherwise Helm's dep `condition:` evaluates
// true → sub-chart loads → user gets postgres/redis/etc. they didn't
// ask for). Pairs with bundle BEHAVIOR/services-iteration.
func TestProject_DisabledServiceDoesNotFlipChartEnable(t *testing.T) {
	values := map[string]any{
		"suse-library": map[string]any{
			"services": []any{
				map[string]any{
					"binding": "db",
					"type":    "postgresql",
					"enabled": false,
				},
				// Mix in an enabled redis to make sure the overlay has
				// SOMETHING — we want to check that postgresql is NOT
				// in the overlay specifically.
				map[string]any{
					"binding": "cache",
					"type":    "redis",
					"enabled": true,
					"auth":    map[string]any{"password": "cache-p"},
				},
			},
		},
	}
	res, err := Project(values, fixtureMappings(), "payments")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	suse, ok := res.Overlay["suse-library"].(map[string]any)
	if !ok {
		t.Fatalf("expected overlay.suse-library to be a map; got: %T", res.Overlay["suse-library"])
	}
	if _, present := suse["postgresql"]; present {
		t.Errorf("disabled postgresql service should NOT produce a postgresql block in overlay; got: %v", suse["postgresql"])
	}
	if _, present := suse["redis"]; !present {
		t.Errorf("enabled redis service should produce a redis block in overlay; got nothing")
	}
}

func TestProject_EmptyServicesNoProjection(t *testing.T) {
	values := map[string]any{
		"suse-library": map[string]any{
			"services": []any{},
		},
	}
	res, err := Project(values, fixtureMappings(), "payments")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ProjectionsCount != 0 {
		t.Errorf("expected 0 projections, got %d", res.ProjectionsCount)
	}
	if len(res.Overlay) != 0 {
		t.Errorf("expected empty overlay, got: %v", res.Overlay)
	}
}

func TestProject_NoSuseLibraryNoProjection(t *testing.T) {
	values := map[string]any{"foo": "bar"}
	res, err := Project(values, fixtureMappings(), "payments")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Overlay) != 0 {
		t.Errorf("expected empty overlay, got: %v", res.Overlay)
	}
}

func TestProject_NilMappingsFallsBackWithWarning(t *testing.T) {
	values := valuesWith(map[string]any{
		"binding": "db",
		"type":    "postgresql",
	})
	res, err := Project(values, nil, "payments")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ProjectionsCount != 0 {
		t.Errorf("expected 0 projections (nil mappings), got %d", res.ProjectionsCount)
	}
	if len(res.Overlay) != 0 {
		t.Errorf("expected empty overlay, got: %v", res.Overlay)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "no dsl-mappings.yaml") {
		t.Errorf("expected fallback warning, got: %v", res.Warnings)
	}
}

func TestProject_MultiBindingSameTypeCreatesAliasedBlocks(t *testing.T) {
	values := valuesWith(
		map[string]any{
			"binding": "primary-db",
			"type":    "postgresql",
			"auth": map[string]any{
				"user": map[string]any{"name": "primary-user", "password": "p1", "database": "primary"},
			},
		},
		map[string]any{
			"binding": "events-db",
			"type":    "postgresql",
			"auth": map[string]any{
				"user": map[string]any{"name": "events-user", "password": "p2", "database": "events"},
			},
		},
	)
	res, err := Project(values, fixtureMappings(), "payments")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ProjectionsCount != 2 {
		t.Errorf("expected 2 projections, got %d", res.ProjectionsCount)
	}
	// No collision warning — aliases handle multi-instance.
	for _, w := range res.Warnings {
		if strings.Contains(w, "collides") {
			t.Errorf("should not warn about collision with aliases; got: %q", w)
		}
	}
	// Each instance gets its own aliased block.
	suse := res.Overlay["suse-library"].(map[string]any)

	primary := suse["postgresql-primary-db"]
	if primary == nil {
		t.Fatal("expected postgresql-primary-db block in overlay")
	}
	primaryMap := primary.(map[string]any)
	if primaryMap["enabled"] != true {
		t.Error("postgresql-primary-db.enabled should be true")
	}
	primaryAuth := primaryMap["auth"].(map[string]any)
	if primaryAuth["username"] != "primary-user" {
		t.Errorf("expected primary-user, got %v", primaryAuth["username"])
	}

	events := suse["postgresql-events-db"]
	if events == nil {
		t.Fatal("expected postgresql-events-db block in overlay")
	}
	eventsMap := events.(map[string]any)
	if eventsMap["enabled"] != true {
		t.Error("postgresql-events-db.enabled should be true")
	}
	eventsAuth := eventsMap["auth"].(map[string]any)
	if eventsAuth["username"] != "events-user" {
		t.Errorf("expected events-user, got %v", eventsAuth["username"])
	}

	// Bare "postgresql" block should NOT exist.
	if suse["postgresql"] != nil {
		t.Error("bare postgresql block should not exist when multi-instance aliases are used")
	}
}

func TestProject_MultiInstanceBindingResolution(t *testing.T) {
	// Verify that cross-binding references resolve correctly for
	// multi-instance services (each instance has its own host).
	mappings := fixtureMappings()
	// Add FQDN-style host template so AliasedHost replacement works.
	pgEntry := mappings.Charts["postgresql"]
	pgEntry.Versions[0].Service.Host = "{{ .Release.Name }}-postgresql.{{ .Release.Namespace }}.svc.cluster.local"
	mappings.Charts["postgresql"] = pgEntry

	values := valuesWith(
		map[string]any{
			"binding": "primary-db",
			"type":    "postgresql",
			"auth":    map[string]any{"user": map[string]any{"name": "u1", "password": "p1", "database": "d1"}},
		},
		map[string]any{
			"binding": "events-db",
			"type":    "postgresql",
			"auth":    map[string]any{"user": map[string]any{"name": "u2", "password": "p2", "database": "d2"}},
		},
	)
	res, err := Project(values, mappings, "myapp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	suse := res.Overlay["suse-library"].(map[string]any)

	// _chart_aliases map should exist for multi-instance.
	aliasMap, ok := suse["_chart_aliases"].(map[string]any)
	if !ok {
		t.Fatal("expected _chart_aliases map in overlay for multi-instance")
	}
	if aliasMap["primary-db"] != "postgresql-primary-db" {
		t.Errorf("expected alias postgresql-primary-db, got %v", aliasMap["primary-db"])
	}
	if aliasMap["events-db"] != "postgresql-events-db" {
		t.Errorf("expected alias postgresql-events-db, got %v", aliasMap["events-db"])
	}
}

func TestProject_SingleInstanceNoAliasMap(t *testing.T) {
	values := valuesWith(
		map[string]any{
			"binding": "db",
			"type":    "postgresql",
			"auth":    map[string]any{"user": map[string]any{"name": "app", "password": "x", "database": "y"}},
		},
	)
	res, err := Project(values, fixtureMappings(), "myapp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	suse := res.Overlay["suse-library"].(map[string]any)
	if suse["_chart_aliases"] != nil {
		t.Error("single-instance should not have _chart_aliases map")
	}
	if suse["postgresql"] == nil {
		t.Error("single-instance should have bare postgresql block")
	}
}

func TestProject_MixedSingleAndMultiInstance(t *testing.T) {
	values := valuesWith(
		map[string]any{
			"binding": "db1",
			"type":    "postgresql",
			"auth":    map[string]any{"user": map[string]any{"name": "u1", "password": "p1", "database": "d1"}},
		},
		map[string]any{
			"binding": "db2",
			"type":    "postgresql",
			"auth":    map[string]any{"user": map[string]any{"name": "u2", "password": "p2", "database": "d2"}},
		},
		map[string]any{
			"binding": "cache",
			"type":    "redis",
			"auth":    map[string]any{"password": "r1"},
		},
	)
	res, err := Project(values, fixtureMappings(), "app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	suse := res.Overlay["suse-library"].(map[string]any)

	// PostgreSQL: multi-instance → aliased blocks.
	if suse["postgresql"] != nil {
		t.Error("bare postgresql should not exist (multi-instance)")
	}
	if suse["postgresql-db1"] == nil {
		t.Error("expected postgresql-db1 block")
	}
	if suse["postgresql-db2"] == nil {
		t.Error("expected postgresql-db2 block")
	}

	// Redis: single instance → bare block.
	if suse["redis"] == nil {
		t.Error("expected bare redis block (single instance)")
	}

	if res.ProjectionsCount != 3 {
		t.Errorf("expected 3 projections, got %d", res.ProjectionsCount)
	}
}

func TestProject_Idempotent(t *testing.T) {
	values := valuesWith(map[string]any{
		"binding": "db",
		"type":    "postgresql",
		"auth": map[string]any{
			"user": map[string]any{"name": "app", "password": "x", "database": "y"},
		},
	})
	r1, _ := Project(values, fixtureMappings(), "payments")
	r2, _ := Project(values, fixtureMappings(), "payments")
	if !equalDeep(r1.Overlay, r2.Overlay) {
		t.Errorf("Project should be deterministic; got two different overlays:\n%v\n%v",
			r1.Overlay, r2.Overlay)
	}
}

func TestProject_MissingBindingOrTypeSilentlySkipped(t *testing.T) {
	values := valuesWith(
		map[string]any{"type": "postgresql"},      // no binding
		map[string]any{"binding": "db"},           // no type
		map[string]any{"binding": "", "type": ""}, // both empty
	)
	res, err := Project(values, fixtureMappings(), "payments")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ProjectionsCount != 0 {
		t.Errorf("expected 0 projections (all malformed), got %d", res.ProjectionsCount)
	}
}

func TestProject_OptionalDSLFieldsMissingIsFine(t *testing.T) {
	// Postgres binding with NO auth block at all — render shouldn't fail;
	// validateConsistency at template time enforces required:true.
	values := valuesWith(map[string]any{
		"binding": "db",
		"type":    "postgresql",
	})
	res, err := Project(values, fixtureMappings(), "payments")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ProjectionsCount != 1 {
		t.Errorf("expected 1 projection (binding+type set, that's enough), got %d", res.ProjectionsCount)
	}
	pg := digMap(t, res.Overlay, "suse-library", "postgresql")
	if pg["enabled"] != true {
		t.Errorf("expected postgresql.enabled=true even without auth block, got %v", pg["enabled"])
	}
	if _, hasAuth := pg["auth"]; hasAuth {
		t.Errorf("expected no auth subtree (DSL didn't set any), got %v", pg["auth"])
	}
}

// TestProject_Passthrough_DeepMerged covers BEHAVIOR/render step 5h:
// services[].passthrough is deep-merged into the chart's overlay subtree
// alongside the explicit DSL projections. The user's escape-hatch values
// must land at suse-library.<chartType>.<sub-path>, NOT replace the
// DSL-projected fields.
//
// Live regression: payments project's `dashboard` binding had
// `passthrough.sidecar.dashboards.enabled: true` and the sidecar block
// never reached the generated overlay before 0.1.43. This test pins
// the post-fix behaviour.
func TestProject_Passthrough_DeepMerged(t *testing.T) {
	values := valuesWith(map[string]any{
		"binding": "dashboard",
		"type":    "grafana",
		"auth": map[string]any{
			"admin": map[string]any{"name": "admin", "password": "demo"},
		},
		"ingress": map[string]any{"enabled": true},
		"passthrough": map[string]any{
			"sidecar": map[string]any{
				"dashboards": map[string]any{
					"enabled":         true,
					"label":           "grafana_dashboard",
					"labelValue":      "1",
					"searchNamespace": "ALL",
					"folder":          "/tmp/dashboards",
				},
				"datasources": map[string]any{
					"enabled":    true,
					"label":      "grafana_datasource",
					"labelValue": "1",
				},
			},
		},
	})
	res, err := Project(values, fixtureMappings(), "payments")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ProjectionsCount != 1 {
		t.Errorf("expected 1 projection, got %d", res.ProjectionsCount)
	}
	graf := digMap(t, res.Overlay, "suse-library", "grafana")
	// DSL fields still present.
	if graf["adminUser"] != "admin" || graf["adminPassword"] != "demo" {
		t.Errorf("DSL auth fields missing after passthrough merge: %v", graf)
	}
	ing, ok := graf["ingress"].(map[string]any)
	if !ok || ing["enabled"] != true {
		t.Errorf("DSL ingress.enabled lost after passthrough merge: %v", graf["ingress"])
	}
	// Passthrough-projected sidecar block.
	side := digMap(t, graf, "sidecar")
	dash := digMap(t, side, "dashboards")
	if dash["enabled"] != true {
		t.Errorf("passthrough sidecar.dashboards.enabled missing: %v", dash)
	}
	if dash["searchNamespace"] != "ALL" || dash["folder"] != "/tmp/dashboards" {
		t.Errorf("passthrough nested fields not merged verbatim: %v", dash)
	}
	ds := digMap(t, side, "datasources")
	if ds["enabled"] != true || ds["label"] != "grafana_datasource" {
		t.Errorf("passthrough sidecar.datasources missing fields: %v", ds)
	}
}

// TestProject_Passthrough_SurNestedFails covers ERR_PASSTHROUGH_SURNESTED:
// a `passthrough.<chartType>` key is the typo case where the dev nested
// the block under the chart name. Pre-0.1.43 this was silently dropped.
// Now it returns an error naming the binding and the misplaced key.
func TestProject_Passthrough_SurNestedFails(t *testing.T) {
	values := valuesWith(map[string]any{
		"binding": "dashboard",
		"type":    "grafana",
		"auth": map[string]any{
			"admin": map[string]any{"name": "admin", "password": "demo"},
		},
		"passthrough": map[string]any{
			"grafana": map[string]any{ // ← the surplus chart-name key
				"sidecar": map[string]any{
					"dashboards": map[string]any{"enabled": true},
				},
			},
		},
	})
	_, err := Project(values, fixtureMappings(), "payments")
	if err == nil {
		t.Fatalf("expected ERR_PASSTHROUGH_SURNESTED error, got nil")
	}
	if !strings.Contains(err.Error(), "passthrough") {
		t.Errorf("error should mention passthrough; got: %v", err)
	}
	if !strings.Contains(err.Error(), "dashboard") {
		t.Errorf("error should name the binding; got: %v", err)
	}
	if !strings.Contains(err.Error(), "grafana") {
		t.Errorf("error should name the chart type; got: %v", err)
	}
}

// TestProject_Passthrough_NotAMapFails covers a malformed passthrough
// (e.g. someone writes `passthrough: ""` or `passthrough: [foo]`). The
// projection refuses to silently ignore it.
func TestProject_Passthrough_NotAMapFails(t *testing.T) {
	values := valuesWith(map[string]any{
		"binding":     "dashboard",
		"type":        "grafana",
		"passthrough": "this should be a map",
	})
	_, err := Project(values, fixtureMappings(), "payments")
	if err == nil {
		t.Fatalf("expected error for non-map passthrough, got nil")
	}
	if !strings.Contains(err.Error(), "passthrough") {
		t.Errorf("error should mention passthrough; got: %v", err)
	}
}

// TestProject_Passthrough_AbsentIsFine pins the no-op case: a service
// entry with no `passthrough` key projects exactly as before.
func TestProject_Passthrough_AbsentIsFine(t *testing.T) {
	values := valuesWith(map[string]any{
		"binding": "dashboard",
		"type":    "grafana",
		"auth": map[string]any{
			"admin": map[string]any{"name": "admin", "password": "demo"},
		},
	})
	res, err := Project(values, fixtureMappings(), "payments")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	graf := digMap(t, res.Overlay, "suse-library", "grafana")
	if graf["adminUser"] != "admin" {
		t.Errorf("DSL projection broken when passthrough is absent: %v", graf)
	}
}

// TestProject_Passthrough_NullIsFine pins YAML's `passthrough:` (null)
// case. yaml.Unmarshal turns an empty key into nil; the projection
// must treat nil as absent, NOT crash on the type assertion.
func TestProject_Passthrough_NullIsFine(t *testing.T) {
	values := valuesWith(map[string]any{
		"binding":     "dashboard",
		"type":        "grafana",
		"passthrough": nil,
	})
	res, err := Project(values, fixtureMappings(), "payments")
	if err != nil {
		t.Fatalf("unexpected error on nil passthrough: %v", err)
	}
	if res.ProjectionsCount != 1 {
		t.Errorf("expected 1 projection (nil passthrough acts like absent), got %d", res.ProjectionsCount)
	}
}

// TestProject_Passthrough_OverwritesScalarPreservesSibling pins the
// deep-merge contract: when DSL has set a scalar (e.g. .enabled=true)
// and passthrough sets a map at the same key, the passthrough map
// replaces the scalar. Conversely, sibling scalar fields untouched by
// passthrough must remain.
func TestProject_Passthrough_OverwritesScalarPreservesSibling(t *testing.T) {
	values := valuesWith(map[string]any{
		"binding": "dashboard",
		"type":    "grafana",
		"auth": map[string]any{
			"admin": map[string]any{"name": "admin", "password": "demo"},
		},
		"ingress": map[string]any{"enabled": true},
		"passthrough": map[string]any{
			// Add a NEW sub-key under the SAME ingress block — DSL set
			// `ingress.enabled`, passthrough sets `ingress.annotations`.
			// Both should coexist after the merge.
			"ingress": map[string]any{
				"annotations": map[string]any{
					"kubernetes.io/ingress.class": "nginx",
				},
			},
		},
	})
	res, err := Project(values, fixtureMappings(), "payments")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	graf := digMap(t, res.Overlay, "suse-library", "grafana")
	ing := digMap(t, graf, "ingress")
	if ing["enabled"] != true {
		t.Errorf("DSL ingress.enabled overwritten by passthrough merge: %v", ing)
	}
	ann := digMap(t, ing, "annotations")
	if ann["kubernetes.io/ingress.class"] != "nginx" {
		t.Errorf("passthrough ingress.annotations not merged: %v", ann)
	}
}

// TestProject_ChartDefaults_FillsMissingFields covers NS Phase G
// (rda-cli 0.1.49): chart_defaults in dsl-mappings.yaml writes literal
// fill-in values into the overlay AFTER values_mapping. Used for chart-
// required fields the DSL doesn't surface — typically shape adapters
// between the unified DSL and a chart's specific schema.
//
// The reference case is dex's ingress: the DSL writes
// `ingress.hosts: [str]` (uniform across grafana/prometheus/dex), but
// the dex chart's ingress shape is `hosts: [{host, paths}]`. The
// values_mapping sets hosts[0].host from the DSL string; chart_defaults
// fills hosts[0].paths with the ImplementationSpecific root entry that
// the dex chart requires.
//
// This test pins both: values_mapping landed first, chart_defaults
// landed second, both leaves coexist.
func TestProject_ChartDefaults_FillsMissingFields(t *testing.T) {
	mappings := &dslmapping.Document{
		APIVersion: "rda.suse.com/dsl-mapping/v1alpha1",
		Charts: map[string]dslmapping.ChartEntry{
			"dex": {
				Versions: []dslmapping.VersionEntry{{
					Constraint: ">=0.24.0 <1.0.0",
					Service:    dslmapping.ServiceSpec{Host: "{{ .Release.Name }}-dex", Port: 5556},
					ValuesMapping: map[string]string{
						"ingress.enabled": "dex.ingress.enabled",
						"ingress.host":    "dex.ingress.hosts[0].host",
					},
					ChartDefaults: map[string]any{
						"dex.ingress.hosts[0].paths": []any{
							map[string]any{
								"path":     "/",
								"pathType": "ImplementationSpecific",
							},
						},
					},
				}},
			},
		},
	}
	values := valuesWith(map[string]any{
		"binding": "auth",
		"type":    "dex",
		"ingress": map[string]any{
			"enabled": true,
			"host":    "auth.demo.localtest.me",
		},
	})
	res, err := Project(values, mappings, "payments")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ProjectionsCount != 1 {
		t.Fatalf("expected 1 projection, got %d", res.ProjectionsCount)
	}
	dex := digMap(t, res.Overlay, "suse-library", "dex")
	ing, ok := dex["ingress"].(map[string]any)
	if !ok {
		t.Fatalf("dex.ingress should be a map, got %T", dex["ingress"])
	}
	if ing["enabled"] != true {
		t.Errorf("expected dex.ingress.enabled=true, got %v", ing["enabled"])
	}
	hosts, ok := ing["hosts"].([]any)
	if !ok {
		t.Fatalf("dex.ingress.hosts should be a list, got %T", ing["hosts"])
	}
	if len(hosts) != 1 {
		t.Fatalf("expected 1 host entry, got %d: %v", len(hosts), hosts)
	}
	host0, ok := hosts[0].(map[string]any)
	if !ok {
		t.Fatalf("hosts[0] should be a map, got %T", hosts[0])
	}
	// values_mapping projection landed:
	if host0["host"] != "auth.demo.localtest.me" {
		t.Errorf("expected hosts[0].host=auth.demo.localtest.me, got %v", host0["host"])
	}
	// chart_defaults projection landed:
	paths, ok := host0["paths"].([]any)
	if !ok {
		t.Fatalf("hosts[0].paths should be a list (chart_defaults didn't apply); got %T = %v",
			host0["paths"], host0["paths"])
	}
	if len(paths) != 1 {
		t.Fatalf("expected 1 path entry, got %d: %v", len(paths), paths)
	}
	path0, ok := paths[0].(map[string]any)
	if !ok {
		t.Fatalf("paths[0] should be a map, got %T", paths[0])
	}
	if path0["path"] != "/" {
		t.Errorf("expected paths[0].path=/, got %v", path0["path"])
	}
	if path0["pathType"] != "ImplementationSpecific" {
		t.Errorf("expected paths[0].pathType=ImplementationSpecific, got %v", path0["pathType"])
	}
}

// TestProject_ChartDefaults_AbsentIsFine pins the back-compat path:
// mappings with no chart_defaults block project exactly as before. The
// new field must be optional.
func TestProject_ChartDefaults_AbsentIsFine(t *testing.T) {
	values := valuesWith(map[string]any{
		"binding": "dashboards",
		"type":    "grafana",
		"auth":    map[string]any{"admin": map[string]any{"name": "admin", "password": "p"}},
	})
	res, err := Project(values, fixtureMappings(), "payments")
	if err != nil {
		t.Fatalf("unexpected error on no-chart_defaults mappings: %v", err)
	}
	if res.ProjectionsCount != 1 {
		t.Errorf("expected 1 projection, got %d", res.ProjectionsCount)
	}
	graf := digMap(t, res.Overlay, "suse-library", "grafana")
	if graf["adminUser"] != "admin" {
		t.Errorf("plain projection broken when chart_defaults absent: %v", graf)
	}
}

// TestProject_ChartDefaults_PassthroughOverrides pins the merge order:
// chart_defaults runs BEFORE passthrough, so the user's escape-hatch
// values win on collision. Without this contract, the dev couldn't
// override a chart-required default that the catalog ships (e.g. dex
// devs needing custom path matching for OIDC discovery).
func TestProject_ChartDefaults_PassthroughOverrides(t *testing.T) {
	mappings := &dslmapping.Document{
		APIVersion: "rda.suse.com/dsl-mapping/v1alpha1",
		Charts: map[string]dslmapping.ChartEntry{
			"dex": {
				Versions: []dslmapping.VersionEntry{{
					Constraint: ">=0.24.0 <1.0.0",
					Service:    dslmapping.ServiceSpec{Host: "{{ .Release.Name }}-dex", Port: 5556},
					ValuesMapping: map[string]string{
						"ingress.host": "dex.ingress.hosts[0].host",
					},
					ChartDefaults: map[string]any{
						"dex.ingress.hosts[0].paths": []any{
							map[string]any{"path": "/", "pathType": "ImplementationSpecific"},
						},
					},
				}},
			},
		},
	}
	values := valuesWith(map[string]any{
		"binding": "auth",
		"type":    "dex",
		"ingress": map[string]any{"host": "auth.demo.localtest.me"},
		"passthrough": map[string]any{
			// User overrides the default paths with a Prefix-typed match.
			"ingress": map[string]any{
				"hosts": []any{
					map[string]any{
						"host": "auth.demo.localtest.me",
						"paths": []any{
							map[string]any{"path": "/dex", "pathType": "Prefix"},
						},
					},
				},
			},
		},
	})
	res, err := Project(values, mappings, "payments")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	dex := digMap(t, res.Overlay, "suse-library", "dex")
	ing := digMap(t, dex, "ingress")
	hosts := ing["hosts"].([]any)
	host0 := hosts[0].(map[string]any)
	paths := host0["paths"].([]any)
	if len(paths) != 1 {
		t.Fatalf("expected user passthrough paths, got %d: %v", len(paths), paths)
	}
	path0 := paths[0].(map[string]any)
	// Passthrough should have OVERRIDDEN the chart_defaults entry.
	if path0["path"] != "/dex" {
		t.Errorf("expected paths[0].path=/dex (passthrough wins), got %v", path0["path"])
	}
	if path0["pathType"] != "Prefix" {
		t.Errorf("expected paths[0].pathType=Prefix (passthrough wins), got %v", path0["pathType"])
	}
}

// digMap walks a nested map by key path; fails the test on any missing /
// non-map key. Lifts the boilerplate from each test case.
func digMap(t *testing.T, m map[string]any, keys ...string) map[string]any {
	t.Helper()
	cur := m
	for _, k := range keys {
		next, ok := cur[k].(map[string]any)
		if !ok {
			t.Fatalf("expected map at %q (path %v), got %v (type %T)", k, keys, cur[k], cur[k])
		}
		cur = next
	}
	return cur
}

// equalDeep is a tiny structural-equality check for nested map[string]any /
// []any / scalars. Avoids reflect.DeepEqual to keep the test deps light.
func equalDeep(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			if !equalDeep(v, bv[k]) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !equalDeep(av[i], bv[i]) {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}
