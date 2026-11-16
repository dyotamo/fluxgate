package tests

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dyotamo/fluxgate/internal/proxy"
)

func TestGETForwardingPreservesQueryHeadersAndResponse(t *testing.T) {
	var backendHost string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("backend method = %q, want GET", r.Method)
		}
		if got := r.URL.Path; got != "/items" {
			t.Errorf("backend path = %q, want /items", got)
		}
		if got := r.URL.Query().Get("page"); got != "2" {
			t.Errorf("backend page query = %q, want 2", got)
		}
		if got := r.Header.Get("X-Request-ID"); got != "request-123" {
			t.Errorf("backend request ID = %q, want request-123", got)
		}
		if got := r.Header.Get("X-Forwarded-Proto"); got != "http" {
			t.Errorf("X-Forwarded-Proto = %q, want http", got)
		}
		if got := r.Header.Get("X-Forwarded-Host"); got != "proxy.example" {
			t.Errorf("X-Forwarded-Host = %q, want proxy.example", got)
		}
		if got := r.Header.Get("X-Remove-Me"); got != "" {
			t.Errorf("connection-specific header was forwarded: %q", got)
		}
		if got := r.Host; got != backendHost {
			t.Errorf("outbound Host = %q, want %q", got, backendHost)
		}
		w.Header().Set("X-Backend", "test")
		w.Header().Set("Connection", "X-Internal")
		w.Header().Set("X-Internal", "secret")
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, "backend response")
	}))
	defer backend.Close()
	backendHost = strings.TrimPrefix(backend.URL, "http://")

	client := proxyClient(t, backend.URL, time.Second)
	request, err := http.NewRequest(http.MethodGet, client.URL+"/items?page=2", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "proxy.example"
	request.Header.Set("X-Request-ID", "request-123")
	request.Header.Set("Connection", "X-Remove-Me")
	request.Header.Set("X-Remove-Me", "must-not-forward")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusAccepted || string(body) != "backend response" {
		t.Fatalf("response = %d %q, want 202 %q", response.StatusCode, body, "backend response")
	}
	if got := response.Header.Get("X-Backend"); got != "test" {
		t.Errorf("response X-Backend = %q, want test", got)
	}
	if got := response.Header.Get("X-Internal"); got != "" {
		t.Errorf("connection-specific response header was forwarded: %q", got)
	}
}

func TestPOSTForwardingPreservesBody(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read backend body: %v", err)
		}
		if got := string(body); got != "payload" {
			t.Errorf("backend body = %q, want payload", got)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "created")
	}))
	defer backend.Close()

	client := proxyClient(t, backend.URL, time.Second)
	response, err := http.Post(client.URL+"/submit", "text/plain", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusCreated || string(body) != "created" {
		t.Fatalf("response = %d %q, want 201 %q", response.StatusCode, body, "created")
	}
}

func TestBackendUnavailableReturnsBadGateway(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	backendURL := backend.URL
	backend.Close()

	client := proxyClient(t, backendURL, time.Second)
	response, err := http.Get(client.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", response.StatusCode)
	}
}

func TestUpstreamTimeoutReturnsGatewayTimeout(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = io.WriteString(w, "late")
	}))
	defer backend.Close()

	client := proxyClient(t, backend.URL, 20*time.Millisecond)
	response, err := http.Get(client.URL + "/slow")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", response.StatusCode)
	}
}

func proxyClient(t *testing.T, upstream string, timeout time.Duration) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	forwarder, err := proxy.NewForwarder(upstream, timeout, logger)
	if err != nil {
		t.Fatalf("create forwarder: %v", err)
	}
	server := httptest.NewServer(proxy.NewHandler(forwarder))
	t.Cleanup(server.Close)
	return server
}
