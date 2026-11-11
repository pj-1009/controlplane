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

.PHONY: up down clean logs test test-memory check lint vuln fmt worker-fail worker-normal race publish \
	k8s-up k8s-status k8s-logs k8s-down k8s-clean k8s-worker-fail k8s-worker-normal k8s-race k8s-publish

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

# --- Kubernetes (local kind cluster) --------------------------------------
# See docs/kubernetes.md. Needs kind and kubectl on the host.
KIND         ?= kind
KIND_CLUSTER := controlplane
K8S_DIR      := deploy/k8s
KUBECTL      := kubectl --context kind-$(KIND_CLUSTER) -n controlplane

# Only the five credentials, taken from .env, so the Secret never holds
# anything else. Regenerated whenever .env changes.
$(K8S_DIR)/secret.env: .env
	@umask 077; grep -E '^(POSTGRES_USER|POSTGRES_PASSWORD|POSTGRES_DB|RABBITMQ_USER|RABBITMQ_PASSWORD)=' .env > $@

k8s-up: .env $(K8S_DIR)/secret.env ## Create the kind cluster if needed, build and load the images, deploy, wait until ready.
	@command -v $(KIND) >/dev/null || { echo "kind is not installed: see docs/kubernetes.md"; exit 1; }
	@$(KIND) get clusters 2>/dev/null | grep -qx $(KIND_CLUSTER) || \
		$(KIND) create cluster --name $(KIND_CLUSTER) --config $(K8S_DIR)/kind-cluster.yaml
	docker build --target controlplane -t controlplane:local .
	docker build --target worker -t controlplane-worker:local .
	$(KIND) load docker-image --name $(KIND_CLUSTER) controlplane:local controlplane-worker:local
	kubectl --context kind-$(KIND_CLUSTER) create namespace controlplane --dry-run=client -o yaml | \
		kubectl --context kind-$(KIND_CLUSTER) apply -f -
	@# The image tag stays "local", so on a re-deploy restart the pods to
	@# pick up the fresh build. A first deploy starts them anyway.
	@if $(KUBECTL) get deployment/controlplane >/dev/null 2>&1; then \
		kubectl --context kind-$(KIND_CLUSTER) apply -k $(K8S_DIR) && \
		$(KUBECTL) rollout restart deployment/controlplane deployment/worker; \
	else \
		kubectl --context kind-$(KIND_CLUSTER) apply -k $(K8S_DIR); \
	fi
	$(KUBECTL) rollout status statefulset/postgres --timeout=180s
	$(KUBECTL) rollout status statefulset/rabbitmq --timeout=180s
	$(KUBECTL) rollout status deployment/controlplane --timeout=180s
	$(KUBECTL) rollout status deployment/worker --timeout=180s
	@echo "API: http://localhost:8080   RabbitMQ UI: http://localhost:15673"

k8s-status: ## Show the pods and services.
	$(KUBECTL) get pods,services -o wide

k8s-logs: ## Follow the control plane and worker logs from every pod.
	$(KUBECTL) logs -f --prefix --max-log-requests 10 -l 'app.kubernetes.io/name in (controlplane,worker)'

k8s-worker-fail: ## Make every task fail, by setting WORKER_FAIL_RATE=1 on the worker.
	$(KUBECTL) set env deployment/worker WORKER_FAIL_RATE=1
	$(KUBECTL) rollout status deployment/worker

k8s-worker-normal: ## Undo k8s-worker-fail.
	$(KUBECTL) set env deployment/worker WORKER_FAIL_RATE-
	$(KUBECTL) rollout status deployment/worker

k8s-race: ## Run the race test against the cluster's API.
	docker build --target racetest -t controlplane-racetest:local .
	docker run --rm -e API_URL=http://host.docker.internal:8080 controlplane-racetest:local

k8s-publish: ## Publish a hand-written update on the cluster, e.g. make k8s-publish ARGS="poison".
	RABBITMQ_EXEC="$(KUBECTL) exec -i rabbitmq-0 --" ./scripts/publish_update.sh $(ARGS)

k8s-down: ## Remove the app from the cluster but keep the data volumes and the cluster.
	$(KUBECTL) delete deployment,statefulset,service,configmap,secret -l app.kubernetes.io/part-of=controlplane

k8s-clean: ## Delete the whole kind cluster, data included.
	$(KIND) delete cluster --name $(KIND_CLUSTER)
	rm -f $(K8S_DIR)/secret.env
