package render

import (
	"fmt"

	"github.com/idefxH/rda-render-engine/dslmapping"
)

// projectCRDs processes a service's routes[] field and renders CRD
// entries into suse-library.crds[]. DO-0004 Phase 2.
//
// Each route entry produces one CRD object with the chart's
// crd_projection spec applied. Route targets are resolved:
//   - "self" → the app's own service (releaseName:port)
//   - "<binding>" → resolved from the bindings map
func projectCRDs(
	svc map[string]any,
	suseOut map[string]any,
	ver dslmapping.VersionEntry,
	binding string,
	bindings map[string]*BindingFields,
	releaseName string,
	appPort int,
) error {
	if ver.CRDProjection == nil {
		return nil
	}

	routesRaw, ok := svc["routes"]
	if !ok || routesRaw == nil {
		return nil
	}
	routes, ok := routesRaw.([]any)
	if !ok {
		return fmt.Errorf("services[binding=%s].routes must be a list (got %T)", binding, routesRaw)
	}
	if len(routes) == 0 {
		return nil
	}

	proj := ver.CRDProjection
	var crds []any

	// Read existing crds if any
	if existing, ok := suseOut["crds"].([]any); ok {
		crds = existing
	}

	for i, routeRaw := range routes {
		route, ok := routeRaw.(map[string]any)
		if !ok {
			return fmt.Errorf("services[binding=%s].routes[%d] is not a map", binding, i)
		}

		routeName, _ := route["name"].(string)
		if routeName == "" {
			routeName = fmt.Sprintf("route-%d", i)
		}
		path, _ := route["path"].(string)
		target, _ := route["target"].(string)

		// Resolve target
		var targetHost string
		var targetPort int
		switch target {
		case "self", "":
			targetHost = releaseName
			targetPort = appPort
		default:
			bf, ok := bindings[target]
			if !ok {
				return fmt.Errorf("services[binding=%s].routes[%d].target=%q: binding not found",
					binding, i, target)
			}
			targetHost = bf.Host
			if bf.Port != "" {
				fmt.Sscanf(bf.Port, "%d", &targetPort)
			}
		}

		// Build the CRD object
		crd := map[string]any{
			"apiVersion": proj.GroupVersion,
			"kind":       proj.Kind,
			"metadata": map[string]any{
				"name": fmt.Sprintf("%s-%s-%s", releaseName, binding, routeName),
			},
			"spec": map[string]any{
				"route": map[string]any{
					"name": routeName,
					"path": path,
					"target": map[string]any{
						"host": targetHost,
						"port": targetPort,
					},
				},
			},
		}

		// Include plugins if present
		if plugins, ok := route["plugins"]; ok {
			crd["spec"].(map[string]any)["route"].(map[string]any)["plugins"] = plugins
		}

		crds = append(crds, crd)
	}

	suseOut["crds"] = crds
	return nil
}
