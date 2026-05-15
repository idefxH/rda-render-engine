package render

import (
	"strings"
	"testing"
)

func TestMapToINI_singleSection(t *testing.T) {
	got := mapToINI(map[string]any{
		"server": map[string]any{
			"root_url": "http://dashboard.localtest.me",
		},
	})
	want := "[server]\nroot_url = http://dashboard.localtest.me"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestMapToINI_multipleSections_sorted(t *testing.T) {
	// Two sections, deliberate unsorted insertion. Sections AND
	// keys within sections must come out alphabetically — the
	// projection contract is deterministic output.
	got := mapToINI(map[string]any{
		"server": map[string]any{
			"root_url": "http://dashboard.localtest.me",
		},
		"auth.generic_oauth": map[string]any{
			"client_id": "myproject-dashboard-client",
			"enabled":   "true",
		},
	})
	// auth.generic_oauth sorts before server.
	want := strings.TrimSpace(`
[auth.generic_oauth]
client_id = myproject-dashboard-client
enabled = true

[server]
root_url = http://dashboard.localtest.me
`)
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestMapToINI_boolAndNumberValues(t *testing.T) {
	got := mapToINI(map[string]any{
		"metrics": map[string]any{
			"enabled":  false,
			"interval": 60,
		},
	})
	want := "[metrics]\nenabled = false\ninterval = 60"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSerializeGrafanaIni_convertsNested(t *testing.T) {
	// Simulates what projection produces after wiring:
	//   grafana.grafana\.ini.auth\.generic_oauth.enabled = "true"
	// becomes:
	//   suseOut.grafana["grafana.ini"]["auth.generic_oauth"]["enabled"] = "true"
	overlay := map[string]any{
		"grafana": map[string]any{
			"enabled": true,
			"grafana.ini": map[string]any{
				"auth.generic_oauth": map[string]any{
					"enabled":   "true",
					"client_id": "myproject-dashboard-client",
				},
				"server": map[string]any{
					"root_url": "http://dashboard.localtest.me",
				},
			},
		},
	}
	serializeGrafanaIni(overlay)

	g, _ := overlay["grafana"].(map[string]any)
	v, _ := g["grafana.ini"].(string)
	if v == "" {
		t.Fatalf("grafana.ini was not serialised to a string: got %T %v", g["grafana.ini"], g["grafana.ini"])
	}
	for _, marker := range []string{
		"[auth.generic_oauth]",
		"client_id = myproject-dashboard-client",
		"enabled = true",
		"[server]",
		"root_url = http://dashboard.localtest.me",
	} {
		if !strings.Contains(v, marker) {
			t.Errorf("expected INI string to contain %q, got:\n%s", marker, v)
		}
	}
}

func TestSerializeGrafanaIni_leavesStringValuesAlone(t *testing.T) {
	// When the user wrote a raw INI string via passthrough, leave it
	// untouched — they've opted into managing the INI text themselves.
	raw := "[server]\nroot_url = http://override.example\n"
	overlay := map[string]any{
		"grafana": map[string]any{
			"grafana.ini": raw,
		},
	}
	serializeGrafanaIni(overlay)
	g, _ := overlay["grafana"].(map[string]any)
	if got, _ := g["grafana.ini"].(string); got != raw {
		t.Errorf("string value was modified.\n got: %q\nwant: %q", got, raw)
	}
}

func TestSerializeGrafanaIni_multiInstanceAlias(t *testing.T) {
	// Multi-instance grafana: the chart block is keyed by the alias
	// (e.g. "grafana-metrics"), not the bare "grafana". The walker
	// must still find the nested "grafana.ini" key under the alias.
	overlay := map[string]any{
		"grafana-metrics": map[string]any{
			"grafana.ini": map[string]any{
				"server": map[string]any{
					"root_url": "http://metrics.localtest.me",
				},
			},
		},
	}
	serializeGrafanaIni(overlay)
	a, _ := overlay["grafana-metrics"].(map[string]any)
	if _, ok := a["grafana.ini"].(string); !ok {
		t.Errorf("grafana.ini under aliased chart block was not serialised: got %T", a["grafana.ini"])
	}
}
