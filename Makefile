.PHONY: build test vet fmt

build:
	go build -o bin/yas ./cmd/yas

test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w cmd internal
