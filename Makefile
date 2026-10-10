.PHONY: build test test-short race lint

build:
	CGO_ENABLED=0 go build -trimpath -o bin/qh ./cmd/qh

test:
	go test ./...

test-short:
	go test -short ./...

race:
	go test -race ./...

lint:
	go vet ./...
