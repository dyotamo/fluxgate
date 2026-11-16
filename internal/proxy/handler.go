package proxy

import "net/http"

func NewHandler(forwarder *Forwarder) http.Handler {
	return forwarder
}
