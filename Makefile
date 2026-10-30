.PHONY: run test evals db-up db-down generate sync-contract check-contract ios-project

run: ## loads api/.env if present
	cd api && set -a && [ -f .env ] && . ./.env; set +a && go run ./cmd/api

test:
	cd api && go test ./...

# Live RFI extraction evals. Needs ANTHROPIC_API_KEY, LLM_EXTRACT_MODEL and
# TEST_DATABASE_URL in the environment; skips when any is unset.
evals:
	cd api && go test -tags evals ./internal/features/rfi/evals/ -v -run TestEvals

db-up:
	docker compose -f api/docker-compose.yml up -d postgres minio

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

# ios/Package.resolved is the source of truth for Swift package versions. The
# .xcodeproj is generated and gitignored, so this target regenerates it and
# installs the pinned resolution where xcodebuild reads it. To update pins:
# run `make ios-project`, update packages in Xcode (File > Packages > Update),
# then copy $(IOS_RESOLVED_DIR)/Package.resolved back to ios/Package.resolved.
IOS_RESOLVED_DIR := ios/FieldAssistant.xcodeproj/project.xcworkspace/xcshareddata/swiftpm

ios-project:
	cd ios && xcodegen generate
	mkdir -p $(IOS_RESOLVED_DIR)
	cp ios/Package.resolved $(IOS_RESOLVED_DIR)/Package.resolved
