.PHONY: build install test vet cover cover-gate docker lint fmt

PREFIX ?= $(HOME)/.local

build:
	go build -o bin/proem ./cmd/proem

# Installs by atomic rename, so upgrading does not overwrite a binary that is
# currently running (which breaks code signing on macOS).
install: build
	./scripts/install-binary.sh bin/proem $(PREFIX)/bin/proem

test:
	go test ./... -race -count=1 -coverprofile=coverage.out
	./scripts/tests/install-binary_test.sh

cover: test
	go tool cover -html=coverage.out -o coverage.html
	go tool cover -func=coverage.out | tail -n 20

fmt:
	gofmt -w .

cover-gate:
	./scripts/coverage.sh

vet:
	go vet ./...

docker:
	docker build -t proem:local .

lint: vet
	golangci-lint run ./... || true
