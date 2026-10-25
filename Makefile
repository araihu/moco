GO := go
DB_PATH ?= ./moco.db
IMAGE ?= moco:$(shell git rev-parse --short HEAD)
IMAGE_VERSION ?= $(shell git describe --always --dirty)
DOCKER_BUILD_FLAGS ?=
LOAD_ARGS ?=
LOAD_REPORT ?=
PROMTOOL_IMAGE := prom/prometheus@sha256:fa3302279710be2f21a8301ec17a8b4f2301fd829bd113839b3b6b02b6f191a2
TOOLS_DIR := $(abspath tools)
TOOL_BIN_DIR ?=
TOOL_GOMEMLIMIT ?= 2GiB
TOOL_GOGC ?= 50
TOOL_BUILD_ENV := GOWORK=off GOFLAGS=-p=1 GOMAXPROCS=1 GOMEMLIMIT=$(TOOL_GOMEMLIMIT) GOGC=$(TOOL_GOGC)
LINT_BUILD_GOMEMLIMIT ?= 4GiB
LINT_BUILD_GOGC ?= 100
LINT_BUILD_ENV := GOWORK=off GOFLAGS='-mod=readonly -p=1' GOMAXPROCS=1 GOMEMLIMIT=$(LINT_BUILD_GOMEMLIMIT) GOGC=$(LINT_BUILD_GOGC)

ifeq ($(strip $(TOOL_BIN_DIR)),)
VACUUM := $$($(TOOL_BUILD_ENV) $(GO) -C $(TOOLS_DIR) tool -n vacuum)
ACTIONLINT := $$($(TOOL_BUILD_ENV) $(GO) -C $(TOOLS_DIR)/actionlint tool -n actionlint)
OAPI_CODEGEN := $$($(TOOL_BUILD_ENV) $(GO) -C $(TOOLS_DIR) tool -n oapi-codegen)
SQLC := $$($(TOOL_BUILD_ENV) $(GO) -C $(TOOLS_DIR) tool -n sqlc)
MIGRATE_SQLITE := $$($(TOOL_BUILD_ENV) $(GO) -C $(TOOLS_DIR) tool -n migrate-sqlite)
GOVULNCHECK := $$($(TOOL_BUILD_ENV) $(GO) -C $(TOOLS_DIR) tool -n govulncheck)
GOLANGCI_LINT := $$($(LINT_BUILD_ENV) $(GO) -C $(TOOLS_DIR)/golangci-lint tool -n golangci-lint)
else
VACUUM := $(abspath $(TOOL_BIN_DIR))/vacuum
ACTIONLINT := $(abspath $(TOOL_BIN_DIR))/actionlint
OAPI_CODEGEN := $(abspath $(TOOL_BIN_DIR))/oapi-codegen
SQLC := $(abspath $(TOOL_BIN_DIR))/sqlc
MIGRATE_SQLITE := $(abspath $(TOOL_BIN_DIR))/migrate-sqlite
GOVULNCHECK := $(abspath $(TOOL_BIN_DIR))/govulncheck
GOLANGCI_LINT := $(abspath $(TOOL_BIN_DIR))/golangci-lint
endif

.PHONY: workflow-lint spec-lint spec-bundle api-generate internal-api-generate sqlc-generate db-migrate test lint vulncheck run check operational-test load-test image image-check monitoring-check
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
	GOWORK=off $(GO) -C dagger/third_party/otel-go test ./... -count=1

lint:
	GOWORK=off $(GOLANGCI_LINT) run --config .golangci.yml ./...
	cd tools && GOWORK=off $(GOLANGCI_LINT) run --config ../.golangci.yml ./...
	@if [ -f dagger/dagger.gen.go ]; then cd dagger && GOWORK=off $(GOLANGCI_LINT) run --config ../.golangci.yml ./...; fi

vulncheck:
	GOWORK=off $(GOVULNCHECK) -test ./...
	GOWORK=off $(GOVULNCHECK) -C tools -test ./...
	GOWORK=off $(GOVULNCHECK) -C dagger/third_party/otel-go -test ./...
	@set -eu; scanner="$(GOVULNCHECK)"; \
	if [ -f dagger/dagger.gen.go ]; then GOWORK=off "$$scanner" -C dagger -test ./...; fi; \
	for tool in vacuum oapi-codegen sqlc migrate-sqlite govulncheck; do \
		if [ -n "$(TOOL_BIN_DIR)" ]; then binary="$(abspath $(TOOL_BIN_DIR))/$$tool"; else binary="$$($(TOOL_BUILD_ENV) $(GO) -C $(TOOLS_DIR) tool -n "$$tool")"; fi; \
		GOWORK=off "$$scanner" -mode=binary "$$binary"; \
	done; \
	if [ -n "$(TOOL_BIN_DIR)" ]; then actionlint="$(abspath $(TOOL_BIN_DIR))/actionlint"; else actionlint="$$($(TOOL_BUILD_ENV) $(GO) -C $(TOOLS_DIR)/actionlint tool -n actionlint)"; fi; \
	GOWORK=off "$$scanner" -mode=binary "$$actionlint"; \
	GOWORK=off "$$scanner" -mode=binary "$(GOLANGCI_LINT)"

run:
	GOWORK=off $(GO) run ./cmd/moco-server

operational-test:
	@set -eu; build_dir=$$(mktemp -d); trap 'rm -rf "$$build_dir"' EXIT; \
	GOWORK=off $(GO) build -trimpath -o "$$build_dir/" ./cmd/...; \
	python3 scripts/production_test.py --bin-dir "$$build_dir"

load-test:
	@set -eu; build_dir=$$(mktemp -d); trap 'rm -rf "$$build_dir"' EXIT; \
	GOWORK=off $(GO) build -trimpath -o "$$build_dir/" ./cmd/...; \
	python3 -B scripts/load_test.py --bin-dir "$$build_dir" $(LOAD_ARGS) $(if $(strip $(LOAD_REPORT)),--report "$(abspath $(LOAD_REPORT))")

image:
	docker build $(DOCKER_BUILD_FLAGS) --build-arg VERSION="$(IMAGE_VERSION)" --build-arg REVISION="$$(git rev-parse HEAD)" -t "$(IMAGE)" .

image-check: image
	python3 scripts/production_test.py --image "$(IMAGE)"

monitoring-check:
	@set -eu; fixture=$$(docker create --entrypoint /bin/promtool "$(PROMTOOL_IMAGE)" test rules /fixture/alerts.test.yaml); \
	trap 'docker rm -f "$$fixture" >/dev/null' EXIT; \
	docker cp deploy/production/. "$$fixture:/fixture"; \
	docker start -a "$$fixture"; \
	test "$$(docker inspect --format '{{.State.ExitCode}}' "$$fixture")" = 0

check: workflow-lint spec-lint api-generate sqlc-generate test lint vulncheck operational-test load-test
	GOWORK=off $(GO) vet ./...
	GOWORK=off $(GO) -C dagger/third_party/otel-go vet ./...
	@if [ -f dagger/dagger.gen.go ]; then GOWORK=off $(GO) -C dagger vet ./...; fi
