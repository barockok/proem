.PHONY: build test vet cover cover-gate docker lint fmt

build:
	go build -o bin/pro-ant ./cmd/proxy

test:
	go test ./... -race -count=1 -coverprofile=coverage.out

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
	docker build -t pro-ant:local .

lint: vet
	golangci-lint run ./... || true
