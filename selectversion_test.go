package render

import (
	"testing"

	"github.com/idefxH/rda-render-engine/dslmapping"
)

func TestSelectVersion_NoBranch(t *testing.T) {
	entry := dslmapping.ChartEntry{
		Versions: []dslmapping.VersionEntry{
			{Constraint: ">=0.4.0 <1.0.0"},
			{Constraint: ">=0.3.0 <0.4.0"},
		},
	}
	got := selectVersion(entry, map[string]any{})
	if got.Constraint != ">=0.4.0 <1.0.0" {
		t.Errorf("expected first version, got %q", got.Constraint)
	}
}

func TestSelectVersion_DefaultBranch(t *testing.T) {
	entry := dslmapping.ChartEntry{
		DefaultBranch: "17",
		Branches: map[string]dslmapping.BranchEntry{
			"17": {ChartVersion: "0.4.4-29.1"},
			"16": {ChartVersion: "0.3.9-28.1"},
		},
		Versions: []dslmapping.VersionEntry{
			{Constraint: ">=0.4.0 <1.0.0"},
			{Constraint: ">=0.3.0 <0.4.0"},
		},
	}
	got := selectVersion(entry, map[string]any{})
	if got.Constraint != ">=0.4.0 <1.0.0" {
		t.Errorf("default branch 17 should match first version, got %q", got.Constraint)
	}
}

func TestSelectVersion_ExplicitBranch(t *testing.T) {
	entry := dslmapping.ChartEntry{
		DefaultBranch: "17",
		Branches: map[string]dslmapping.BranchEntry{
			"17": {ChartVersion: "0.4.4-29.1"},
			"16": {ChartVersion: "0.3.9-28.1"},
		},
		Versions: []dslmapping.VersionEntry{
			{Constraint: ">=0.4.0 <1.0.0"},
			{Constraint: ">=0.3.0 <0.4.0"},
		},
	}
	got := selectVersion(entry, map[string]any{"branch": "16"})
	if got.Constraint != ">=0.3.0 <0.4.0" {
		t.Errorf("branch 16 should match second version, got %q", got.Constraint)
	}
}

func TestSelectVersion_UnknownBranch_FallsBack(t *testing.T) {
	entry := dslmapping.ChartEntry{
		Branches: map[string]dslmapping.BranchEntry{
			"17": {ChartVersion: "0.4.4-29.1"},
		},
		Versions: []dslmapping.VersionEntry{
			{Constraint: ">=0.4.0 <1.0.0"},
		},
	}
	got := selectVersion(entry, map[string]any{"branch": "99"})
	if got.Constraint != ">=0.4.0 <1.0.0" {
		t.Errorf("unknown branch should fallback to first version, got %q", got.Constraint)
	}
}

func TestSelectVersion_EmptyVersions(t *testing.T) {
	entry := dslmapping.ChartEntry{}
	got := selectVersion(entry, map[string]any{})
	if got.Constraint != "" {
		t.Errorf("empty versions should return zero value, got %q", got.Constraint)
	}
}
