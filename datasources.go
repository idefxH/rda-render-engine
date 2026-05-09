package render

import (
	"fmt"

	"github.com/idefxH/rda-render-engine/dslmapping"
	"github.com/idefxH/rda-render-engine/errs"
)

func projectDatasources(
	svc map[string]any,
	suseOut map[string]any,
	ver dslmapping.VersionEntry,
	binding string,
	chartAlias string,
	bindings map[string]*BindingFields,
	values map[string]any,
	mappings *dslmapping.Document,
) error {
	dsRaw, ok := svc["datasources"]
	if !ok || dsRaw == nil {
		return nil
	}

	dsList, ok := dsRaw.([]any)
	if !ok {
		return fmt.Errorf("%w: services[binding=%s].datasources must be a list",
			errs.ErrInvocation, binding)
	}

	if len(dsList) == 0 {
		return nil
	}

	var datasources []map[string]any
	for i, dsEntryRaw := range dsList {
		dsEntry, ok := dsEntryRaw.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: services[binding=%s].datasources[%d] must be a map",
				errs.ErrInvocation, binding, i)
		}

		refBinding, _ := dsEntry["binding"].(string)
		if refBinding == "" {
			return fmt.Errorf("%w: services[binding=%s].datasources[%d]: binding field required",
				errs.ErrInvocation, binding, i)
		}

		refSvc, refChartType := findServiceInValues(values, refBinding)
		if refSvc == nil {
			return fmt.Errorf("%w: services[binding=%s].datasources[%d]: binding %q not found in services[]",
				errs.ErrInvocation, binding, i, refBinding)
		}

		if enabled, ok := refSvc["enabled"].(bool); ok && !enabled {
			return fmt.Errorf("%w: services[binding=%s].datasources[%d]: binding %q is disabled",
				errs.ErrInvocation, binding, i, refBinding)
		}

		refBF, ok := bindings[refBinding]
		if !ok {
			return fmt.Errorf("%w: services[binding=%s].datasources[%d]: binding %q has no resolved URL",
				errs.ErrInvocation, binding, i, refBinding)
		}

		refEntry, ok := mappings.Charts[refChartType]
		if !ok || len(refEntry.Versions) == 0 {
			return fmt.Errorf("%w: services[binding=%s].datasources[%d]: chart type %q not in catalog",
				errs.ErrInvocation, binding, i, refChartType)
		}
		refVer := refEntry.Versions[0]

		if refVer.DatasourceType == "" {
			return fmt.Errorf("%w: services[binding=%s].datasources[%d]: chart type %q has no datasource_type in dsl-mappings",
				errs.ErrInvocation, binding, i, refChartType)
		}

		dsName, _ := dsEntry["name"].(string)
		if dsName == "" {
			dsName = refBinding
		}

		dsDefault, _ := dsEntry["default"].(bool)

		dsURL := refBF.URL
		if secretURL, ok := refBF.Secret["url"]; ok && secretURL != "" {
			dsURL = secretURL
		}

		datasources = append(datasources, map[string]any{
			"name":      dsName,
			"type":      refVer.DatasourceType,
			"url":       dsURL,
			"access":    "proxy",
			"isDefault": dsDefault,
		})
	}

	if len(datasources) == 0 {
		return nil
	}

	chartBlock, ok := suseOut[chartAlias].(map[string]any)
	if !ok {
		chartBlock = map[string]any{}
		suseOut[chartAlias] = chartBlock
	}

	// Check for passthrough collision
	if existing, ok := chartBlock["datasources"]; ok && existing != nil {
		return fmt.Errorf("%w: services[binding=%s]: datasources field and passthrough.datasources collide — use one or the other",
			errs.ErrInvocation, binding)
	}

	chartBlock["datasources"] = map[string]any{
		"datasources.yaml": map[string]any{
			"apiVersion":  1,
			"datasources": datasources,
		},
	}

	return nil
}

func findServiceInValues(values map[string]any, binding string) (map[string]any, string) {
	suse, ok := values["suse-library"].(map[string]any)
	if !ok {
		return nil, ""
	}
	servicesRaw, ok := suse["services"].([]any)
	if !ok {
		return nil, ""
	}
	for _, svcRaw := range servicesRaw {
		svc, ok := svcRaw.(map[string]any)
		if !ok {
			continue
		}
		if b, _ := svc["binding"].(string); b == binding {
			chartType, _ := svc["type"].(string)
			return svc, chartType
		}
	}
	return nil, ""
}
