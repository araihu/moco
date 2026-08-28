GO := go
DB_PATH ?= ./moco.db
TOOL_GOMEMLIMIT ?= 2GiB
TOOL_GOGC ?= 50
TOOL_BUILD_ENV := GOWORK=off GOFLAGS=-p=1 GOMAXPROCS=1 GOMEMLIMIT=$(TOOL_GOMEMLIMIT) GOGC=$(TOOL_GOGC)
GOLANGCI_LINT_VERSION := 2.13.2
GOLANGCI_LINT_INSTALLER_COMMIT := 27774aaf853a4fd21f1dd5e69439459dc1b26e68
GOLANGCI_LINT := tools/bin/golangci-lint-v$(GOLANGCI_LINT_VERSION)/golangci-lint

.PHONY: spec-lint spec-bundle api-generate sqlc-generate db-migrate test lint vulncheck run check
spec-lint:
	GOWORK=off $$($(TOOL_BUILD_ENV) $(GO) -C tools tool -n vacuum) lint --ruleset openapi/vacuum.yaml --fail-severity warn --min-score 100 --no-banner -d openapi/public.yaml
	GOWORK=off $$($(TOOL_BUILD_ENV) $(GO) -C tools tool -n vacuum) lint --ruleset openapi/vacuum.yaml --fail-severity warn --min-score 100 --no-banner -d openapi/internal.yaml

spec-bundle:
	GOWORK=off $$($(TOOL_BUILD_ENV) $(GO) -C tools tool -n vacuum) bundle --composed --no-style --base openapi openapi/public.yaml openapi/bundled/public.yaml
	GOWORK=off $$($(TOOL_BUILD_ENV) $(GO) -C tools tool -n vacuum) bundle --composed --no-style --base openapi openapi/internal.yaml openapi/bundled/internal.yaml

api-generate: spec-bundle
	cd tools && GOWORK=off $$($(TOOL_BUILD_ENV) $(GO) tool -n oapi-codegen) --config ../openapi/oapi-codegen.yaml ../openapi/bundled/public.yaml

sqlc-generate:
	GOWORK=off $$($(TOOL_BUILD_ENV) $(GO) -C tools tool -n sqlc) generate -f sqlc.yaml

db-migrate:
	GOWORK=off $$($(TOOL_BUILD_ENV) $(GO) -C tools tool -n migrate-sqlite) -path db/migrations -database "sqlite://$(abspath $(DB_PATH))" up

test:
	GOWORK=off $(GO) test ./... -count=1
	GOWORK=off $(GO) -C tools test ./... -count=1

$(GOLANGCI_LINT):
	@set -eu; installer="$$(mktemp)"; trap 'rm -f "$$installer"' EXIT; \
	curl --fail --silent --show-error --location --output "$$installer" https://raw.githubusercontent.com/golangci/golangci-lint/$(GOLANGCI_LINT_INSTALLER_COMMIT)/install.sh; \
	sh "$$installer" -b $(dir $@) v$(GOLANGCI_LINT_VERSION)

lint: $(GOLANGCI_LINT)
	GOWORK=off $(abspath $(GOLANGCI_LINT)) run --config .golangci.yml ./...
	cd tools && GOWORK=off $(abspath $(GOLANGCI_LINT)) run --config ../.golangci.yml ./...

vulncheck: $(GOLANGCI_LINT)
	GOWORK=off $$($(TOOL_BUILD_ENV) $(GO) -C tools tool -n govulncheck) -test ./...
	GOWORK=off $$($(TOOL_BUILD_ENV) $(GO) -C tools tool -n govulncheck) -C tools -test ./...
	@set -eu; scanner="$$($(TOOL_BUILD_ENV) $(GO) -C tools tool -n govulncheck)"; \
	for tool in vacuum oapi-codegen sqlc migrate-sqlite govulncheck; do \
		binary="$$($(TOOL_BUILD_ENV) $(GO) -C tools tool -n "$$tool")"; \
		GOWORK=off "$$scanner" -mode=binary "$$binary"; \
	done; \
	GOWORK=off "$$scanner" -mode=binary "$(abspath $(GOLANGCI_LINT))"

run:
	GOWORK=off $(GO) run ./cmd/moco-server

check: spec-lint api-generate sqlc-generate test lint vulncheck
	GOWORK=off $(GO) vet ./...
