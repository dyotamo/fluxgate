package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Forwarder struct {
	upstream *url.URL
	client   *http.Client
	logger   *slog.Logger
}

func NewForwarder(upstream string, timeout time.Duration, logger *slog.Logger) (*Forwarder, error) {
	parsed, err := url.Parse(upstream)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("invalid upstream URL")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Forwarder{
		upstream: parsed,
		client:   &http.Client{Timeout: timeout},
		logger:   logger,
	}, nil
}

func (f *Forwarder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	target := *f.upstream
	target.Path, target.RawPath = joinEscapedURLPath(f.upstream.EscapedPath(), r.URL.EscapedPath())
	target.RawQuery = r.URL.RawQuery

	outbound, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
	if err != nil {
		f.writeError(w, http.StatusBadGateway, "failed to create upstream request", "create upstream request", err)
		return
	}
	outbound.ContentLength = r.ContentLength
	copyRequestHeaders(outbound.Header, r.Header)
	outbound.Host = f.upstream.Host

	clientIP := remoteIP(r.RemoteAddr)
	if clientIP != "" {
		if prior := r.Header.Get("X-Forwarded-For"); prior != "" {
			clientIP = prior + ", " + clientIP
		}
		outbound.Header.Set("X-Forwarded-For", clientIP)
	}
	if r.TLS == nil {
		outbound.Header.Set("X-Forwarded-Proto", "http")
	} else {
		outbound.Header.Set("X-Forwarded-Proto", "https")
	}
	outbound.Header.Set("X-Forwarded-Host", r.Host)

	response, err := f.client.Do(outbound)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		status := http.StatusBadGateway
		message := "upstream request failed"
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			status = http.StatusGatewayTimeout
			message = "upstream request timed out"
		}
		f.writeError(w, status, message, "forward request", err)
		return
	}
	defer response.Body.Close()

	copyResponseHeaders(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	if _, err := io.Copy(w, response.Body); err != nil {
		f.logger.Error("copy upstream response body", "error", err)
	}
}

func (f *Forwarder) writeError(w http.ResponseWriter, status int, message, operation string, err error) {
	f.logger.Error(operation, "error", err)
	http.Error(w, message, status)
}

func copyRequestHeaders(destination, source http.Header) {
	connectionHeaders := connectionHeaderNames(source)
	for name, values := range source {
		if isHopByHop(name) || strings.EqualFold(name, "Host") || strings.EqualFold(name, "Content-Length") || connectionHeaders[strings.ToLower(name)] {
			continue
		}
		for _, value := range values {
			destination.Add(name, value)
		}
	}
}

func copyResponseHeaders(destination, source http.Header) {
	connectionHeaders := connectionHeaderNames(source)
	for name, values := range source {
		if isHopByHop(name) || connectionHeaders[strings.ToLower(name)] {
			continue
		}
		for _, value := range values {
			destination.Add(name, value)
		}
	}
}

func connectionHeaderNames(headers http.Header) map[string]bool {
	names := make(map[string]bool)
	for _, value := range headers.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			name = strings.TrimSpace(name)
			if name != "" {
				names[strings.ToLower(name)] = true
			}
		}
	}
	return names
}

func isHopByHop(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "proxy-connection", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func remoteIP(remoteAddress string) string {
	if host, _, err := net.SplitHostPort(remoteAddress); err == nil {
		return host
	}
	return remoteAddress
}

func joinEscapedURLPath(base, request string) (string, string) {
	var joined string
	switch {
	case base == "" || base == "/":
		joined = request
	case request == "" || request == "/":
		joined = base
	default:
		joined = strings.TrimRight(base, "/") + "/" + strings.TrimLeft(request, "/")
	}

	path, err := url.PathUnescape(joined)
	if err != nil {
		return joined, ""
	}
	if path == joined {
		return path, ""
	}
	return path, joined
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
