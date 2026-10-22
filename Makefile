.PHONY: run test db-up db-down generate sync-contract check-contract

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
	cd api && go tool sqlc generate

IOS_CONTRACT := ios/Packages/FieldAssistantAPI/Sources/FieldAssistantAPI/openapi.yaml

sync-contract:
	cp api/openapi.yaml $(IOS_CONTRACT)

check-contract:
	@cmp -s api/openapi.yaml $(IOS_CONTRACT) || { echo "iOS contract copy is out of date with api/openapi.yaml; run: make sync-contract"; exit 1; }
	@echo "contract in sync"
