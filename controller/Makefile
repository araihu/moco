GO ?= go

.PHONY: generate check build
generate:
	GOWORK=off $(GO) tool controller-gen object paths=./api/... crd output:crd:artifacts:config=../charts/moco-controller/crds

check: generate
	GOWORK=off $(GO) test -race ./... -count=1
	GOWORK=off $(GO) vet ./...

build:
	GOWORK=off CGO_ENABLED=0 $(GO) build -mod=readonly -trimpath -o ../bin/moco-controller ./cmd/moco-controller
