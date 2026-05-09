// capabilities.go — Phase 2.2 of Design Orientation 0001: render-time
// projection of services[].bootstrap.<cap>: lists into the chart's
// values overlay (file-static / file-initdb backends) or stage them
// for Phase 2 V2+ (api-job / k8s-resource).
//
// Reads:
//   - svc["bootstrap"] map of cap-name → list of items (the user's DSL)
//   - ver.Capabilities map of cap-name → CapabilitySpec (per-chart
//     materialisation contract from dsl-mappings, Phase 2.1 / #114)
//
// Writes into chartBlock the chart's values shape:
//   - file-static: Target path = list of items
//   - file-initdb: Target path = map of KeyTemplate-rendered keys → ValueSource content
//
// Errors carry binding + cap + item context so messages point at the
// dev's values.yaml (the artifact they edit). See rda.md
// BEHAVIOR/INTERNAL: project-capabilities for the full step list.

package render

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"golang.org/x/crypto/bcrypt"

	"github.com/idefxH/rda-render-engine/dslmapping"
	"github.com/idefxH/rda-render-engine/errs"
)

// projectCapabilities materialises the user's bootstrap.<cap> lists
// for one binding. Mutates chartBlock in place with the projected
// values. Returns warnings (chart-author bug strings the caller
// should print to stderr) and an error on validation / unsupported
// backend / unknown backend.
//
// Forward-compat behaviour:
//   - svc.bootstrap absent / nil  → return cleanly (the user simply
//     hasn't declared anything for this binding).
//   - cap declared in user DSL but absent from spec.Capabilities →
//     warning, skip the cap.
//   - cap declared in spec.Capabilities but absent from user DSL →
//     no-op (that's just "no items to project").
func projectCapabilities(
	svc map[string]any,
	chartBlock map[string]any,
	ver dslmapping.VersionEntry,
	binding, chartType, projectDir string,
) (warnings []string, err error) {
	bootstrapRaw, ok := svc["bootstrap"]
	if !ok || bootstrapRaw == nil {
		return nil, nil
	}
	bootstrap, ok := bootstrapRaw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: services[binding=%s].bootstrap must be a map (got %T)",
			errs.ErrInvocation, binding, bootstrapRaw)
	}
	if len(bootstrap) == 0 {
		return nil, nil
	}

	// Build the work list in spec.Order ascending — caps must run
	// in deterministic order (db.schemas before db.seeds, etc).
	type todo struct {
		name string
		spec dslmapping.CapabilitySpec
		raw  any
	}
	work := make([]todo, 0, len(bootstrap))
	for capName, raw := range bootstrap {
		// Skip the legacy bootstrap.jobs[] mechanism — orthogonal,
		// projected by projectBootstrapJobs (Phase 2 will fold it
		// in as one more capability; out of 2.2 scope).
		if capName == "jobs" {
			continue
		}
		spec, found := ver.Capabilities[capName]
		if !found {
			warnings = append(warnings,
				fmt.Sprintf("services[binding=%s,type=%s]: capability %q not declared in dsl-mappings — skipping",
					binding, chartType, capName))
			continue
		}
		work = append(work, todo{name: capName, spec: spec, raw: raw})
	}
	// Stable sort by Order; ties broken by capName for determinism.
	sort.SliceStable(work, func(i, j int) bool {
		oi := work[i].spec.Order
		oj := work[j].spec.Order
		if oi == 0 {
			oi = 100
		}
		if oj == 0 {
			oj = 100
		}
		if oi != oj {
			return oi < oj
		}
		return work[i].name < work[j].name
	})

	for _, t := range work {
		if t.raw == nil {
			// `bootstrap.<cap>: ~` — explicit drop. Seen in
			// `overrides.<stage>.bootstrap.<cap>: ~`.
			continue
		}
		items, ok := t.raw.([]any)
		if !ok {
			return warnings, fmt.Errorf(
				"%w: services[binding=%s,type=%s].bootstrap.%s must be a list of items (got %T — did you forget the `- ` list marker?)",
				errs.ErrInvocation, binding, chartType, t.name, t.raw)
		}

		// Validate + transform each item, then dispatch by Backend.
		validated, err := validateAndTransform(items, t.spec, binding, t.name, projectDir)
		if err != nil {
			return warnings, err
		}

		switch t.spec.Backend {
		case dslmapping.BackendFileStatic:
			if err := applyFileStatic(chartBlock, t.spec.Projection, validated); err != nil {
				return warnings, fmt.Errorf("services[binding=%s].bootstrap.%s: %w",
					binding, t.name, err)
			}
		case dslmapping.BackendFileInitDB:
			if err := applyFileInitDB(chartBlock, t.spec.Projection, validated); err != nil {
				return warnings, fmt.Errorf("services[binding=%s].bootstrap.%s: %w",
					binding, t.name, err)
			}
		case dslmapping.BackendAPIJob, dslmapping.BackendK8sResource:
			return warnings, fmt.Errorf(
				"%w: services[binding=%s,type=%s].bootstrap.%s declares backend %q which Phase 2.2 does not yet ship — pin to a future bundle once 2.2 V2 lands, or use file-static / file-initdb for now",
				errs.ErrCapabilityBackendUnsupported, binding, chartType, t.name, t.spec.Backend)
		default:
			return warnings, fmt.Errorf(
				"%w: services[binding=%s,type=%s].bootstrap.%s declares backend %q which is not one of file-static / file-initdb / api-job / k8s-resource — typo?",
				errs.ErrCapabilityBackendUnknown, binding, chartType, t.name, t.spec.Backend)
		}
	}
	return warnings, nil
}

