# Database providers

The control plane talks to its database only through one Go interface,
[`store.Store`](../internal/store/store.go). Each database is a **provider**:
a package that implements that interface. You pick one at startup with
`DB_DRIVER`, and no other code changes.

| Provider | `DB_DRIVER` | Package | Use it for |
|---|---|---|---|
| PostgreSQL | `postgres` (default) | [internal/store/postgres](../internal/store/postgres/) | Everything real |
| In memory | `memory` | [internal/store/memory](../internal/store/memory/) | Experiments and tests. Data is lost on restart, and processes don't share it |

## Switch provider

Set `DB_DRIVER` and restart the control plane:

```bash
DB_DRIVER=memory docker compose up -d --wait controlplane
```

The log then shows:

```
{"level":"WARN","msg":"using the in-memory store: data is lost when the process exits"}
```

To go back, restart without it (or set `DB_DRIVER=postgres` in `.env`):

```bash
docker compose up -d --wait controlplane
```

### On Kubernetes

`DB_DRIVER` is in the `controlplane-config` ConfigMap. For a quick try,
override it on the Deployment, and run **one** replica: each in-memory
store lives inside its own pod, so two replicas would each see different
tenants.

```bash
kubectl --context kind-controlplane -n controlplane scale deployment/controlplane --replicas=1
kubectl --context kind-controlplane -n controlplane set env deployment/controlplane DB_DRIVER=memory
```

To go back, remove the override and restore two replicas. The Postgres data
is still there:

```bash
kubectl --context kind-controlplane -n controlplane set env deployment/controlplane DB_DRIVER-
kubectl --context kind-controlplane -n controlplane scale deployment/controlplane --replicas=2
```

To change it permanently, edit `DB_DRIVER` in
[kustomization.yaml](../deploy/k8s/kustomization.yaml) and run `make k8s-up`.

An unknown name stops the process at startup:

```
controlplane: unknown database driver "mysql", want one of [postgres memory]
```

## How it fits together

```
cmd/controlplane/main.go
        │  drivers.Open(ctx, cfg.DBDriver, cfg.DatabaseURL, logger)
        ▼
internal/store/drivers ── "postgres" ─▶ internal/store/postgres  (SQL, migrations)
        │                └─ "memory" ──▶ internal/store/memory    (maps + a mutex)
        ▼
   store.Store  ◀── all the API, the outbox relay and the consumer ever see
```

- **[`internal/store`](../internal/store/store.go)** holds the interface, the
  shared types (filters, pages, cursors, update outcomes) and no database code.
  Its doc comment lists the guarantees every provider must keep.
- **[`internal/store/drivers`](../internal/store/drivers/drivers.go)** is the
  only place that knows which providers exist. It is a plain `switch`.
- **[`cmd/controlplane/main.go`](../cmd/controlplane/main.go)** calls
  `drivers.Open` once. The API, relay and consumer get a `store.Store` and
  never learn which database is behind it.

## The guarantees a provider must keep

The rest of the system relies on these, whatever the database:

1. **Atomic writes.** The tenant change, its task and its outbox message are
   stored together or not at all. The same goes for an update's message id,
   the task change and the tenant change.
2. **One winner per race.** Two creates with the same slug, or two PATCHes with
   the same version, can't both succeed, even from different processes.
3. **Domain errors only.** It returns `domain.ErrTenantNotFound`,
   `domain.ErrTenantAlreadyExists` and so on, never driver errors, so the API's
   error mapping works unchanged.
4. **Stable ordering.** Lists are ordered by `(created_at, id)` and paged with
   `store.Cursor`.
5. **Safe relaying.** Two relays never send the same outbox message at the
   same time, and a message is marked sent only after it was published.

Postgres gets these from transactions, unique constraints, conditional
`UPDATE`s and `FOR UPDATE SKIP LOCKED`. The memory provider gets them from
one mutex and by writing nothing until every check has passed.

## How the guarantees are tested

| Level | What | Command |
|---|---|---|
| **Conformance suite** | [internal/store/storetest](../internal/store/storetest/storetest.go): 11 tests of the guarantees above, including 20-way races. Every provider runs the same suite | part of `make test` |
| **The whole system** | The API, consumer, relay and end-to-end tests take their store from `testutil.Store`, which reads `TEST_DB_DRIVER`, so the same tests run on any provider | `make test` (Postgres), `make test-memory` |
| **Provider-specific** | Postgres also tests its migrations and that a rolled-back transaction leaves no outbox row | part of `make test` |

Both providers pass all of it.

## Add a provider, for example MySQL

1. **Create the package** `internal/store/mysql` with a type that implements
   `store.Store`. Add this line so the compiler checks it:
   ```go
   var _ store.Store = (*Store)(nil)
   ```
   The provider owns its schema: put its migrations in the package, and apply
   them in its `Open`, like [postgres.Open](../internal/store/postgres/postgres.go) does.
2. **Run the conformance suite** from the package's tests:
   ```go
   func TestConformance(t *testing.T) {
       storetest.Run(t, func(t *testing.T) store.Store { return newTestStore(t) })
   }
   ```
   Make it pass. Each failing test names the guarantee that's broken.
3. **Register it.** Add a `case` to
   [drivers.Open](../internal/store/drivers/drivers.go), and to
   `testutil.Store` so the whole suite can run on it.
4. **Run the whole system on it** with `TEST_DB_DRIVER=mysql`, the way
   `make test-memory` does for the memory provider.

What will need real thought for a SQL database other than Postgres:

| Postgres feature used | What to find in the new database |
|---|---|
| `FOR UPDATE SKIP LOCKED` (outbox relay) | MySQL 8 has it. Without it, run one relay, or claim rows with an `UPDATE … LIMIT` |
| Unique constraint error `23505` mapped to `ErrTenantAlreadyExists` | That database's duplicate-key error code (MySQL: 1062) |
| `INSERT … ON CONFLICT DO NOTHING` (dedup) | `INSERT IGNORE` or a duplicate-key check |
| Partial unique index (one open task per tenant) | A generated column with a unique index, or rely on the status checks |
| `pg_advisory_xact_lock` (migrations) | `GET_LOCK()` in MySQL, or the migration tool's own lock |
| `$1` placeholders, `uuid`, `timestamptz` | `?`, `BINARY(16)` or `CHAR(36)`, `DATETIME(6)` in UTC |

A document database such as DynamoDB or MongoDB can implement the interface
too. The atomic writes then need that database's transactions
(`TransactWriteItems`, multi-document transactions), and the outbox may be
better served by its change stream.
