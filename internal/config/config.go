package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dyotamo/fluxgate/internal/balancer"
	"gopkg.in/yaml.v3"
)

type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("duration must be a scalar string")
	}

	parsed, err := time.ParseDuration(node.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", node.Value, err)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) Value() time.Duration {
	return time.Duration(d)
}

type Config struct {
	Server ServerConfig  `yaml:"server"`
	Proxy  ProxyConfig   `yaml:"proxy"`
	Routes []RouteConfig `yaml:"routes"`
}

type ServerConfig struct {
	Listen          string   `yaml:"listen"`
	ReadTimeout     Duration `yaml:"read_timeout"`
	WriteTimeout    Duration `yaml:"write_timeout"`
	IdleTimeout     Duration `yaml:"idle_timeout"`
	ShutdownTimeout Duration `yaml:"shutdown_timeout"`
}

type ProxyConfig struct {
	Timeout Duration `yaml:"timeout"`
}

type RouteConfig struct {
	Name         string             `yaml:"name"`
	Host         string             `yaml:"host"`
	PathPrefix   string             `yaml:"path_prefix"`
	Upstream     string             `yaml:"upstream,omitempty"`
	LoadBalancer LoadBalancerConfig `yaml:"load_balancer"`
	Upstreams    []UpstreamConfig   `yaml:"upstreams,omitempty"`
}

type LoadBalancerConfig struct {
	Algorithm string `yaml:"algorithm"`
}

type UpstreamConfig struct {
	URL    string `yaml:"url"`
	Weight *int   `yaml:"weight,omitempty"`
}

func (r RouteConfig) BackendConfigs() []UpstreamConfig {
	if strings.TrimSpace(r.Upstream) != "" {
		return []UpstreamConfig{{URL: r.Upstream}}
	}
	return append([]UpstreamConfig(nil), r.Upstreams...)
}

func (r RouteConfig) Algorithm() string {
	if strings.TrimSpace(r.LoadBalancer.Algorithm) == "" {
		return string(balancer.RoundRobin)
	}
	return strings.ToLower(strings.TrimSpace(r.LoadBalancer.Algorithm))
}

func (c Config) Validate() error {
	if c.Server.Listen == "" {
		return fmt.Errorf("server.listen is required")
	}
	_, port, err := net.SplitHostPort(c.Server.Listen)
	if err != nil || port == "" {
		return fmt.Errorf("server.listen must be a valid host:port address")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("server.listen port must be between 1 and 65535")
	}

	for name, duration := range map[string]Duration{
		"server.read_timeout":     c.Server.ReadTimeout,
		"server.write_timeout":    c.Server.WriteTimeout,
		"server.idle_timeout":     c.Server.IdleTimeout,
		"server.shutdown_timeout": c.Server.ShutdownTimeout,
		"proxy.timeout":           c.Proxy.Timeout,
	} {
		if duration.Value() <= 0 {
			return fmt.Errorf("%s must be greater than zero", name)
		}
	}

	return ValidateRoutes(c.Routes)
}

func ValidateRoutes(routes []RouteConfig) error {
	if len(routes) == 0 {
		return fmt.Errorf("at least one route is required")
	}

	names := make(map[string]bool, len(routes))
	matches := make(map[string]bool, len(routes))
	for i, route := range routes {
		if strings.TrimSpace(route.Name) == "" {
			return fmt.Errorf("routes[%d].name is required", i)
		}
		if names[route.Name] {
			return fmt.Errorf("route name %q is duplicated", route.Name)
		}
		names[route.Name] = true

		host := normalizeHostname(route.Host)
		if !validHostname(host) {
			return fmt.Errorf("routes[%d].host must be a valid exact hostname without a port", i)
		}

		prefix := normalizePathPrefix(route.PathPrefix)
		if !validPathPrefix(route.PathPrefix) {
			return fmt.Errorf("routes[%d].path_prefix must be an absolute path without a query or fragment", i)
		}

		if _, err := balancer.ParseAlgorithm(route.Algorithm()); err != nil {
			return fmt.Errorf("routes[%d].load_balancer.algorithm: %w", i, err)
		}

		if strings.TrimSpace(route.Upstream) != "" && len(route.Upstreams) > 0 {
			return fmt.Errorf("routes[%d] must use either upstream or upstreams, not both", i)
		}
		upstreams := route.BackendConfigs()
		if len(upstreams) == 0 {
			return fmt.Errorf("routes[%d] must configure an upstream or at least one backend in upstreams", i)
		}
		for backendIndex, backend := range upstreams {
			upstream, err := url.Parse(backend.URL)
			if err != nil || (upstream.Scheme != "http" && upstream.Scheme != "https") || upstream.Host == "" {
				return fmt.Errorf("routes[%d].upstreams[%d].url must be an absolute http or https URL", i, backendIndex)
			}
			if upstream.User != nil || upstream.RawQuery != "" || upstream.Fragment != "" {
				return fmt.Errorf("routes[%d].upstreams[%d].url must not contain credentials, a query, or a fragment", i, backendIndex)
			}
			if backend.Weight != nil && *backend.Weight <= 0 {
				return fmt.Errorf("routes[%d].upstreams[%d].weight must be greater than zero", i, backendIndex)
			}
		}

		matchKey := host + "\x00" + prefix
		if matches[matchKey] {
			return fmt.Errorf("routes[%d] conflicts with another route for host %q and path_prefix %q", i, host, prefix)
		}
		matches[matchKey] = true
	}
	return nil
}

func normalizeHostname(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

func validHostname(host string) bool {
	if host == "" || len(host) > 253 || strings.ContainsAny(host, " \t\r\n/?#*@") {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return false
			}
		}
	}
	return true
}

func normalizePathPrefix(prefix string) string {
	if prefix != "/" {
		return strings.TrimRight(prefix, "/")
	}
	return prefix
}

func validPathPrefix(prefix string) bool {
	return normalizePathPrefix(prefix) != "" && strings.HasPrefix(prefix, "/") && !strings.ContainsAny(prefix, "?#")
}
