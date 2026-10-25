GO := go
CLI_TOOL_BIN_DIR ?=
TOOLS_DIR := $(abspath tools)
GENERATOR_ARGS := --spec ../openapi/bundled/cli/public.json --name moco --module github.com/araihu/moco/cli --out .

ifeq ($(strip $(CLI_TOOL_BIN_DIR)),)
GENERATOR := $$(GOWORK=off $(GO) -C $(TOOLS_DIR) tool -n oapi-cli-gen)
GOVULNCHECK := $$(GOWORK=off $(GO) -C $(TOOLS_DIR) tool -n govulncheck)
else
GENERATOR := $(abspath $(CLI_TOOL_BIN_DIR))/oapi-cli-gen
GOVULNCHECK := $(abspath $(CLI_TOOL_BIN_DIR))/govulncheck
endif

.PHONY: build install generate check-generated fmt-check vet test race vulncheck check

build:
	GOWORK=off $(GO) build -mod=readonly -trimpath -o ../bin/moco ./cmd/moco

install:
	GOWORK=off $(GO) install -mod=readonly ./cmd/moco

generate:
	GOWORK=off $(GENERATOR) generate $(GENERATOR_ARGS) --update
	GOWORK=off $(GO) mod tidy

check-generated:
	GOWORK=off $(GENERATOR) generate $(GENERATOR_ARGS) --check

fmt-check:
	@test -z "$$(gofmt -l cmd internal tools)" || { echo 'Go files need formatting'; exit 1; }

vet:
	GOWORK=off $(GO) vet -mod=readonly ./...
	GOWORK=off $(GO) -C tools vet -mod=readonly ./...

test:
	GOWORK=off $(GO) test -mod=readonly ./... -count=1
	GOWORK=off $(GO) -C tools test -mod=readonly ./... -count=1

race:
	GOWORK=off $(GO) test -mod=readonly -race ./... -count=1

vulncheck: build
	GOWORK=off $(GOVULNCHECK) -C cmd/moco -scan=module
	GOWORK=off $(GOVULNCHECK) -mode=binary -scan=module ../bin/moco
	GOWORK=off $(GOVULNCHECK) -mode=binary -scan=module "$(GENERATOR)"

check: check-generated fmt-check vet test race vulncheck
