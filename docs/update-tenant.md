# Update a tenant

Renaming a tenant, what repeated or concurrent updates do, and each way an
update can fail.

## How update works

```
active ──PATCH──▶ updating ──worker done──▶ active  (new name applied)
                      └─────worker failed──▶ failed  (old name kept)
```

- PATCH is allowed only when the tenant is **`active`**.
- The body must include the tenant's current **`version`**. This is *optimistic
  locking*: you prove which version you read, and the update only applies if
  nobody changed the tenant since.
- **The new name is applied only when the update succeeds.** PATCH stores the
  requested name on the update task (`requested_name`). The tenant keeps its
  current name while `updating`, takes the new one when the task is `done`, and
  keeps the old one if the task `failed`.
- Every status change adds 1 to `version`, so one successful PATCH moves it by
  2: once to `updating`, once back to `active`.

Start the stack first (`make up`) and have an active tenant. See
[Create a tenant](create-tenant.md); the examples below use `wayne`.

## Success

Read the tenant to get its current version:

```bash
curl -s localhost:8000/tenants/$ID
```

```json
{"id":"8c2e2bf5-928c-4df5-9a61-f1537b17e945","slug":"wayne","name":"Wayne Enterprises","status":"active","version":2,"created_at":"2026-11-05T01:08:43.803Z","updated_at":"2026-11-05T01:08:46.870Z"}
```

Send the new name with that version:

```bash
curl -s -w '\nHTTP %{http_code}\n' -X PATCH localhost:8000/tenants/$ID \
  -H 'Content-Type: application/json' \
  -d '{"name":"Wayne Corp","version":2}'
```

```
{"id":"8c2e2bf5-928c-4df5-9a61-f1537b17e945","slug":"wayne","name":"Wayne Enterprises","status":"updating","version":3,"created_at":"2026-11-05T01:08:43.803Z","updated_at":"2026-11-05T01:08:48.845Z"}

HTTP 202
```

**202 Accepted**: the tenant is `updating` at version 3. Its name is still
**Wayne Enterprises**; the new name waits on the update task:

```bash
curl -s "localhost:8000/tasks?tenant_id=$ID&type=update"
```

```json
{"items":[{"id":"1831f500-f00a-4738-8056-d900b926a5ed","type":"update","tenant_id":"8c2e2bf5-928c-4df5-9a61-f1537b17e945","status":"accepted","error":null,"requested_name":"Wayne Corp","created_at":"2026-11-05T01:08:48.845Z","updated_at":"2026-11-05T01:08:48.845Z"}],"next_cursor":null}
```

Wait 3–5 seconds:

```bash
curl -s localhost:8000/tenants/$ID
```

```json
{"id":"8c2e2bf5-928c-4df5-9a61-f1537b17e945","slug":"wayne","name":"Wayne Corp","status":"active","version":4,"created_at":"2026-11-05T01:08:43.803Z","updated_at":"2026-11-05T01:08:52.207Z"}
```

The task finished `done`, so the tenant now has the new name **Wayne Corp**, is
`active` again, and is at version 4.

Only `name` can be changed. The slug is permanent.

## Repeating a PATCH is safe (idempotent)

Sending the same PATCH more than once never applies it twice. The first
request wins; every repeat is rejected with 409 and **changes nothing**. That
is what makes a PATCH safe to retry, for example after a network timeout.

### Repeat it straight away

While the first PATCH is still being applied, the tenant is `updating`:

```bash
curl -s -X PATCH localhost:8000/tenants/$ID -H 'Content-Type: application/json' \
  -d '{"name":"Wayne Corp","version":2}'
```

```json
{"error":{"code":"tenant_update_not_allowed","message":"the tenant's current status does not allow this operation","details":{}}}
```

### Repeat it after the update finished

The tenant is `active` again, but at version 4, so `"version":2` is out of date:

```bash
curl -s -X PATCH localhost:8000/tenants/$ID -H 'Content-Type: application/json' \
  -d '{"name":"Wayne Corp","version":2}'
```

