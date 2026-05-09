# rda-render-engine

Shared Go module that projects the unified `services[]` DSL into chart-specific
Helm values overlays. Extracted from
[rda-cli](https://github.com/idefxH/rda-cli) `internal/render` so that
multiple consumers (CLI, Tilt extension, CI tooling) can share a single,
tested render implementation.

## Module path

```
github.com/idefxH/rda-render-engine
```

The root package is `render`. Import it as:

```go
import render "github.com/idefxH/rda-render-engine"
```

## Sub-packages

| Package | Description |
|---------|-------------|
| `dslmapping` | Type definitions for the `dsl-mappings.yaml` catalog (chart entries, version entries, capabilities, scaffolds, etc.) |
| `errs` | Minimal sentinel error values used by the render engine |

## Key entry points

- **`render.Project(values, mappings, releaseName)`** — project services[] DSL into a Helm values overlay (no stage overrides).
- **`render.ProjectWithStage(values, mappings, releaseName, stage)`** — full projection with per-environment stage overrides.
- **`render.ComputeAliases(services)`** — compute chart aliases for multi-instance support.
- **`render.PatchChartDeps(chartYAMLPath, aliases)`** — patch Chart.yaml dependencies with computed aliases.
- **`render.MaintainInlineComments(valuesPath, values, mappings, releaseName, aliases)`** — maintain derived-value annotations in values.yaml.

## Dependencies

- `github.com/Masterminds/semver/v3` — semver constraint matching for version selection
- `golang.org/x/crypto` — bcrypt for capability bootstrap password hashing
- `gopkg.in/yaml.v3` — YAML parsing for Chart.yaml patching and inline comments
