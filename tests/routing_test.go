package tests

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dyotamo/fluxgate/internal/config"
	"github.com/dyotamo/fluxgate/internal/router"
)

func TestRoutingForMultipleHostsAndPaths(t *testing.T) {
	usersBackend := newBackend(t, "users backend")
	ordersBackend := newBackend(t, "orders backend")
	adminBackend := newBackend(t, "admin backend")

	handler, err := router.New([]config.RouteConfig{
		{Name: "users", Host: "api.example.com", PathPrefix: "/users", Upstream: usersBackend.URL},
		{Name: "orders", Host: "api.example.com", PathPrefix: "/orders", Upstream: ordersBackend.URL},
		{Name: "admin", Host: "admin.example.com", PathPrefix: "/", Upstream: adminBackend.URL},
	}, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("create router: %v", err)
	}
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	tests := []struct {
		name       string
		host       string
		path       string
		wantBody   string
		wantStatus int
	}{
		{name: "users route", host: "API.EXAMPLE.COM:8080", path: "/users/list?active=1", wantBody: "users backend: /users/list?active=1", wantStatus: http.StatusOK},
		{name: "orders route", host: "api.example.com", path: "/orders/42", wantBody: "orders backend: /orders/42", wantStatus: http.StatusOK},
		{name: "different hostname", host: "admin.example.com:8080", path: "/dashboard", wantBody: "admin backend: /dashboard", wantStatus: http.StatusOK},
		{name: "path boundary does not match", host: "api.example.com", path: "/usersettings", wantBody: "404 page not found\n", wantStatus: http.StatusNotFound},
		{name: "unknown host", host: "unknown.example.com", path: "/users", wantBody: "404 page not found\n", wantStatus: http.StatusNotFound},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := routedRequest(t, proxyServer.URL, test.host, http.MethodGet, test.path, "")
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatalf("read response body: %v", err)
			}
			if response.StatusCode != test.wantStatus || string(body) != test.wantBody {
				t.Fatalf("response = %d %q, want %d %q", response.StatusCode, body, test.wantStatus, test.wantBody)
			}
		})
	}
}

func TestRoutingPreservesPOSTBodyAndQuery(t *testing.T) {
	backend := newBackend(t, "")
	handler, err := router.New([]config.RouteConfig{
		{Name: "users", Host: "api.example.com", PathPrefix: "/users", Upstream: backend.URL},
	}, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("create router: %v", err)
	}
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	response := routedRequest(t, proxyServer.URL, "api.example.com", http.MethodPost, "/users/create?source=web", "user payload")
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "user payload: /users/create?source=web" {
		t.Fatalf("response = %d %q, want 200 with forwarded path and body", response.StatusCode, body)
	}
}

func newBackend(t *testing.T, name string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		if name != "" {
			_, _ = io.WriteString(w, name+": "+r.URL.RequestURI())
			return
		}
		_, _ = io.WriteString(w, string(body)+": "+r.URL.RequestURI())
	}))
}

func routedRequest(t *testing.T, proxyURL, host, method, path, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, proxyURL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	request.Host = host
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("request proxy: %v", err)
	}
	return response
}