```json
{"error":{"code":"tenant_version_conflict","message":"the tenant was modified; re-read it and retry with the current version","details":{}}}
```

Nothing changed: the tenant is still `"name":"Wayne Corp","status":"active","version":4`,
and there is still exactly **one** `update` task.

### Send many at the same moment

Ten identical PATCHes in parallel, all with the tenant's current version.
This run was on another tenant, `globex`, which was at version 6; use your
tenant's current version:

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
([`internal/store/postgres/tenants.go`](../internal/store/postgres/tenants.go), `UpdateTenant`;
simplified here, the real query passes the statuses as parameters):

```sql
UPDATE tenants
SET status = 'updating', version = version + 1, updated_at = now()
WHERE id = $1 AND version = $3 AND status = 'active'
```

Postgres runs it atomically. The first request matches the row and bumps the
version; every other request with the same version then matches zero rows.
Nothing can slip in between a check and the write, because there is no
separate check. In the same transaction, the winner inserts the update task
carrying the requested name.

When the worker reports the task `done`, the consumer applies the name
([`internal/store/postgres/updates.go`](../internal/store/postgres/updates.go), `ApplyUpdate`):

```sql
UPDATE tenants
SET status = 'active', name = COALESCE($3, name), version = version + 1, updated_at = now()
WHERE id = $1
```

`$3` is the task's `requested_name` when the update is `done`, and NULL when it
`failed`, so `COALESCE` keeps the old name.

### How to retry correctly

A 409 tells you the tenant has moved on. To make a further change, read it
again and use the version it reports:

```bash
curl -s localhost:8000/tenants/$ID          # "version":4
curl -s -X PATCH localhost:8000/tenants/$ID -H 'Content-Type: application/json' \
  -d '{"name":"Wayne Holdings","version":4}'   # 202
```

If you are unsure whether an earlier PATCH went through (say, after a timeout),
look at the tenant's update tasks: `GET /tasks?tenant_id=$ID&type=update` shows
each one with its `requested_name` and `status`.

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
`make worker-fail` to cause this. Continuing with `wayne`, now named
**Wayne Corp** at version 4:

```bash
make worker-fail
curl -s -X PATCH localhost:8000/tenants/$ID -H 'Content-Type: application/json' \
  -d '{"name":"Wayne Holdings","version":4}'      # 202, "status":"updating"
```

A few seconds later:

```json
{"id":"8c2e2bf5-928c-4df5-9a61-f1537b17e945","slug":"wayne","name":"Wayne Corp","status":"failed","version":6,"created_at":"2026-11-05T01:08:43.803Z","updated_at":"2026-11-05T01:08:57.668Z"}
```

The tenant is `failed` and **keeps its old name, Wayne Corp**. The rejected
name is only on the failed task, next to the reason:

```bash
curl -s "localhost:8000/tasks?tenant_id=$ID&type=update" | jq -c '.items[] | {status, requested_name, error}'
```

```
{"status":"done","requested_name":"Wayne Corp","error":null}
{"status":"failed","requested_name":"Wayne Holdings","error":"simulated update failure"}
```

A `failed` tenant can't be updated again (409 `tenant_update_not_allowed`).
It can only be deleted; see [Delete a tenant](delete-tenant.md).

Run `make worker-normal` afterwards.

## Summary

| Situation | PATCH returns | Tenant after |
|---|---|---|
| `active`, current version | 202 (old name, `updating`) | `active` with the new name (version +2) |
| Same PATCH repeated, while `updating` | 409 `tenant_update_not_allowed` | unchanged |
| Same PATCH repeated, after it finished | 409 `tenant_version_conflict` | unchanged |
| N identical PATCHes at once | one 202, N−1 × 409 | changed once |
| Not `active` | 409 `tenant_update_not_allowed` | unchanged |
| Unknown id, or not a UUID | 404 `tenant_not_found` | — |
| Invalid body | 422 `validation_error` | unchanged |
| Accepted, but the job fails | 202 | `failed`, **old name kept** |
