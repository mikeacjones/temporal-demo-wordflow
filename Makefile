.PHONY: up down test vet workflowcheck

# Everything runs in containers; see compose.yaml.
up:
	docker compose up --build

down:
	docker compose down

test:
	docker compose --profile test run --rm test

vet:
	docker compose --profile test run --rm test go vet ./...

workflowcheck:
	docker compose --profile test run --rm test go run go.temporal.io/sdk/contrib/tools/workflowcheck@v0.5.0 ./...
