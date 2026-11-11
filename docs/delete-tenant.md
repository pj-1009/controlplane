# Delete a tenant

Deleting a tenant, and each way a delete can fail.

## How delete works

`DELETE` doesn't remove the row. It starts a `destroy` task:

```
active ──DELETE──▶ destroying ──worker done──▶ destroyed
failed ──DELETE──▶ destroying ──worker done──▶ destroyed
```

- DELETE is allowed only from **`active`** or **`failed`**. Any other status gets 409.
- The tenant stays readable with `"status":"destroyed"`, for history.
- Its slug stays taken; it can't be used for a new tenant.

Start the stack first: `make up`. On Kubernetes, use `make k8s-up` and port 8080
(see [Following the other guides on Kubernetes](kubernetes.md#following-the-other-guides-on-kubernetes)).

## Success: delete an active tenant

Create a tenant and wait until it's active (see [Create a tenant](create-tenant.md)):

```bash
ID=$(curl -s -X POST localhost:8000/tenants -H 'Content-Type: application/json' \
  -d '{"slug":"initech","name":"Initech"}' | jq -r .id)
sleep 5
curl -s localhost:8000/tenants/$ID      # "status":"active","version":2
```

Delete it:

```bash
curl -s -w '\nHTTP %{http_code}\n' -X DELETE localhost:8000/tenants/$ID
```

```
{"id":"5cb60669-32f8-4b23-91a4-dd0507a6dce2","slug":"initech","name":"Initech","status":"destroying","version":3,"created_at":"2026-11-04T18:07:37.438Z","updated_at":"2026-11-04T18:07:43.530Z"}

HTTP 202
```

**202 Accepted** means the delete has started, not that it's finished. The
tenant is now `destroying`. Wait 3–5 seconds:

```bash
curl -s localhost:8000/tenants/$ID
```

```json
{"id":"5cb60669-32f8-4b23-91a4-dd0507a6dce2","slug":"initech","name":"Initech","status":"destroyed","version":4,"created_at":"2026-11-04T18:07:37.438Z","updated_at":"2026-11-04T18:07:46.040Z"}
```

The `destroy` task that did the work:

```bash
curl -s "localhost:8000/tasks?tenant_id=$ID&type=destroy"
```

```json
{"items":[{"id":"c6b64f20-3e5c-4999-9edb-b5dcb72510f3","type":"destroy","tenant_id":"5cb60669-32f8-4b23-91a4-dd0507a6dce2","status":"done","error":null,"requested_name":null,"created_at":"2026-11-04T18:07:43.530Z","updated_at":"2026-11-04T18:07:46.040Z"}],"next_cursor":null}
```

## Success: delete a failed tenant

A tenant whose provisioning failed can still be deleted. Make one with
`make worker-fail` (see [Create a tenant and see it fail](create-tenant-errors.md#2-when-provisioning-itself-fails)),
then run `make worker-normal` so the delete itself can succeed:

```bash
curl -s -w '\nHTTP %{http_code}\n' -X DELETE localhost:8000/tenants/$ID
```

```
{"id":"ef902811-92b8-48bf-8e93-a61384f12697","slug":"hooli","name":"Hooli","status":"destroying","version":3,"created_at":"2026-11-04T18:07:25.895Z","updated_at":"2026-11-04T18:07:32.339Z"}

HTTP 202
```

A few seconds later it is `"status":"destroyed","version":4`.

If you leave `make worker-fail` on, the destroy task fails too, and the tenant
ends up `failed` again. You can delete it again once the worker is back to normal.

## Failure cases

### Delete it again while it's still being destroyed: 409

Run the DELETE twice in a row:

```bash
curl -s -X DELETE localhost:8000/tenants/$ID   # 202, now destroying
curl -s -X DELETE localhost:8000/tenants/$ID   # straight away
```

```json
{"error":{"code":"tenant_update_not_allowed","message":"the tenant's current status does not allow this operation","details":{}}}
```

### Delete a tenant that's already destroyed: 409

Same command, after it reached `destroyed`. Same response:

```json
{"error":{"code":"tenant_update_not_allowed","message":"the tenant's current status does not allow this operation","details":{}}}
```

### Delete a tenant that's still provisioning: 409

Create a tenant and delete it **immediately**, before the worker finishes (it
takes at least half a second):

```bash
ID=$(curl -s -X POST localhost:8000/tenants -H 'Content-Type: application/json' \
  -d '{"slug":"umbrella","name":"Umbrella"}' | jq -r .id)
curl -s -X DELETE localhost:8000/tenants/$ID
```

```json
{"error":{"code":"tenant_update_not_allowed","message":"the tenant's current status does not allow this operation","details":{}}}
```

Wait until it's `active`, and the same DELETE returns 202. The same rule
applies while a tenant is `updating` after a PATCH.

### Delete a tenant that doesn't exist: 404

```bash
curl -s -X DELETE localhost:8000/tenants/00000000-0000-0000-0000-000000000000
curl -s -X DELETE localhost:8000/tenants/initech     # a slug, not an id
```

Both return:

```json
{"error":{"code":"tenant_not_found","message":"tenant not found","details":{}}}
```

DELETE takes the tenant's **id**, not its slug.

### Reuse the slug of a destroyed tenant: 409

```bash
curl -s -X POST localhost:8000/tenants -H 'Content-Type: application/json' \
  -d '{"slug":"initech","name":"Initech Again"}'
```

```json
{"error":{"code":"tenant_already_exists","message":"a tenant with this slug already exists","details":{}}}
```

## Summary

| Tenant status | DELETE returns |
|---|---|
| `active` | 202, then `destroying` → `destroyed` |
| `failed` | 202, then `destroying` → `destroyed` |
| `provisioning`, `updating` | 409 `tenant_update_not_allowed` (wait until it's `active` or `failed`) |
| `destroying`, `destroyed` | 409 `tenant_update_not_allowed` |
| no such id, or not a UUID | 404 `tenant_not_found` |

To see destroyed tenants:

```bash
curl -s "localhost:8000/tenants?status=destroyed"
```