// validateAndTransform walks the items list, validates each against
// spec.Schema (Required + Default), then applies spec.Projection.Transform
// (bcrypt-password-to-hash, inline-or-file). Returns a fresh list of
// post-transform items so the caller can project without mutating
// the original.
//
// projectDir is the project root (chart_dir/..) used by the
// inline-or-file transform to resolve relative paths in `sqlFile:`.
func validateAndTransform(
	items []any,
	spec dslmapping.CapabilitySpec,
	binding, capName, projectDir string,
) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(items))
	for i, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf(
				"%w: services[binding=%s].bootstrap.%s[%d] must be a map (got %T)",
				errs.ErrInvocation, binding, capName, i, raw)
		}
		// Defensive copy — don't mutate the caller's data.
		copy := map[string]any{}
		for k, v := range item {
			copy[k] = v
		}

		// Schema validation.
		for fieldName, fieldSchema := range spec.Schema {
			val, present := copy[fieldName]
			if !present || isEmptyVal(val) {
				if fieldSchema.Default != "" {
					copy[fieldName] = fieldSchema.Default
					continue
				}
				if fieldSchema.Required {
					return nil, fmt.Errorf(
						"%w: services[binding=%s].bootstrap.%s[%d]: required field %q is missing or empty",
						errs.ErrInvocation, binding, capName, i, fieldName)
				}
			}
		}

		// Transforms.
		if err := applyTransform(copy, spec.Projection.Transform, projectDir); err != nil {
			return nil, fmt.Errorf(
				"services[binding=%s].bootstrap.%s[%d]: transform %q: %w",
				binding, capName, i, spec.Projection.Transform, err)
		}

		out = append(out, copy)
	}
	return out, nil
}

func isEmptyVal(v any) bool {
	if v == nil {
		return true
	}
	if s, ok := v.(string); ok {
		return s == ""
	}
	return false
}

// applyTransform mutates `item` in place per the named transform.
// Empty / unknown transform names are no-ops (forward-compat — bundle
// authors can land new transforms without breaking older rda-cli).
func applyTransform(item map[string]any, name, projectDir string) error {
	switch name {
	case "":
		return nil
	case "bcrypt-password-to-hash":
		return transformBcrypt(item)
	case "inline-or-file":
		return transformInlineOrFile(item, projectDir)
	default:
		// Forward-compat: unknown transform leaves the item alone.
		// The chart-side validation catches missing fields at
		// helm-template time; we don't fail loud here so authors
		// can stage new transforms.
		return nil
	}
}

// transformBcrypt replaces item.password with item.hash =
// bcrypt(password). Used by dex auth.users — dex's
// staticPasswords schema expects `hash:` not `password:`.
func transformBcrypt(item map[string]any) error {
	pwRaw, ok := item["password"]
	if !ok {
		// Item with `hash:` already present (the dev pre-bcrypted
		// in their values.yaml) is allowed — no work to do.
		return nil
	}
	pw, ok := pwRaw.(string)
	if !ok {
		return fmt.Errorf("password field must be a string (got %T)", pwRaw)
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("bcrypt: %w", err)
	}
	item["hash"] = string(hashed)
	delete(item, "password")
	return nil
}

