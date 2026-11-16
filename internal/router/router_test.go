package router

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dyotamo/fluxgate/internal/config"
)

func TestMatchHostnameAndPath(t *testing.T) {
	router := newTestRouter(t, []config.RouteConfig{
		{Name: "users", Host: "api.example.com", PathPrefix: "/users", Upstream: "http://localhost:3001"},
		{Name: "root", Host: "other.example.com", PathPrefix: "/", Upstream: "http://localhost:3002"},
	})

	tests := []struct {
		name     string
		host     string
		path     string
		wantName string
	}{
		{name: "exact host", host: "api.example.com", path: "/users", wantName: "users"},
		{name: "case insensitive host", host: "API.EXAMPLE.COM", path: "/users/123", wantName: "users"},
		{name: "ignore host port", host: "api.example.com:8080", path: "/users/123", wantName: "users"},
		{name: "root path", host: "other.example.com", path: "/anything", wantName: "root"},
		{name: "path boundary", host: "api.example.com", path: "/usersettings", wantName: ""},
		{name: "wrong host", host: "unknown.example.com", path: "/users", wantName: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := router.match(test.host, test.path)
			if test.wantName == "" {
				if got != nil {
					t.Fatalf("matched route %q, want no match", got.name)
				}
				return
			}
			if got == nil || got.name != test.wantName {
				t.Fatalf("matched route = %v, want %q", got, test.wantName)
			}
		})
	}
}

func TestMatchPrefersLongestPathPrefix(t *testing.T) {
	router := newTestRouter(t, []config.RouteConfig{
		{Name: "users", Host: "api.example.com", PathPrefix: "/users", Upstream: "http://localhost:3001"},
		{Name: "user-details", Host: "api.example.com", PathPrefix: "/users/details", Upstream: "http://localhost:3002"},
	})
	got := router.match("api.example.com", "/users/details/42")
	if got == nil || got.name != "user-details" {
		t.Fatalf("matched route = %v, want user-details", got)
	}
}

func TestNewRejectsConflictingRoutes(t *testing.T) {
	_, err := New([]config.RouteConfig{
		{Name: "first", Host: "api.example.com", PathPrefix: "/users", Upstream: "http://localhost:3001"},
		{Name: "second", Host: "API.EXAMPLE.COM", PathPrefix: "/users/", Upstream: "http://localhost:3002"},
	}, time.Second, slog.Default())
	if err == nil {
		t.Fatal("New() succeeded with conflicting routes")
	}
}

func TestActiveRequestLeaseReleasedAfterForwardingFailure(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	backendURL := backend.URL
	backend.Close()

	router := newTestRouter(t, []config.RouteConfig{{
		Name:         "failing",
		Host:         "api.example.com",
		PathPrefix:   "/",
		LoadBalancer: config.LoadBalancerConfig{Algorithm: "least_connections"},
		Upstreams:    []config.UpstreamConfig{{URL: backendURL}},
	}})
	request := httptest.NewRequest(http.MethodGet, "http://api.example.com/", nil)
	request.Host = "api.example.com"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", response.Code)
	}
	if active := router.routes[0].backends[0].ActiveRequests(); active != 0 {
		t.Fatalf("active requests after forwarding error = %d, want 0", active)
	}
}

func TestRouterReturnsServiceUnavailableWithoutEligibleBackend(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "unexpected backend request")
	}))
	defer backend.Close()

	router := newTestRouter(t, []config.RouteConfig{{
		Name:       "disabled",
		Host:       "api.example.com",
		PathPrefix: "/",
		Upstream:   backend.URL,
	}})
	router.routes[0].backends[0].SetEligible(false)
	request := httptest.NewRequest(http.MethodGet, "http://api.example.com/", nil)
	request.Host = "api.example.com"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
}

func newTestRouter(t *testing.T, routes []config.RouteConfig) *Router {
	t.Helper()
	router, err := New(routes, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return router
}
