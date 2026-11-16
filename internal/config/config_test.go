package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validConfig = `server:
  listen: ":8080"
  read_timeout: 15s
  write_timeout: 15s
  idle_timeout: 60s
  shutdown_timeout: 10s
proxy:
  timeout: 30s
routes:
  - name: users-api
    host: api.example.com
    path_prefix: /users
    upstream: http://localhost:3001
`

const modernConfig = `server:
  listen: ":8080"
  read_timeout: 15s
  write_timeout: 15s
  idle_timeout: 60s
  shutdown_timeout: 10s
proxy:
  timeout: 30s
routes:
  - name: users-api
    host: api.example.com
    path_prefix: /users
    load_balancer:
      algorithm: weighted_round_robin
    upstreams:
      - url: http://localhost:3001
      - url: http://localhost:3002
        weight: 3
`

func TestLoadValidLegacyConfig(t *testing.T) {
	cfg, err := Load(writeConfig(t, validConfig))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Server.Listen != ":8080" || len(cfg.Routes) != 1 || cfg.Routes[0].Upstream != "http://localhost:3001" {
		t.Fatalf("unexpected configuration: %+v", cfg)
	}
	if got := cfg.Proxy.Timeout.Value().String(); got != "30s" {
		t.Fatalf("proxy timeout = %q, want 30s", got)
	}
	if got := cfg.Routes[0].Algorithm(); got != "round_robin" {
		t.Fatalf("legacy algorithm = %q, want round_robin", got)
	}
	backends := cfg.Routes[0].BackendConfigs()
	if len(backends) != 1 || backends[0].URL != "http://localhost:3001" {
		t.Fatalf("legacy backends = %+v, want one configured backend", backends)
	}
}

func TestLoadModernUpstreamPool(t *testing.T) {
	cfg, err := Load(writeConfig(t, modernConfig))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	backends := cfg.Routes[0].BackendConfigs()
	if len(backends) != 2 || backends[0].Weight != nil || backends[1].Weight == nil || *backends[1].Weight != 3 {
		t.Fatalf("loaded backends = %+v, want two backends with optional weight 3", backends)
	}
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	invalidModern := strings.Replace(modernConfig, "weight: 3", "weight: 0", 1)
	invalidAlgorithm := strings.Replace(modernConfig, "weighted_round_robin", "least_latency", 1)
	mixedBackendFields := validConfig + "    upstreams:\n      - url: http://localhost:3002\n"
	tests := []struct {
		name string
		body string
	}{
		{name: "missing routes", body: strings.Replace(validConfig, "routes:\n  - name: users-api\n    host: api.example.com\n    path_prefix: /users\n    upstream: http://localhost:3001\n", "", 1)},
		{name: "invalid upstream", body: strings.Replace(validConfig, "http://localhost:3001", "ftp://localhost:3001", 1)},
		{name: "invalid duration", body: strings.Replace(validConfig, "timeout: 30s", "timeout: soon", 1)},
		{name: "unknown field", body: validConfig + "unexpected: true\n"},
		{name: "route conflict", body: validConfig + "  - name: duplicate\n    host: API.EXAMPLE.COM\n    path_prefix: /users/\n    upstream: http://localhost:3002\n"},
		{name: "route host contains port", body: strings.Replace(validConfig, "host: api.example.com", "host: api.example.com:8080", 1)},
		{name: "invalid path prefix", body: strings.Replace(validConfig, "path_prefix: /users", "path_prefix: //", 1)},
		{name: "invalid algorithm", body: invalidAlgorithm},
		{name: "non-positive weight", body: invalidModern},
		{name: "mixed upstream fields", body: mixedBackendFields},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, test.body)); err == nil {
				t.Fatal("Load() succeeded for invalid config")
			}
		})
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "proxy.yaml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
