# Update a tenant

The commands to update (rename) a tenant, what repeated or concurrent updates
do, and each way an update can fail. Every output below is from a real run.

## How update works

```
active ──PATCH──▶ updating ──worker done──▶ active
                      └─────worker failed──▶ failed
```

- PATCH is allowed only when the tenant is **`active`**.
- The body must include the tenant's current **`version`**. This is *optimistic
  locking*: you prove which version you read, and the update only applies if
  nobody changed the tenant since.
- Every status change adds 1 to `version`, so one successful PATCH moves it by
  2: once to `updating`, once back to `active`.

Start the stack first (`make up`), and have an active tenant. The examples use
`globex` from [Create a tenant](create-tenant.md).

## Success

Read the tenant to get its current version:

```bash
curl -s localhost:8000/tenants/$ID
```

```json
{"id":"6990affc-0bd0-4903-9784-cd1fb0195ab4","slug":"globex","name":"Globex Corporation","status":"active","version":2,"created_at":"2026-11-04T18:06:54.846Z","updated_at":"2026-11-04T18:06:57.271Z"}
```

Send the new name with that version:

```bash
curl -s -w '\nHTTP %{http_code}\n' -X PATCH localhost:8000/tenants/$ID \
  -H 'Content-Type: application/json' \
  -d '{"name":"Globex Inc","version":2}'
```

```
{"id":"6990affc-0bd0-4903-9784-cd1fb0195ab4","slug":"globex","name":"Globex Inc","status":"updating","version":3,"created_at":"2026-11-04T18:06:54.846Z","updated_at":"2026-11-04T18:12:58.408Z"}

HTTP 202
```

**202 Accepted**: the new name is saved, the tenant is `updating` at version 3,
and an `update` task has been queued. Wait 3–5 seconds:

```bash
curl -s localhost:8000/tenants/$ID
```

```json
{"id":"6990affc-0bd0-4903-9784-cd1fb0195ab4","slug":"globex","name":"Globex Inc","status":"active","version":4,"created_at":"2026-11-04T18:06:54.846Z","updated_at":"2026-11-04T18:13:01.643Z"}
```

Back to `active`, now at version 4. The task that did the work:

```bash
curl -s "localhost:8000/tasks?tenant_id=$ID&type=update"
```

```json
{"items":[{"type":"update","status":"done", ...}],"next_cursor":null}
```

Only `name` can be changed. The slug is permanent.

## Repeating a PATCH is safe (idempotent)

Sending the same PATCH more than once never applies it twice. The first
request wins; every repeat is rejected with 409 and **changes nothing**. That
is what makes a PATCH safe to retry, for example after a network timeout.

### Repeat it straight away

While the first PATCH is still being applied, the tenant is `updating`:

```bash
curl -s -X PATCH localhost:8000/tenants/$ID -H 'Content-Type: application/json' \
  -d '{"name":"Globex Inc","version":2}'
```

```json
{"error":{"code":"tenant_update_not_allowed","message":"the tenant's current status does not allow this operation","details":{}}}
```

### Repeat it after the update finished

The tenant is `active` again, but at version 4, so `"version":2` is out of date:

```bash
curl -s -X PATCH localhost:8000/tenants/$ID -H 'Content-Type: application/json' \
  -d '{"name":"Globex Inc","version":2}'
```

```json
{"error":{"code":"tenant_version_conflict","message":"the tenant was modified; re-read it and retry with the current version","details":{}}}
```

Nothing changed. The tenant is still:

```json
{"name":"Globex Inc","status":"active","version":4}
```

and there is still exactly **one** `update` task.

### Send many at the same moment

Ten identical PATCHes in parallel, all with the same version:

```bash
seq 10 | xargs -P 10 -I{} curl -s -o /dev/null -w '%{http_code}\n' \
  -X PATCH localhost:8000/tenants/$ID -H 'Content-Type: application/json' \
  -d '{"name":"Globex Global","version":6}' | sort | uniq -c
```

```
   1 202
   9 409
```

Exactly one wins. The other nine get 409: `tenant_version_conflict` or
`tenant_update_not_allowed`, depending on whether they arrive after the winner
bumped the version or while the tenant is `updating`. One update task is
created, not ten. `make race` runs the same test with 20 requests.

### Why it works

