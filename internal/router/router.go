package router

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/dyotamo/fluxgate/internal/balancer"
	"github.com/dyotamo/fluxgate/internal/config"
	"github.com/dyotamo/fluxgate/internal/proxy"
)

type Router struct {
	routes []route
	logger *slog.Logger
}

type route struct {
	name       string
	host       string
	pathPrefix string
	algorithm  balancer.Algorithm
	pool       balancer.Balancer
	backends   []*balancer.Backend
	forwarders map[string]*proxy.Forwarder
}

func New(routes []config.RouteConfig, timeout time.Duration, logger *slog.Logger) (*Router, error) {
	if err := config.ValidateRoutes(routes); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("proxy timeout must be greater than zero")
	}
	if logger == nil {
		logger = slog.Default()
	}

	result := &Router{logger: logger, routes: make([]route, 0, len(routes))}
	for _, configured := range routes {
		algorithm, err := balancer.ParseAlgorithm(configured.Algorithm())
		if err != nil {
			return nil, fmt.Errorf("route %q: %w", configured.Name, err)
		}
		backends := configured.BackendConfigs()
		backendModels := make([]*balancer.Backend, 0, len(backends))
		forwarders := make(map[string]*proxy.Forwarder, len(backends))
		for index, configuredBackend := range backends {
			weight := 1
			if configuredBackend.Weight != nil {
				weight = *configuredBackend.Weight
			}
			backendID := fmt.Sprintf("%s-%d", configured.Name, index+1)
			backend, err := balancer.NewBackend(backendID, configuredBackend.URL, weight)
			if err != nil {
				return nil, fmt.Errorf("route %q backend %d: %w", configured.Name, index+1, err)
			}
			forwarder, err := proxy.NewForwarder(backend.URL, timeout, logger)
			if err != nil {
				return nil, fmt.Errorf("create forwarder for backend %q: %w", backend.ID, err)
			}
			backendModels = append(backendModels, backend)
			forwarders[backend.ID] = forwarder
		}
		pool, err := balancer.New(algorithm, backendModels)
		if err != nil {
			return nil, fmt.Errorf("create load balancer for route %q: %w", configured.Name, err)
		}
		result.routes = append(result.routes, route{
			name:       configured.Name,
			host:       normalizeHostname(configured.Host),
			pathPrefix: normalizePathPrefix(configured.PathPrefix),
			algorithm:  algorithm,
			pool:       pool,
			backends:   backendModels,
			forwarders: forwarders,
		})
	}
	return result, nil
}

func (r *Router) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	selectedRoute := r.match(request.Host, request.URL.Path)
	if selectedRoute == nil {
		r.logger.Warn("no route matched", "host", request.Host, "path", request.URL.Path)
		http.NotFound(w, request)
		return
	}

	selection, err := selectedRoute.pool.Next()
	if err != nil {
		r.logger.Error("backend selection failed", "route", selectedRoute.name, "algorithm", selectedRoute.algorithm, "error", err)
		http.Error(w, "no backend available", http.StatusServiceUnavailable)
		return
	}
	defer selection.Release()

	backend := selection.Backend
	r.logger.Info("selected backend", "route", selectedRoute.name, "algorithm", selectedRoute.algorithm, "backend", backend.ID, "upstream", backend.URL)
	selectedRoute.forwarders[backend.ID].ServeHTTP(w, request)
}

func (r *Router) match(host, path string) *route {
	host = requestHostname(host)
	var selected *route
	for i := range r.routes {
		candidate := &r.routes[i]
		if candidate.host != host || !pathMatches(candidate.pathPrefix, path) {
			continue
		}
		if selected == nil || len(candidate.pathPrefix) > len(selected.pathPrefix) {
			selected = candidate
		}
	}
	return selected
}

func pathMatches(prefix, path string) bool {
	if prefix == "/" {
		return strings.HasPrefix(path, "/")
	}
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func requestHostname(host string) string {
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		return normalizeHostname(parsedHost)
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	return normalizeHostname(host)
}

func normalizeHostname(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

func normalizePathPrefix(prefix string) string {
	if prefix != "/" {
		return strings.TrimRight(prefix, "/")
	}
	return prefix
}
