.PHONY: build check fmt fmt-check generate integration migrate migration migration-validate outbox run test vet vuln

build:
	go build ./cmd/api ./cmd/migrate ./cmd/outbox

generate:
	go generate ./internal/api

fmt:
	gofmt -w ./cmd ./internal

fmt-check:
	@test -z "$$(gofmt -l ./cmd ./internal)" || \
		{ gofmt -l ./cmd ./internal; echo "Go files need formatting" >&2; exit 1; }

test:
	go test -race ./...

integration:
	go test -race -tags=integration ./internal/postgres ./internal/messaging

vet:
	go vet ./...

vuln:
	go tool govulncheck ./...

check: generate fmt-check migration-validate test vet

migrate:
	go run ./cmd/migrate

migration:
	@test -n "$(name)" || { echo "usage: make migration name=add_recordings" >&2; exit 1; }
	atlas migrate diff "$(name)" --env local

migration-validate:
	atlas migrate validate --env local

run:
	go run ./cmd/api

outbox:
	go run ./cmd/outbox