The whole check and the change are **one SQL statement**
([`internal/store/tenants.go`](../internal/store/tenants.go), `UpdateTenant`;
simplified here, the real query passes the statuses as parameters):

```sql
UPDATE tenants
SET name = $2, status = 'updating', version = version + 1, updated_at = now()
WHERE id = $1 AND version = $4 AND status = 'active'
```

Postgres runs it atomically. The first request matches the row and bumps the
version; every other request with the same version then matches zero rows.
Nothing can slip in between a check and the write, because there is no
separate check.

### How to retry correctly

A 409 tells you the tenant has moved on. To make a further change, read it
again and use the version it reports:

```bash
curl -s localhost:8000/tenants/$ID          # "version":4
curl -s -X PATCH localhost:8000/tenants/$ID -H 'Content-Type: application/json' \
  -d '{"name":"Globex International","version":4}'   # 202
```

If you are unsure whether your earlier PATCH went through (say, after a
timeout), read the tenant. If it shows your new name, it went through.

## Failure cases

### The tenant isn't active: 409

PATCH while `provisioning`, `updating`, `destroying`, `destroyed` or `failed`:

```bash
curl -s -X PATCH localhost:8000/tenants/<destroyed tenant id> \
  -H 'Content-Type: application/json' -d '{"name":"X","version":4}'
```

```json
{"error":{"code":"tenant_update_not_allowed","message":"the tenant's current status does not allow this operation","details":{}}}
```

### The version is out of date: 409

```json
{"error":{"code":"tenant_version_conflict","message":"the tenant was modified; re-read it and retry with the current version","details":{}}}
```

### The tenant doesn't exist: 404

```bash
curl -s -X PATCH localhost:8000/tenants/00000000-0000-0000-0000-000000000000 \
  -H 'Content-Type: application/json' -d '{"name":"X","version":1}'
```

```json
{"error":{"code":"tenant_not_found","message":"tenant not found","details":{}}}
```

### The body is invalid: 422

| Body | `details` in the response |
|---|---|
| `{"name":"X"}` | `"version":"is required"` |
| `{"name":"X","version":0}` | `"version":"must be a positive integer"` |
| `{"version":8}` | `"name":"is required"` |
| `{"name":"  ","version":8}` | `"name":"must not be empty"` |
| `{"name":"X","version":8,"slug":"new"}` | `"body":"json: unknown field \"slug\""` |
| `{"name":"X","version":"8"}` | `"body":"field version has the wrong type"` |

Each is a 422 `validation_error`.

### The update job itself fails

The PATCH is accepted (202), but the background job fails. Use
`make worker-fail` to cause this:

```bash
make worker-fail
curl -s -X PATCH localhost:8000/tenants/$ID -H 'Content-Type: application/json' \
  -d '{"name":"Stark Enterprises","version":2}'      # 202, "status":"updating"
```

A few seconds later:

```json
{"id":"05daaf18-8ab8-4f9a-8e1f-c9aebc6dee25","slug":"stark","name":"Stark Enterprises","status":"failed","version":4,"created_at":"2026-11-04T18:13:35.549Z","updated_at":"2026-11-04T18:13:44.777Z"}
```

The task shows why:

```json
{"type":"update","status":"failed","error":"simulated update failure"}
```

Two things to know:

- **The new name is kept.** The name is saved when the PATCH is accepted; a
  failed job doesn't roll it back.
- **A failed tenant can't be updated again** (409 `tenant_update_not_allowed`).
  It can only be deleted; see [Delete a tenant](delete-tenant.md).

Run `make worker-normal` afterwards.

## Summary

| Situation | PATCH returns | Tenant after |
|---|---|---|
| `active`, current version | 202 | `updating`, then `active` (version +2) |
| Same PATCH repeated, while `updating` | 409 `tenant_update_not_allowed` | unchanged |
| Same PATCH repeated, after it finished | 409 `tenant_version_conflict` | unchanged |
| N identical PATCHes at once | one 202, N−1 × 409 | changed once |
| Not `active` | 409 `tenant_update_not_allowed` | unchanged |
| Unknown id, or not a UUID | 404 `tenant_not_found` | — |
| Invalid body | 422 `validation_error` | unchanged |
| Accepted, but the job fails | 202 | `failed`, new name kept |
