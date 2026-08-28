GO := go
DB_PATH ?= ./moco.db
TOOL_BUILD_ENV := GOWORK=off GOFLAGS=-p=1 GOMAXPROCS=1 GOMEMLIMIT=1GiB GOGC=20

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

lint:
	GOWORK=off $$($(TOOL_BUILD_ENV) $(GO) -C tools tool -n golangci-lint) run --config .golangci.yml ./...
	cd tools && GOWORK=off $$($(TOOL_BUILD_ENV) $(GO) tool -n golangci-lint) run --config ../.golangci.yml ./...

vulncheck:
	GOWORK=off $$($(TOOL_BUILD_ENV) $(GO) -C tools tool -n govulncheck) -test ./...
	GOWORK=off $$($(TOOL_BUILD_ENV) $(GO) -C tools tool -n govulncheck) -C tools -test ./...
	@set -eu; scanner="$$($(TOOL_BUILD_ENV) $(GO) -C tools tool -n govulncheck)"; \
	for tool in vacuum oapi-codegen sqlc migrate-sqlite golangci-lint govulncheck; do \
		binary="$$($(TOOL_BUILD_ENV) $(GO) -C tools tool -n "$$tool")"; \
		GOWORK=off "$$scanner" -mode=binary "$$binary"; \
	done

run:
	GOWORK=off $(GO) run ./cmd/moco-server

check: spec-lint api-generate sqlc-generate test lint vulncheck
	GOWORK=off $(GO) vet ./...
