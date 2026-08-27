GO := go
VACUUM_VERSION := v0.30.0
OAPI_CODEGEN_VERSION := v2.8.0
SQLC_VERSION := v1.31.1
MIGRATE_VERSION := v4.19.1
DB_PATH ?= ./moco.db

.PHONY: spec-lint
spec-lint:
	GOWORK=off $(GO) run github.com/daveshanley/vacuum@$(VACUUM_VERSION) lint --ruleset api/vacuum.yaml -d api/openapi.yaml

.PHONY: api-generate
api-generate:
	GOWORK=off $(GO) run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_CODEGEN_VERSION) --config api/oapi-codegen.yaml api/openapi.yaml

.PHONY: sqlc-generate db-migrate
sqlc-generate:
	GOWORK=off $(GO) run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate

db-migrate:
	GOWORK=off $(GO) run -tags sqlite github.com/golang-migrate/migrate/v4/cmd/migrate@$(MIGRATE_VERSION) -path db/migrations -database "sqlite://$(DB_PATH)" up
