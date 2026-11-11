COMPOSE      := docker compose
LINT_IMAGE   := golangci/golangci-lint:v2.15.0
GO_IMAGE     := golang:1.26
VULN_VERSION := v1.9.0
TEST_REPORT  := test-results/test-$(shell date +%Y%m%d-%H%M%S).txt

# Tooling runs in containers so the host only needs Docker and make. These
# cache volumes are separate from the ones docker compose manages.
TOOL_VOLUMES := controlplane-tools-gomod controlplane-tools-gobuild controlplane-tools-lint
DOCKER_GO := docker run --rm -v "$(CURDIR)":/src -w /src \
	-v controlplane-tools-gomod:/go/pkg/mod \
	-v controlplane-tools-gobuild:/root/.cache/go-build \
	-e GOFLAGS=-buildvcs=false

.PHONY: up down clean logs test test-memory check lint vuln fmt worker-fail worker-normal race publish

# .env holds the local passwords and is git-ignored. It is created once from
# .env.example with random passwords. Delete it only together with the volumes
# (make clean), because Postgres and RabbitMQ keep the password from first boot.
.env:
	@umask 077; awk -v pg="$$(openssl rand -hex 16)" -v mq="$$(openssl rand -hex 16)" \
		'/^POSTGRES_PASSWORD=/ { $$0 = "POSTGRES_PASSWORD=" pg } \
		 /^RABBITMQ_PASSWORD=/ { $$0 = "RABBITMQ_PASSWORD=" mq } { print }' .env.example > $@
	@echo "Created .env with random passwords (git-ignored)."

up: .env ## Build and boot the whole stack, wait until healthy.
	$(COMPOSE) up --build -d --wait
	@echo ""
	@echo "Control plane API:  http://localhost:8000  (health: /healthz)"
	@echo "RabbitMQ UI:        http://localhost:15672 (user and password are in .env)"

down: .env ## Stop and remove containers, keep volumes.
	$(COMPOSE) down

clean: .env ## Stop containers and remove volumes.
	$(COMPOSE) --profile tools down -v
	docker volume rm -f $(TOOL_VOLUMES)

logs: .env ## Follow logs from all services.
	$(COMPOSE) logs -f

test: .env ## Run the full test suite (with -race) against postgres + rabbitmq.
	@mkdir -p test-results
	$(COMPOSE) up -d --wait postgres rabbitmq
	@# Verbose output goes to the terminal and to a timestamped file; pipefail
	@# keeps a failing test from being hidden by tee's exit code.
	$(COMPOSE) run --rm test bash -c 'set -o pipefail; \
		go test -race -p 1 -count=1 -v -tags integration ./... 2>&1 | tee $(TEST_REPORT)'
	@echo "Test results saved to $(TEST_REPORT)"

test-memory: .env ## Run the API, consumer, relay and end-to-end tests on the in-memory store.
	$(COMPOSE) up -d --wait rabbitmq
	$(COMPOSE) run --rm -e TEST_DB_DRIVER=memory test \
		go test -race -count=1 -tags integration ./internal/api/ ./internal/consumer/ ./internal/outbox/ ./internal/e2e/

lint:
	$(DOCKER_GO) -v controlplane-tools-lint:/root/.cache/golangci-lint $(LINT_IMAGE) golangci-lint run ./...

vuln:
	$(DOCKER_GO) $(GO_IMAGE) go run golang.org/x/vuln/cmd/govulncheck@$(VULN_VERSION) -tags integration ./...

check: lint vuln test ## Lint (incl. format + gosec), govulncheck, then tests.

fmt: ## Auto-format with gofmt + goimports.
	$(DOCKER_GO) -v controlplane-tools-lint:/root/.cache/golangci-lint $(LINT_IMAGE) golangci-lint fmt ./...

worker-fail: .env ## Restart the worker so every task fails.
	WORKER_FAIL_RATE=1 $(COMPOSE) up -d --no-deps --force-recreate worker

worker-normal: .env ## Restart the worker with default settings.
	$(COMPOSE) up -d --no-deps --force-recreate worker

race: .env ## Hammer the running API with concurrent creates and PATCHes.
	$(COMPOSE) run --rm --build racetest

publish: .env ## Publish a hand-written update, e.g. make publish ARGS="poison".
	./scripts/publish_update.sh $(ARGS)
