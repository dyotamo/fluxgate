.PHONY: build run test

build:
	mkdir -p bin
	go build -o bin/fluxgate ./cmd/proxy

run:
	go run ./cmd/proxy -config configs/proxy.yaml

test:
	go test ./...
