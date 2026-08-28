GO := go
DB_PATH ?= ./moco.db
TOOLS_DIR := $(abspath tools)
TOOL_BIN_DIR ?=
TOOL_GOMEMLIMIT ?= 2GiB
TOOL_GOGC ?= 50
TOOL_BUILD_ENV := GOWORK=off GOFLAGS=-p=1 GOMAXPROCS=1 GOMEMLIMIT=$(TOOL_GOMEMLIMIT) GOGC=$(TOOL_GOGC)
GOLANGCI_LINT_VERSION := 2.13.2
GOLANGCI_LINT_INSTALLER_COMMIT := 27774aaf853a4fd21f1dd5e69439459dc1b26e68
GOLANGCI_LINT := tools/bin/golangci-lint-v$(GOLANGCI_LINT_VERSION)/golangci-lint

ifeq ($(strip $(TOOL_BIN_DIR)),)
VACUUM := $$($(TOOL_BUILD_ENV) $(GO) -C $(TOOLS_DIR) tool -n vacuum)
ACTIONLINT := $$($(TOOL_BUILD_ENV) $(GO) -C $(TOOLS_DIR)/actionlint tool -n actionlint)
OAPI_CODEGEN := $$($(TOOL_BUILD_ENV) $(GO) -C $(TOOLS_DIR) tool -n oapi-codegen)
SQLC := $$($(TOOL_BUILD_ENV) $(GO) -C $(TOOLS_DIR) tool -n sqlc)
MIGRATE_SQLITE := $$($(TOOL_BUILD_ENV) $(GO) -C $(TOOLS_DIR) tool -n migrate-sqlite)
GOVULNCHECK := $$($(TOOL_BUILD_ENV) $(GO) -C $(TOOLS_DIR) tool -n govulncheck)
else
VACUUM := $(abspath $(TOOL_BIN_DIR))/vacuum
ACTIONLINT := $(abspath $(TOOL_BIN_DIR))/actionlint
OAPI_CODEGEN := $(abspath $(TOOL_BIN_DIR))/oapi-codegen
SQLC := $(abspath $(TOOL_BIN_DIR))/sqlc
MIGRATE_SQLITE := $(abspath $(TOOL_BIN_DIR))/migrate-sqlite
GOVULNCHECK := $(abspath $(TOOL_BIN_DIR))/govulncheck
endif

.PHONY: workflow-lint spec-lint spec-bundle api-generate internal-api-generate sqlc-generate db-migrate test lint vulncheck run check
workflow-lint:
	GOWORK=off $(ACTIONLINT) .github/workflows/*.yml

spec-lint:
	GOWORK=off $(VACUUM) lint --ruleset openapi/vacuum.yaml --fail-severity warn --min-score 100 --no-banner -d openapi/public.yaml
	GOWORK=off $(VACUUM) lint --ruleset openapi/vacuum.yaml --fail-severity warn --min-score 100 --no-banner -d openapi/internal.yaml

spec-bundle:
	GOWORK=off $(VACUUM) bundle --composed --no-style --base openapi openapi/public.yaml openapi/bundled/public.yaml
	GOWORK=off $(VACUUM) bundle --composed --no-style --base openapi openapi/internal.yaml openapi/bundled/internal.yaml

api-generate: spec-bundle
	cd tools && GOWORK=off $(OAPI_CODEGEN) --config ../openapi/oapi-codegen.yaml ../openapi/bundled/public.yaml
	cd tools && GOWORK=off $(OAPI_CODEGEN) --config ../openapi/internal-oapi-codegen.yaml ../openapi/bundled/internal.yaml

internal-api-generate: spec-bundle
	cd tools && GOWORK=off $(OAPI_CODEGEN) --config ../openapi/internal-oapi-codegen.yaml ../openapi/bundled/internal.yaml

sqlc-generate:
	GOWORK=off $(SQLC) generate -f sqlc.yaml

db-migrate:
	GOWORK=off $(MIGRATE_SQLITE) -path db/migrations -database "sqlite://$(abspath $(DB_PATH))" up

test:
	GOWORK=off $(GO) test ./... -count=1
	GOWORK=off $(GO) -C tools test ./... -count=1
	@if [ -f dagger/dagger.gen.go ]; then GOWORK=off $(GO) -C dagger test ./... -count=1; fi

$(GOLANGCI_LINT):
	@set -eu; installer="$$(mktemp)"; trap 'rm -f "$$installer"' EXIT; \
	curl --fail --silent --show-error --location --output "$$installer" https://raw.githubusercontent.com/golangci/golangci-lint/$(GOLANGCI_LINT_INSTALLER_COMMIT)/install.sh; \
	sh "$$installer" -b $(dir $@) v$(GOLANGCI_LINT_VERSION)

lint: $(GOLANGCI_LINT)
	GOWORK=off $(abspath $(GOLANGCI_LINT)) run --config .golangci.yml ./...
	cd tools && GOWORK=off $(abspath $(GOLANGCI_LINT)) run --config ../.golangci.yml ./...
	@if [ -f dagger/dagger.gen.go ]; then cd dagger && GOWORK=off $(abspath $(GOLANGCI_LINT)) run --config ../.golangci.yml ./...; fi

vulncheck: $(GOLANGCI_LINT)
	GOWORK=off $(GOVULNCHECK) -test ./...
	GOWORK=off $(GOVULNCHECK) -C tools -test ./...
	@set -eu; scanner="$(GOVULNCHECK)"; \
	if [ -f dagger/dagger.gen.go ]; then GOWORK=off "$$scanner" -C dagger -test ./...; fi; \
	for tool in vacuum oapi-codegen sqlc migrate-sqlite govulncheck; do \
		if [ -n "$(TOOL_BIN_DIR)" ]; then binary="$(abspath $(TOOL_BIN_DIR))/$$tool"; else binary="$$($(TOOL_BUILD_ENV) $(GO) -C $(TOOLS_DIR) tool -n "$$tool")"; fi; \
		GOWORK=off "$$scanner" -mode=binary "$$binary"; \
	done; \
	if [ -n "$(TOOL_BIN_DIR)" ]; then actionlint="$(abspath $(TOOL_BIN_DIR))/actionlint"; else actionlint="$$($(TOOL_BUILD_ENV) $(GO) -C $(TOOLS_DIR)/actionlint tool -n actionlint)"; fi; \
	GOWORK=off "$$scanner" -mode=binary "$$actionlint"; \
	GOWORK=off "$$scanner" -mode=binary "$(abspath $(GOLANGCI_LINT))"

run:
	GOWORK=off $(GO) run ./cmd/moco-server

check: workflow-lint spec-lint api-generate sqlc-generate test lint vulncheck
	GOWORK=off $(GO) vet ./...
	@if [ -f dagger/dagger.gen.go ]; then GOWORK=off $(GO) -C dagger vet ./...; fi
