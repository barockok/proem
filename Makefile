.PHONY: build install test vet cover cover-gate docker lint fmt e2e e2e-down

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

# End-to-end SSO gateway suite: real Keycloak, Redis, a mock upstream and the
# real Claude Code CLI, in docker compose. Needs no secrets. See e2e/README.md.
E2E_COMPOSE = docker compose -f e2e/docker-compose.yml

e2e:
	./e2e/certs/gen.sh
	$(E2E_COMPOSE) up -d --build --wait
	$(E2E_COMPOSE) --profile driver run --rm --build driver || { $(E2E_COMPOSE) logs; $(MAKE) e2e-down; exit 1; }
	$(E2E_COMPOSE) --profile claude run --rm --build claude || { $(E2E_COMPOSE) logs; $(MAKE) e2e-down; exit 1; }
	$(MAKE) e2e-down

e2e-down:
	$(E2E_COMPOSE) --profile driver --profile claude down -v --remove-orphans
