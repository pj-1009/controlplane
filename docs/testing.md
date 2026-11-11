# Running the tests

How to run the tests, read the results, and run just the part you need.

## The short version

```bash
make test     # every test, with the race detector, against real Postgres and RabbitMQ
make check    # lint + security checks + vulnerability scan + every test
```

Both need Docker running. You don't need Go installed; the tests run inside a
`golang:1.26` container. First, complete [Setup](setup.md) once.

## 1. Run every test: `make test`

```bash
make test
```

It does three things:

1. starts Postgres and RabbitMQ if they aren't running (`docker compose up -d --wait postgres rabbitmq`);
2. runs every test inside the `test` container:
   ```
   go test -race -p 1 -count=1 -v -tags integration ./...
   ```
3. prints the output and saves a copy to `test-results/test-<timestamp>.txt`.

It takes about **25 seconds** once the Go caches are warm. The first run is
slower because it downloads modules.

What the flags mean:

| Flag | Why |
|---|---|
| `-race` | Turns on Go's race detector, which fails a test if two goroutines touch the same memory unsafely |
| `-p 1` | Runs one package at a time, because the packages share one test database |
| `-count=1` | Always runs the tests; never reuses a cached "pass" |
| `-v` | Prints every test by name |
| `-tags integration` | Includes the tests that need Postgres and RabbitMQ (see [What gets tested](#5-what-gets-tested)) |

The end of a passing run looks like this:

```
=== RUN   TestRolledBackTransactionLeavesNoOutboxRow
--- PASS: TestRolledBackTransactionLeavesNoOutboxRow (0.04s)
PASS
ok  	github.com/pj-1009/controlplane/internal/store/postgres	1.574s
?   	github.com/pj-1009/controlplane/internal/store/postgres/migrations	[no test files]
?   	github.com/pj-1009/controlplane/internal/store/storetest	[no test files]
?   	github.com/pj-1009/controlplane/internal/testutil	[no test files]
?   	github.com/pj-1009/controlplane/internal/worker	[no test files]
=== RUN   TestSpecIsValid
--- PASS: TestSpecIsValid (0.03s)
PASS
ok  	github.com/pj-1009/controlplane/spec	1.043s
Test results saved to test-results/test-20261110-115442.txt
```

## 2. Read the results

Each package ends with one line:

| Line | Meaning |
|---|---|
| `ok  <package>  1.574s` | Every test in that package passed |
| `FAIL <package>` | At least one test in that package failed |
| `?   <package>  [no test files]` | The package has no tests of its own; it's covered by other packages' tests |

`make test` exits with code 0 only if everything passed, so it can gate a CI job.

The saved file has the full output. To check it quickly:

```bash
ls -t test-results/                                   # newest first
grep -c -- '--- PASS' test-results/<file>.txt         # how many tests passed (227 in the run above)
grep -- '--- FAIL' test-results/<file>.txt            # which tests failed, if any
grep -E '^(ok|FAIL)' test-results/<file>.txt          # one line per package
```

`test-results/` is git-ignored, so the files stay on your machine.

### When a test fails

The output shows the test name, the file and line, what was expected and what
came back. For example (illustrative):

```
--- FAIL: TestPatchTenant (0.05s)
    api_test.go:313:
        Error Trace:  /src/internal/api/api_test.go:313
        Error:        Not equal:
                      expected: "Acme 2"
                      actual  : "Tenant acme"
```

The summary line for that package then reads `FAIL`, and `make test` exits
with a non-zero code.

Rerun just that test while you fix it (see the next section).

## 3. Run only part of the tests

Use the `test` container directly. Every command below is a single line.

One package:

```bash
docker compose run --rm test go test -race -count=1 -tags integration ./internal/api/
```

One test, by name (`-run` takes a regular expression; `$` stops `TestPatchTenant`
also matching `TestPatchTenantFailedUpdateKeepsName`):

```bash
docker compose run --rm test go test -race -count=1 -tags integration -run 'TestPatchTenant$' -v ./internal/api/
```

```
=== RUN   TestPatchTenant
--- PASS: TestPatchTenant (0.07s)
PASS
ok  	github.com/pj-1009/controlplane/internal/api	1.081s
```

That took under 3 seconds.

Every test whose name contains a word, across all packages:

```bash
docker compose run --rm test go test -race -count=1 -tags integration -run 'Concurrent' -v ./...
```

If Postgres and RabbitMQ aren't running yet, start them first:

```bash
docker compose up -d --wait postgres rabbitmq
```

### Unit tests only, without Docker

If you have Go 1.26 installed, the tests that don't need a database run in about
a second, with no containers:

```bash
go test -count=1 ./...
```

```
ok  	github.com/pj-1009/controlplane/cmd/worker	0.605s
ok  	github.com/pj-1009/controlplane/internal/api	1.078s
ok  	github.com/pj-1009/controlplane/internal/domain	1.486s
ok  	github.com/pj-1009/controlplane/internal/retry	1.973s
```

Without `-tags integration`, Go skips every file marked `//go:build integration`.
If you add the tag on your own machine, those tests fail straight away and tell
you why:

```
--- FAIL: TestCreateAndGetTenant (0.00s)
    api_test.go:125: TEST_DATABASE_URL is not set; run integration tests with `make test`
```

## 4. The other checks

| Command | What it runs | Passing output |
|---|---|---|
| `make lint` | golangci-lint: formatting (gofmt, goimports), `go vet`, `staticcheck`, `errcheck`, `gosec` security checks and more ([.golangci.yml](../.golangci.yml)) | `0 issues.` |
| `make vuln` | govulncheck: known vulnerabilities in Go and every dependency | `No vulnerabilities found.` |
| `make check` | `make lint`, then `make vuln`, then `make test`; stops at the first failure | all three of the above |
| `make fmt` | Rewrites files to fix formatting (it changes files; it doesn't check) | — |
| `make test-memory` | The API, consumer, relay and end-to-end tests on the in-memory store instead of Postgres (needs only RabbitMQ) | `ok` for the four packages |
| `make race` | A standalone load test against the **running** stack (needs `make up`; `make k8s-race` runs it against the Kubernetes cluster) | `OK: exactly one winner in each race.` |

`make race` output:

```
Race 1: 20 concurrent POST /tenants with slug "race-10bc37e3"
  201                                      x1
  409 tenant_already_exists                x19

Waiting for tenant 8f28c6e7-b373-498e-8a23-6f0fdbd0c402 to become active (needs the worker)...

Race 2: 20 concurrent PATCH /tenants/8f28c6e7-b373-498e-8a23-6f0fdbd0c402 with version 2
  202                                      x1
  409 tenant_update_not_allowed            x19

OK: exactly one winner in each race.
```

Unlike `make test`, it uses the real API on port 8000 and leaves its tenants in
your database.

## 5. What gets tested

| Package | Kind | What it covers |
|---|---|---|
| `internal/domain` | unit | Every allowed and forbidden status change; slug, name and time rules; strict message decoding |
| `internal/retry` | unit | Backoff delays and jitter |
| `cmd/worker` | unit | Worker flags, env vars and their validation |
| `internal/api` | unit + integration | The panic recoverer and error mapping (unit). Every endpoint and error code, pagination, and three concurrency races against the real router and database (integration) |
| `internal/store/storetest` | (suite) | The conformance suite every database provider runs: atomic writes, one winner per race, domain errors, update outcomes, renames applied only when done, pagination, outbox relay |
| `internal/store/memory` | unit | The conformance suite against the in-memory provider |
| `internal/store/postgres` | integration | The conformance suite against Postgres, plus migrations and a rolled-back transaction leaving no outbox row |
| `internal/broker` | integration | Persistent publishing with confirms, idempotent queue setup, reconnecting after the connection is cut |
| `internal/outbox` | integration | The relay publishes and marks rows sent; a crash before marking sent leads to a harmless resend |
| `internal/consumer` | integration | Duplicates, stale updates, poison messages to the DLQ, panics, retries with backoff |
| `internal/e2e` | integration | The whole system in one process: a tenant becomes active; a failed tenant is destroyed; duplicates converge; resends are harmless |

## 6. Why the tests don't touch your data

You can run `make test` while the stack is up and while you're using it:

- **Separate database.** The tests use a database called `controlplane_test`,
  created automatically on the same Postgres server. Each test empties it
  before it starts. Your `controlplane` database is never touched.
- **Separate queues.** Each test declares its own queues with a random prefix,
  such as `test-1a2b3c4d.tasks`, and deletes them afterwards. The running relay,
  consumer and worker never see test messages.
- **Real services, no mocks.** The bugs that matter here live in SQL, locks and
  broker behaviour, which a mock can't reproduce.

The connection details reach the tests through environment variables set on
the `test` service in [docker-compose.yml](../docker-compose.yml):
`TEST_DATABASE_URL`, `TEST_ADMIN_DATABASE_URL`, `TEST_AMQP_URL` and
`TEST_RABBITMQ_MGMT_URL`. The helpers that use them are in
[internal/testutil](../internal/testutil/testutil.go).

## Troubleshooting

**`TEST_DATABASE_URL is not set`.** You ran integration tests with `go test`
on your own machine. Use `make test`, or the `docker compose run --rm test …`
commands above.

**Many packages fail at once with `password authentication failed`.** `.env`
and the database volume don't match (see [Setup](setup.md#troubleshooting)).
Fix it with `make clean && make up`, which deletes the local database.

**`Cannot connect to the Docker daemon`.** Start Docker Desktop.

**A test fails only sometimes.** Rerun it several times to check:
`docker compose run --rm test go test -race -count=10 -tags integration -run 'TestName$' ./internal/<package>/`.
A test that fails only under `-race` is a real data race; read the `WARNING: DATA RACE` block it prints.
