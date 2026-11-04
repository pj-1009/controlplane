COMPOSE      := docker compose
LINT_IMAGE   := golangci/golangci-lint:v2.15.0
GO_IMAGE     := golang:1.26
VULN_VERSION := v1.9.0

# Tooling runs in containers so the host only needs Docker and make. These
# cache volumes are separate from the ones docker compose manages.
TOOL_VOLUMES := controlplane-tools-gomod controlplane-tools-gobuild controlplane-tools-lint
DOCKER_GO := docker run --rm -v "$(CURDIR)":/src -w /src \
	-v controlplane-tools-gomod:/go/pkg/mod \
	-v controlplane-tools-gobuild:/root/.cache/go-build \
	-e GOFLAGS=-buildvcs=false

.PHONY: up down clean logs test check lint vuln fmt worker-fail worker-normal race publish

up: ## Build and boot the whole stack, wait until healthy.
	$(COMPOSE) up --build -d --wait
	@echo ""
	@echo "Control plane API:  http://localhost:8000  (health: /healthz)"
	@echo "RabbitMQ UI:        http://localhost:15672 (controlplane / REDACTED)"

down: ## Stop and remove containers, keep volumes.
	$(COMPOSE) down

clean: ## Stop containers and remove volumes.
	$(COMPOSE) --profile tools down -v
	docker volume rm -f $(TOOL_VOLUMES)

logs: ## Follow logs from all services.
	$(COMPOSE) logs -f

test: ## Run the full test suite (with -race) against postgres + rabbitmq.
	$(COMPOSE) up -d --wait postgres rabbitmq
	$(COMPOSE) run --rm test go test -race -p 1 -count=1 -tags integration ./...

lint:
	$(DOCKER_GO) -v controlplane-tools-lint:/root/.cache/golangci-lint $(LINT_IMAGE) golangci-lint run ./...

vuln:
	$(DOCKER_GO) $(GO_IMAGE) go run golang.org/x/vuln/cmd/govulncheck@$(VULN_VERSION) -tags integration ./...

check: lint vuln test ## Lint (incl. format + gosec), govulncheck, then tests.

fmt: ## Auto-format with gofmt + goimports.
	$(DOCKER_GO) -v controlplane-tools-lint:/root/.cache/golangci-lint $(LINT_IMAGE) golangci-lint fmt ./...

worker-fail: ## Restart the worker so every task fails.
	WORKER_FAIL_RATE=1 $(COMPOSE) up -d --no-deps --force-recreate worker

worker-normal: ## Restart the worker with default settings.
	$(COMPOSE) up -d --no-deps --force-recreate worker

race: ## Hammer the running API with concurrent creates and PATCHes.
	$(COMPOSE) run --rm --build racetest

publish: ## Publish a hand-written update, e.g. make publish ARGS="poison".
	./scripts/publish_update.sh $(ARGS)
