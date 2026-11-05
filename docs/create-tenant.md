# Create a tenant

The commands to create a tenant and watch it become `active`. Every output
below is from a real run; your ids and times will differ.

Start the stack first (see [Setup](setup.md)):

```bash
make up
```

## 1. Create it

```bash
curl -s -i -X POST localhost:8000/tenants \
  -H 'Content-Type: application/json' \
  -d '{"slug":"globex","name":"Globex Corporation"}'
```

```
HTTP/1.1 201 Created
Location: /tenants/6990affc-0bd0-4903-9784-cd1fb0195ab4
(other headers trimmed)

{"id":"6990affc-0bd0-4903-9784-cd1fb0195ab4","slug":"globex","name":"Globex Corporation","status":"provisioning","version":1,"created_at":"2026-11-04T18:06:54.846Z","updated_at":"2026-11-04T18:06:54.846Z"}
```

What to notice:

- **201 Created**, and the `Location` header gives the new tenant's URL.
- `"status":"provisioning"` and `"version":1`: the tenant exists, but the work
  to set it up hasn't happened yet.
- The response is instant. The API saved the tenant, its deploy task and an
  outbox message in one database transaction, and returned. It didn't wait for
  the worker.

Save the id so the next commands can use it:

```bash
ID=6990affc-0bd0-4903-9784-cd1fb0195ab4   # paste your own id here
```

**Shortcut:** instead of the command above, you can create the tenant and save
its id in one step. Use it *instead of* step 1, not as well: creating the same
slug twice fails with 409. It needs `jq`.

```bash
ID=$(curl -s -X POST localhost:8000/tenants -H 'Content-Type: application/json' \
  -d '{"slug":"globex","name":"Globex Corporation"}' | jq -r .id)
echo $ID
```

## 2. Watch it finish

Check it straight away:

```bash
curl -s localhost:8000/tenants/$ID
```

```json
{"id":"6990affc-…","slug":"globex","name":"Globex Corporation","status":"provisioning","version":1,…}
```

Wait 3–5 seconds and check again:

```bash
curl -s localhost:8000/tenants/$ID
```

```json
{"id":"6990affc-0bd0-4903-9784-cd1fb0195ab4","slug":"globex","name":"Globex Corporation","status":"active","version":2,"created_at":"2026-11-04T18:06:54.846Z","updated_at":"2026-11-04T18:06:57.271Z"}
```

It is now `"status":"active"`, and `version` went from 1 to 2. Every status
change adds 1 to the version.

## 3. See the task that did the work

```bash
curl -s "localhost:8000/tasks?tenant_id=$ID"
```

```json
{
  "items": [
    {
      "id": "5855f4be-1738-4587-84cb-cf6486faea98",
      "type": "deploy",
      "tenant_id": "6990affc-0bd0-4903-9784-cd1fb0195ab4",
      "status": "done",
      "error": null,
      "requested_name": null,
      "created_at": "2026-11-04T18:06:54.846Z",
      "updated_at": "2026-11-04T18:06:57.271Z"
    }
  ],
  "next_cursor": null
}
```

The `deploy` task went `accepted` → `in_progress` → `done`.

## 4. Follow it in the logs (optional)

Run this in a second terminal **before** step 1:

```bash
make logs
```

You'll see the tenant travel through the system, in this order:

| Service | Log message | What happened |
|---|---|---|
| controlplane | `tenant created` | The API saved the tenant, task and outbox row |
| controlplane | `outbox rows published` | The relay sent the task to RabbitMQ |
| worker | `task received` | The worker picked it up |
| worker | `update published` (twice) | It reported `in_progress`, then `done` |
| worker | `task acked` | It told RabbitMQ the task is finished |
| controlplane | `update applied` | The consumer saved each update; the second one made the tenant `active` |

## 5. List tenants

```bash
curl -s "localhost:8000/tenants?status=active&limit=5"
```

```json
{"items":[{"id":"…","slug":"globex","status":"active",…}],"next_cursor":null}
```

If there are more results, `next_cursor` holds a value. Pass it back as
`?cursor=<value>` to get the next page.

## Rules for the request

| Field | Rule |
|---|---|
| `slug` | Required. 3–28 characters: lowercase `a-z`, digits `0-9` and `-`. Must start with a letter and not end with `-`. Must not be taken by any tenant, including destroyed ones. |
| `name` | Required. Any text that isn't blank. Spaces at the start and end are trimmed. |

No other fields are accepted. To see what happens when you break these rules,
see [Create a tenant and see it fail](create-tenant-errors.md).
