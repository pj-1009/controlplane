# Create a tenant and see it fail

Commands that make tenant creation fail, and the exact error each one returns.
Every output below is from a real run. [Error messages](errors.md) lists every
error code in one table.

There are two kinds of failure:

1. **The API rejects the request** and nothing is created. You get a 4xx
   response straight away.
2. **The API accepts the request, but provisioning fails later.** You get 201,
   and the tenant ends up `failed` a few seconds later.

Start the stack first: `make up`. The examples assume a tenant with slug
`globex` already exists; create it with [Create a tenant](create-tenant.md).

## 1. Rejected by the API

### The slug is already taken: 409

```bash
curl -s -X POST localhost:8000/tenants -H 'Content-Type: application/json' \
  -d '{"slug":"globex","name":"Another Globex"}'
```

```json
{"error":{"code":"tenant_already_exists","message":"a tenant with this slug already exists","details":{}}}
```

Slugs stay taken forever, even after the tenant is destroyed.

### The slug has the wrong format: 422

Each of these gets the same answer:

```bash
curl -s -X POST localhost:8000/tenants -H 'Content-Type: application/json' -d '{"slug":"Globex","name":"Globex"}'   # uppercase
curl -s -X POST localhost:8000/tenants -H 'Content-Type: application/json' -d '{"slug":"ab","name":"AB"}'           # too short
curl -s -X POST localhost:8000/tenants -H 'Content-Type: application/json' -d '{"slug":"1acme","name":"One"}'       # starts with a digit
curl -s -X POST localhost:8000/tenants -H 'Content-Type: application/json' -d '{"slug":"acme-","name":"Acme"}'      # ends with a hyphen
```

```json
{"error":{"code":"validation_error","message":"request validation failed","details":{"slug":"must be 3-28 characters of a-z, 0-9 and '-', start with a letter and not end with '-'"}}}
```

### A field is missing or blank: 422

```bash
curl -s -X POST localhost:8000/tenants -H 'Content-Type: application/json' -d '{"name":"No Slug"}'
```

```json
{"error":{"code":"validation_error","message":"request validation failed","details":{"slug":"is required"}}}
```

```bash
curl -s -X POST localhost:8000/tenants -H 'Content-Type: application/json' -d '{"slug":"blank-name","name":"   "}'
```

```json
{"error":{"code":"validation_error","message":"request validation failed","details":{"name":"must not be empty"}}}
```

Every problem is reported at once:

```bash
curl -s -X POST localhost:8000/tenants -H 'Content-Type: application/json' -d '{}'
```

```json
{"error":{"code":"validation_error","message":"request validation failed","details":{"name":"is required","slug":"is required"}}}
```

### The body is not what the API expects: 422

| Command | `details` in the response |
|---|---|
| `curl -s -X POST localhost:8000/tenants -H 'Content-Type: application/json' -d '{"slug":"extra","name":"Extra","plan":"gold"}'` | `"body":"json: unknown field \"plan\""` |
| `curl -s -X POST localhost:8000/tenants -H 'Content-Type: application/json' -d '{"slug":123,"name":"Num"}'` | `"body":"field slug has the wrong type"` |
| `curl -s -X POST localhost:8000/tenants -H 'Content-Type: application/json' -d '{"slug":"bad",'` | `"body":"is not valid JSON"` |
| `curl -s -X POST localhost:8000/tenants -H 'Content-Type: application/json'` | `"body":"must not be empty"` |
| `curl -s -X POST localhost:8000/tenants -H 'Content-Type: application/json' -d '[]'` | `"body":"must be a JSON object"` |
| `curl -s -X POST localhost:8000/tenants -H 'Content-Type: application/json' -d '{"slug":"two","name":"Two"}{}'` | `"body":"must contain a single JSON object"` |

Each is a 422 `validation_error`. The API rejects unknown fields on purpose,
so a typo such as `"nmae"` is caught instead of silently ignored.

### The wrong URL or method: 404 or 405

```bash
curl -s -X POST localhost:8000/tenant -H 'Content-Type: application/json' -d '{}'   # "tenant", not "tenants"
```

```json
{"error":{"code":"route_not_found","message":"no such route","details":{}}}
```

```bash
curl -s -X PUT localhost:8000/tenants
```

```json
{"error":{"code":"method_not_allowed","message":"method not allowed","details":{}}}
```

### Looking up a tenant that doesn't exist: 404

```bash
curl -s localhost:8000/tenants/00000000-0000-0000-0000-000000000000
curl -s localhost:8000/tenants/not-a-uuid
```

Both return:

```json
{"error":{"code":"tenant_not_found","message":"tenant not found","details":{}}}
```

An id that isn't a valid UUID is treated as "no such tenant".

## 2. When provisioning itself fails

Here the API accepts the request, and the background job fails. Use the
worker's fail switch to make every task fail:

```bash
make worker-fail
```

Create a tenant:

```bash
ID=$(curl -s -X POST localhost:8000/tenants -H 'Content-Type: application/json' \
  -d '{"slug":"hooli","name":"Hooli"}' | jq -r .id)
```

The response is a normal **201** with `"status":"provisioning"`. Wait 3–5 seconds, then:

```bash
curl -s localhost:8000/tenants/$ID
```

```json
{"id":"ef902811-92b8-48bf-8e93-a61384f12697","slug":"hooli","name":"Hooli","status":"failed","version":2,"created_at":"2026-11-04T18:07:25.895Z","updated_at":"2026-11-04T18:07:28.677Z"}
```

The reason is on the task:

```bash
curl -s "localhost:8000/tasks?tenant_id=$ID"
```

```json
{"items":[{"id":"7ac396fc-6536-4e0d-946f-0abf0d7c9ef8","type":"deploy","tenant_id":"ef902811-92b8-48bf-8e93-a61384f12697","status":"failed","error":"simulated deploy failure","requested_name":null,"created_at":"2026-11-04T18:07:25.895Z","updated_at":"2026-11-04T18:07:28.677Z"}],"next_cursor":null}
```

**Turn failures off again** before you go on:

```bash
make worker-normal
```

A `failed` tenant can't be updated, but it can be deleted. See [Delete a tenant](delete-tenant.md).
