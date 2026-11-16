package tests

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestForwardingPreservesEscapedPath(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.URL.EscapedPath())
	}))
	defer backend.Close()

	client := proxyClient(t, backend.URL, time.Second)
	response, err := http.Get(client.URL + "/encoded/a%2Fb")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); got != "/encoded/a%2Fb" {
		t.Fatalf("escaped path = %q, want /encoded/a%%2Fb", got)
	}
}
