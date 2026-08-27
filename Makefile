GO := go
DB_PATH ?= ./moco.db

.PHONY: spec-lint spec-bundle api-generate sqlc-generate db-migrate
spec-lint:
	GOWORK=off $(GO) -C tools tool vacuum lint --ruleset ../openapi/vacuum.yaml --fail-severity warn --min-score 100 --no-banner -d ../openapi/public.yaml
	GOWORK=off $(GO) -C tools tool vacuum lint --ruleset ../openapi/vacuum.yaml --fail-severity warn --min-score 100 --no-banner -d ../openapi/internal.yaml

spec-bundle:
	GOWORK=off $(GO) -C tools tool vacuum bundle --composed --no-style --base ../openapi ../openapi/public.yaml ../openapi/bundled/public.yaml
	GOWORK=off $(GO) -C tools tool vacuum bundle --composed --no-style --base ../openapi ../openapi/internal.yaml ../openapi/bundled/internal.yaml

api-generate: spec-bundle
	GOWORK=off $(GO) -C tools tool oapi-codegen --config ../openapi/oapi-codegen.yaml ../openapi/bundled/public.yaml

sqlc-generate:
	GOWORK=off $(GO) -C tools tool sqlc generate -f ../sqlc.yaml

db-migrate:
	GOWORK=off $(GO) -C tools run -tags sqlite github.com/golang-migrate/migrate/v4/cmd/migrate -path ../db/migrations -database "sqlite://$(abspath $(DB_PATH))" up