// transformInlineOrFile populates item.sql from item.sqlFile (read
// relative to projectDir). Items already carrying item.sql pass
// through unchanged.
func transformInlineOrFile(item map[string]any, projectDir string) error {
	if _, hasInline := item["sql"]; hasInline {
		return nil
	}
	rawPath, ok := item["sqlFile"]
	if !ok {
		// Neither sql nor sqlFile — let schema validation catch it
		// (the dev probably typo'd). Don't error here; the cap's
		// schema usually has Required:true on `name` but leaves
		// sql/sqlFile optional individually.
		return nil
	}
	pathStr, ok := rawPath.(string)
	if !ok {
		return fmt.Errorf("sqlFile must be a string path (got %T)", rawPath)
	}
	abs := pathStr
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(projectDir, pathStr)
	}
	contents, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Errorf("read sqlFile %s: %w", abs, err)
	}
	item["sql"] = string(contents)
	delete(item, "sqlFile")
	return nil
}

// applyFileStatic projects items as a list under spec.Target. Each
// item is renamed via spec.FieldMap (identity when empty). Multiple
// caps targeting the same path append; first writer wins on the list.
func applyFileStatic(chartBlock map[string]any, spec dslmapping.ProjectionSpec, items []map[string]any) error {
	if spec.Target == "" {
		return errors.New("file-static backend requires a non-empty target")
	}
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, renameItem(item, spec.FieldMap))
	}
	if err := setAtPath(chartBlock, spec.Target, out); err != nil {
		return fmt.Errorf("project to %s: %w", spec.Target, err)
	}
	return nil
}

// applyFileInitDB projects items as a map under spec.Target. The
// map key is rendered from spec.KeyTemplate against the item; the
// value is the field named by spec.ValueSource. "sql_or_file"
// resolves to the `sql` field (after the inline-or-file transform).
func applyFileInitDB(chartBlock map[string]any, spec dslmapping.ProjectionSpec, items []map[string]any) error {
	if spec.Target == "" {
		return errors.New("file-initdb backend requires a non-empty target")
	}
	if spec.KeyTemplate == "" {
		return errors.New("file-initdb backend requires a non-empty key_template")
	}
	if spec.ValueSource == "" {
		return errors.New("file-initdb backend requires a non-empty value_source")
	}
	valueField := spec.ValueSource
	if valueField == "sql_or_file" {
		valueField = "sql"
	}

	// Find or initialise the target map. Multiple caps may target
	// the same path (db.schemas + db.seeds → both write to
	// postgresql.primary.initdb.scripts), so we accumulate.
	existing := digMapByPath(chartBlock, spec.Target)
	out := map[string]any{}
	if existing != nil {
		for k, v := range existing {
			out[k] = v
		}
	}

	tpl, err := template.New(spec.Target).Parse(spec.KeyTemplate)
	if err != nil {
		return fmt.Errorf("parse key_template %q: %w", spec.KeyTemplate, err)
	}
	for _, item := range items {
		renamed := renameItem(item, spec.FieldMap)
		var buf bytes.Buffer
		if err := tpl.Execute(&buf, renamed); err != nil {
			return fmt.Errorf("render key_template: %w", err)
		}
		key := buf.String()
		val, ok := renamed[valueField]
		if !ok {
			return fmt.Errorf("item missing field %q (value_source)", valueField)
		}
		out[key] = val
	}
	if err := setAtPath(chartBlock, spec.Target, out); err != nil {
		return fmt.Errorf("project to %s: %w", spec.Target, err)
	}
	return nil
}

// renameItem returns a shallow copy of item with keys renamed per
// the field_map. Map key is user-side, value is chart-side. Empty
// field_map is identity.
func renameItem(item map[string]any, fieldMap map[string]string) map[string]any {
	if len(fieldMap) == 0 {
		// Defensive copy so the caller can't mutate our output.
		out := map[string]any{}
		for k, v := range item {
			out[k] = v
		}
		return out
	}
	out := map[string]any{}
	for k, v := range item {
		if newK, mapped := fieldMap[k]; mapped {
			out[newK] = v
		} else {
			out[k] = v
		}
	}
	return out
}

// digMapByPath walks chartBlock by dotted path and returns the map at
// that path, or nil when absent / wrong shape. Used by file-initdb
// to merge multiple caps into the same target.
func digMapByPath(m map[string]any, dottedPath string) map[string]any {
	keys := strings.Split(dottedPath, ".")
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		v, exists := mm[k]
		if !exists {
			return nil
		}
		cur = v
	}
	out, _ := cur.(map[string]any)
	return out
}
