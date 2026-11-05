# CI/CD

How to run the quality checks (lint, formatting, vulnerability scan, tests) on
demand, and how to use them as a gate in a CI pipeline.

## Run the checks on demand

Run these from the repo folder at any time. They need only Docker: no Go
install, and no running stack (only `make check` starts Postgres and RabbitMQ,
for its tests).

```bash
make lint     # check the code; changes nothing
make fmt      # fix formatting in place
make vuln     # scan Go and every dependency for known vulnerabilities
make check    # lint, then vuln, then every test; stops at the first failure
```

| Command | What it does | Time (warm cache) | Passing output |
|---|---|---|---|
| `make lint` | golangci-lint: formatting (gofmt, goimports), `go vet`, staticcheck, errcheck, `gosec` security rules, unused code and more | ~2s | `0 issues.` |
| `make fmt` | Rewrites files to fix formatting (gofmt, goimports) | ~1s | nothing printed |
| `make vuln` | govulncheck: known vulnerabilities in the Go standard library and every module, reported only if the code can reach them | ~5s | `No vulnerabilities found.` |
| `make check` | All of the above plus the full test suite (see [Running the tests](testing.md)) | ~35s | `0 issues.`, `No vulnerabilities found.`, then `ok` per package |

Each command exits with code 0 on success and non-zero on any finding, so it can
fail a script or a CI job. The first run is slower because it downloads the
linter and Go modules; they are then cached in Docker volumes until
`make clean`.

## When a check fails

`make lint` names the file, line and rule. For example, a file with an
unchecked error and an unused function:

```
internal/retry/zz_lint_demo.go:6:11: Error return value of `os.Remove` is not checked (errcheck)
	os.Remove("x")
	         ^
internal/retry/zz_lint_demo.go:5:6: func demo is unused (unused)
func demo()   {
     ^
2 issues:
* errcheck: 1
* unused: 1
make: *** [lint] Error 1
```

A file whose only problem is formatting:

```
internal/retry/zz_fmt_demo.go:4:1: File is not properly formatted (gofmt)
func Demo()   int {
^
1 issues:
* gofmt: 1
make: *** [lint] Error 1
```

How to fix each kind:

| Finding | Fix |
|---|---|
| `gofmt` or `goimports` | Run `make fmt`, then `make lint` again |
| Anything else (`errcheck`, `unused`, `gosec`, `staticcheck`, …) | Change the code; `make fmt` can't fix these |
| `make vuln` reports a vulnerability | Upgrade the module it names (`go get <module>@<fixed version>`, then `go mod tidy`), or the Go version if it's in the standard library |

golangci-lint shows **one issue per line**, so a formatting problem on a line
that also has another finding appears only after the other one is fixed. Run
`make lint` again after each round of fixes.

A suggested routine before each commit:

```bash
make fmt && make lint
```

and before each push:

```bash
make check
```

## What controls the checks

| What | Where |
|---|---|
| Which linters run, and their settings | [.golangci.yml](../.golangci.yml) |
| Tool versions (golangci-lint v2.15.0, govulncheck v1.9.0, Go 1.26) | [Makefile](../Makefile), top of the file |
| How each target runs | [Makefile](../Makefile): `lint`, `fmt`, `vuln`, `check` |

The versions are pinned, so the results only change when you bump them, not
when a new release of a tool comes out.

## Using it in CI

`make check` is the whole CI gate: if it exits 0, the change passes lint, the
vulnerability scan and every test. A CI job needs only Docker, make and
openssl (to generate `.env`), which standard Linux runners have.

A minimal GitHub Actions workflow would be `.github/workflows/ci.yml`:

```yaml
name: ci
on: [push, pull_request]
jobs:
  check:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: make check
      - uses: actions/upload-artifact@v4
        if: always()
        with:
          name: test-results
          path: test-results/
```

- `make check` creates `.env` with fresh random passwords on the runner, so no
  secrets need to be configured.
- The upload step keeps the saved test output (`test-results/`) even when a
  test fails.

This workflow is not in the repo yet, and it hasn't been run on GitHub. The
same `make check` passes locally.

## Deployment (CD)

There is no deployment pipeline: the project runs locally with Docker Compose.
The pieces a CD step would use already exist:

- `docker compose build` produces the `controlplane` and `worker` images from
  the [Dockerfile](../Dockerfile): static binaries on a distroless, non-root base.
- `GET /healthz` returns 200 only when both the database and RabbitMQ are
  reachable, which suits a readiness probe.
- Migrations run automatically at startup and take a database lock, so
  several instances starting at once are safe.

A deployment would push those images to a registry after `make check` passes
on the main branch, and supply real secrets from the platform instead of `.env`.
