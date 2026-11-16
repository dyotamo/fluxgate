package tests

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dyotamo/fluxgate/internal/config"
	"github.com/dyotamo/fluxgate/internal/router"
)

func TestRoundRobinIntegrationAndIndependentRoutePools(t *testing.T) {
	usersOne := namedBackend(t, "users-1")
	usersTwo := namedBackend(t, "users-2")
	ordersOne := namedBackend(t, "orders-1")
	ordersTwo := namedBackend(t, "orders-2")

	handler, err := router.New([]config.RouteConfig{
		{
			Name:         "users",
			Host:         "api.example.com",
			PathPrefix:   "/users",
			LoadBalancer: config.LoadBalancerConfig{Algorithm: "round_robin"},
			Upstreams:    []config.UpstreamConfig{{URL: usersOne.URL}, {URL: usersTwo.URL}},
		},
		{
			Name:         "orders",
			Host:         "api.example.com",
			PathPrefix:   "/orders",
			LoadBalancer: config.LoadBalancerConfig{Algorithm: "round_robin"},
			Upstreams:    []config.UpstreamConfig{{URL: ordersOne.URL}, {URL: ordersTwo.URL}},
		},
	}, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("create router: %v", err)
	}
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	for index, want := range []string{"users-1", "users-2", "users-1"} {
		response := routedRequest(t, proxyServer.URL, "api.example.com", http.MethodGet, "/users/list", "")
		body := readAndClose(t, response)
		if response.StatusCode != http.StatusOK || body != want {
			t.Errorf("users request %d = %d %q, want 200 %q", index+1, response.StatusCode, body, want)
		}
	}
	for index, want := range []string{"orders-1", "orders-2", "orders-1"} {
		response := routedRequest(t, proxyServer.URL, "api.example.com", http.MethodGet, "/orders/list", "")
		body := readAndClose(t, response)
		if response.StatusCode != http.StatusOK || body != want {
			t.Errorf("orders request %d = %d %q, want 200 %q", index+1, response.StatusCode, body, want)
		}
	}
}

func namedBackend(t *testing.T, name string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, name)
	}))
}

func readAndClose(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return string(body)
}
