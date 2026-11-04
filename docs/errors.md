# Error messages

Every error the API returns, what it means and when it happens.

## The error format

Every error response has the same shape:

```json
{
  "error": {
    "code": "validation_error",
    "message": "request validation failed",
    "details": { "slug": "is required" }
  }
}
```

| Field | Meaning |
|---|---|
| `code` | A fixed, machine-readable string. Code against this, not the message. |
| `message` | A short sentence for people. |
| `details` | Extra information. For `validation_error` it maps each bad field to what is wrong. Otherwise it is `{}`. |

All errors are written in one place, [`internal/api/errors.go`](../internal/api/errors.go).
The store returns plain Go errors ([`internal/domain/errors.go`](../internal/domain/errors.go)),
and one table maps each to an HTTP status.

## All error codes

| HTTP | `code` | `message` | Thrown when |
|---|---|---|---|
| 404 | `tenant_not_found` | tenant not found | No tenant has that id, **or the id in the path is not a valid UUID**. |
| 404 | `task_not_found` | task not found | No task has that id, or the id is not a valid UUID. |
| 404 | `route_not_found` | no such route | The URL doesn't exist, for example `/tenant` instead of `/tenants`. |
| 405 | `method_not_allowed` | method not allowed | The URL exists but not with that method, for example `PUT /tenants`. |
| 409 | `tenant_already_exists` | a tenant with this slug already exists | `POST /tenants` with a slug that is taken. Slugs stay taken after a tenant is destroyed. |
| 409 | `tenant_update_not_allowed` | the tenant's current status does not allow this operation | `PATCH` on a tenant that isn't `active`, or `DELETE` on one that isn't `active` or `failed`. |
| 409 | `tenant_version_conflict` | the tenant was modified; re-read it and retry with the current version | `PATCH` with a `version` that is no longer current. |
| 422 | `validation_error` | request validation failed | The request body or query is invalid. `details` says which field and why. |
| 500 | `internal_error` | internal server error | An unexpected error. The cause is logged on the server and never sent to the client. |
| 503 | `service_unavailable` | a dependency is unhealthy | Only from `/healthz`: the database or RabbitMQ is unreachable. |

## Which endpoint can return which error

| Endpoint | Success | Possible errors |
|---|---|---|
| `POST /tenants` | 201 | 409 `tenant_already_exists`, 422 `validation_error` |
| `GET /tenants` | 200 | 422 `validation_error` (bad `status`, `limit` or `cursor`) |
| `GET /tenants/{id}` | 200 | 404 `tenant_not_found` |
| `PATCH /tenants/{id}` | 202 | 404 `tenant_not_found`, 409 `tenant_update_not_allowed`, 409 `tenant_version_conflict`, 422 `validation_error` (see [Update a tenant](update-tenant.md)) |
| `DELETE /tenants/{id}` | 202 | 404 `tenant_not_found`, 409 `tenant_update_not_allowed` |
| `GET /tasks` | 200 | 422 `validation_error` (bad `tenant_id`, `status`, `type`, `limit` or `cursor`) |
| `GET /tasks/{id}` | 200 | 404 `task_not_found` |
| `GET /healthz` | 200 | 503 `service_unavailable` |

Any endpoint can also return 500 `internal_error`, and any wrong URL or method
gives 404 `route_not_found` or 405 `method_not_allowed`.

## Every `validation_error` detail

These are the exact texts that appear in `details`.

### Request body (`POST` and `PATCH`)

| `details` | Thrown when | Example body |
|---|---|---|
| `"body": "must not be empty"` | No body was sent | *(none)* |
| `"body": "is not valid JSON"` | The JSON is broken | `{"slug":"bad",` |
| `"body": "must be a JSON object"` | The body is JSON but not an object | `[]` |
| `"body": "must contain a single JSON object"` | Extra data after the object | `{"slug":"a","name":"b"}{}` |
| `"body": "json: unknown field \"plan\""` | A field the API doesn't accept | `{"slug":"x","name":"X","plan":"gold"}` |
| `"body": "field slug has the wrong type"` | A field has the wrong JSON type | `{"slug":123,"name":"X"}` |
| `"body": "is too large"` | The body is over 1 MB | |

### Tenant fields

| `details` | Thrown when |
|---|---|
| `"slug": "is required"` | `POST` without `slug` |
| `"slug": "must be 3-28 characters of a-z, 0-9 and '-', start with a letter and not end with '-'"` | The slug breaks the format: uppercase, too short or long, a starting digit, a trailing hyphen, or any other character |
| `"name": "is required"` | `POST` or `PATCH` without `name` |
| `"name": "must not be empty"` | `name` is empty or only spaces |
| `"version": "is required"` | `PATCH` without `version` |
| `"version": "must be a positive integer"` | `PATCH` with `version` 0 or less |

Several problems are reported together. For example, `{}` gives
`{"name":"is required","slug":"is required"}`.

### Query parameters (`GET /tenants`, `GET /tasks`)

| `details` | Thrown when |
|---|---|
| `"limit": "must be an integer between 1 and 100"` | `limit` is not a number, or is outside 1–100 |
| `"cursor": "is invalid"` | `cursor` wasn't produced by this API |
| `"status": "is not a known tenant status"` | `GET /tenants?status=` with anything other than `provisioning`, `active`, `updating`, `destroying`, `destroyed` or `failed` |
| `"status": "is not a known task status"` | `GET /tasks?status=` with anything other than `accepted`, `in_progress`, `done` or `failed` |
| `"type": "is not a known task type"` | `GET /tasks?type=` with anything other than `deploy`, `update` or `destroy` |
| `"tenant_id": "must be a UUID"` | `GET /tasks?tenant_id=` with something that isn't a UUID |

## Why some cases return what they do

- **An invalid UUID gives 404, not 422.** `GET /tenants/not-a-uuid` says
  `tenant_not_found`. From the client's point of view no tenant has that id,
  so the API doesn't reveal anything about id formats.
- **A taken slug gives 409, not 422.** The request is well-formed; it conflicts
  with existing data. The database's unique constraint decides this, so two
  requests at the same moment can't both succeed.
- **`tenant_update_not_allowed` is used for DELETE too.** Both PATCH and DELETE
  change the tenant's status, and the same rule decides both: what the current
  status allows. See [Delete a tenant](delete-tenant.md) for each case.
- **500 never shows the cause.** Internal error text could leak details such
  as SQL. The server logs it with the request path; the client sees only
  `internal_error`.

## Failures that are not HTTP errors

A tenant can also fail **after** the API returned success: the request was
accepted, but the background provisioning job failed. The tenant then ends in
status `failed`, and its task has an `error` text:

```json
{"type":"deploy","status":"failed","error":"simulated deploy failure", ...}
```

[Create a tenant and see it fail](create-tenant-errors.md#2-when-provisioning-itself-fails)
shows how to cause this.
