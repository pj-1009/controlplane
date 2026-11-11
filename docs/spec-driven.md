# The API contract (spec-driven development)

The HTTP API is described in [`spec/openapi.yaml`](../spec/openapi.yaml)
(OpenAPI 3.0). The spec is the source of truth: the tests fail when the code
and the spec disagree, so to change the API you **change the spec first**.

## What the spec pins down

- Every route and method.
- Every request body, with required fields, types and the slug pattern.
  Unknown fields are not allowed.
- Every status code each route may return. An undocumented status is an error.
- Every response body. Extra or missing fields are errors, and so are wrong
  types, unknown enum values and timestamps that aren't `…T…\.\d{3}Z`.
- **Which error codes each response may carry.** For example, a 409 from
  `PATCH` may only be `tenant_update_not_allowed` or `tenant_version_conflict`,
  and a 409 from `POST` only `tenant_already_exists`.

## How it's enforced

| Check | Where | Fails when |
|---|---|---|
| The spec is a valid OpenAPI document | `TestSpecIsValid` in [spec/contract_test.go](../spec/contract_test.go) | A typo or broken `$ref` in the YAML |
| The router and the spec list the same routes | `TestRoutesMatchSpec` in [internal/api/contract_test.go](../internal/api/contract_test.go) | A route is added to the code but not the spec, or the other way round |
| **Every response the API tests receive matches the spec** | `env.do` in [internal/api/api_test.go](../internal/api/api_test.go) | A handler returns an undocumented status, field or error code |
| 500 and 503 responses match the spec | `TestErrorResponsesMatchSpec` | The error envelope changes |

The third check gives most of the coverage. All the API tests send their
requests through one helper, so every existing API test became a contract test
without being rewritten. The checking code is in
[spec/contract.go](../spec/contract.go), using
[kin-openapi](https://github.com/getkin/kin-openapi).

All of it runs in `make test` and `make check`; nothing extra to install.

## Proof that it catches drift

Each of these deliberate breakages fails `make test` with the message shown:

| Breakage | Error |
|---|---|
| Add a `plan` field to `Tenant` | `response body doesn't match schema #/components/schemas/Tenant: property "plan" is unsupported` |
| `POST /tenants` returns 200 instead of 201 | `POST /tenants returned 200, which doesn't match openapi.yaml: status is not supported` |
| Add a `GET /version` route | `the router and spec/openapi.yaml disagree` |

## The workflow for an API change

1. **Change the spec.** Edit `spec/openapi.yaml`: add the field, parameter,
   status or route. Open this as its own pull request, or as the first commit
   of one, so the design is reviewed before any code is written.
2. **Run `make test`.** The contract tests now fail and point at the code that
   doesn't match yet.
3. **Write the behaviour tests.** Add tests in `internal/api/api_test.go` for
   the new behaviour. They fail too.
4. **Implement** until `make check` passes.
5. **Update the docs.** Update [docs/errors.md](errors.md) and the README's
   API reference if error codes or examples changed.

### Example: add `GET /tenants?slug=`

1. In the spec, add a `slug` query parameter to `listTenants`.
2. `make test` still passes, because the contract only checks responses.
   Unknown query parameters are ignored today. Add a test first:
   `GET /tenants?slug=acme` returns only `acme`. It fails.
3. Add `Slug` to `store.TenantFilter` and the handler, until the test passes.

## What the spec doesn't cover

- **Request validation.** The tests deliberately send invalid requests, so the
  contract checks responses only. The handlers' own validation decides what is
  rejected, and the spec documents it.
- **The RabbitMQ messages.** The `tasks` and `task_updates` messages are
  defined in [internal/domain/messages.go](../internal/domain/messages.go).
  They could get their own AsyncAPI spec with the same kind of check.
- **Behaviour across calls.** For example: "a second PATCH with the same
  version is rejected", or "the name changes only when the task is done".
  The ordinary tests cover these.
