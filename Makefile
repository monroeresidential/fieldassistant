.PHONY: run test db-up db-down generate sync-contract

run:
	cd api && go run ./cmd/api

test:
	cd api && go test ./...

db-up:
	docker compose -f api/docker-compose.yml up -d postgres

db-down:
	docker compose -f api/docker-compose.yml down

generate:
	cd api && go tool oapi-codegen -config oapi-codegen.yaml openapi.yaml

sync-contract:
	@echo "defined in a later task"
