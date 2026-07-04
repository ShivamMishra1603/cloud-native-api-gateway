package router

import (
	"errors"
	"net/http"
	"strings"

	"github.com/ShivamMishra1603/cloud-native-api-gateway/internal/config"
)

var ErrNoRouteMatched = errors.New("no matching route found")

// Route holds a compiled route path mapping.
type Route struct {
	PathPrefix  string // Normalized path prefix (without trailing wildcard or slashes)
	StripPrefix bool
	ServiceName string
}

// Router handles matching incoming requests to registered service routes.
type Router struct {
	routes []Route
}

// New creates a new Router compiled from the configuration.
func New(cfg *config.Config) *Router {
	var routes []Route
	for _, svc := range cfg.Services {
		for _, r := range svc.Routes {
			prefix := normalizePrefix(r.Path)
			routes = append(routes, Route{
				PathPrefix:  prefix,
				StripPrefix: r.StripPrefix,
				ServiceName: svc.Name,
			})
		}
	}
	return &Router{routes: routes}
}

// Match searches for the longest matching route prefix for the given request path.
// It returns the matching Route, or ErrNoRouteMatched if none match.
func (r *Router) Match(req *http.Request) (Route, error) {
	path := req.URL.Path

	var bestMatch Route
	var bestMatchLen int
	found := false

	for _, route := range r.routes {
		if matchPath(path, route.PathPrefix) {
			// Longest prefix match wins (most specific route)
			if len(route.PathPrefix) > bestMatchLen {
				bestMatch = route
				bestMatchLen = len(route.PathPrefix)
				found = true
			}
		}
	}

	if found {
		return bestMatch, nil
	}

	return Route{}, ErrNoRouteMatched
}

// normalizePrefix trims trailing wildcards (*) and slashes (/) to normalize the prefix.
func normalizePrefix(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimSuffix(p, "*")
	p = strings.TrimSuffix(p, "/")
	if p == "" {
		return "/"
	}
	return p
}

// matchPath returns true if the path is either an exact match or a child of the prefix.
// Example: prefix "/catalog" matches "/catalog", "/catalog/", and "/catalog/products",
// but does not match "/catalog-other".
func matchPath(path, prefix string) bool {
	if prefix == "/" {
		return true // Root matches anything
	}
	if path == prefix {
		return true
	}
	return strings.HasPrefix(path, prefix+"/")
}
