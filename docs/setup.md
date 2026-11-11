# Setup

How to get the control plane running on your machine and check that it works.

## 1. What you need

| Tool | Why | Check it with |
|---|---|---|
| Docker Desktop (or any Docker with Compose v2) | Runs every service, the tests and the linters | `docker compose version` |
| make | Runs the short commands below | `make --version` |
| git | Gets the code | `git --version` |
| curl | Calls the API | `curl --version` |

You do **not** need Go installed. Everything builds and runs in containers.

These ports must be free: **8000** (API), **5432** (Postgres), **5672** and **15672** (RabbitMQ).

On macOS, if `docker` is "command not found" even though Docker Desktop is
installed, add its CLI to your PATH and open a new terminal:

```bash
echo 'export PATH="/Applications/Docker.app/Contents/Resources/bin:$PATH"' >> ~/.zprofile
```

## 2. Get the code

```bash
git clone https://github.com/pj-1009/controlplane.git
cd controlplane
```

## 3. Start everything

Make sure Docker Desktop is running, then:

```bash
make up
```

The first run takes a minute or two to build. It:

1. creates `.env` with random Postgres and RabbitMQ passwords (only the first time; the file is git-ignored);
2. builds the `controlplane` and `worker` images;
3. starts Postgres, RabbitMQ, the control plane and the worker;
4. waits until every healthcheck passes.

It ends with:

```
Control plane API:  http://localhost:8000  (health: /healthz)
RabbitMQ UI:        http://localhost:15672 (user and password are in .env)
```

## 4. Check it works

```bash
curl -s localhost:8000/healthz
```

```json
{"checks":{"broker":"ok","database":"ok"},"status":"ok"}
```

Then follow [Create a tenant](create-tenant.md) to see one go from `provisioning` to `active`.

## 5. Open the RabbitMQ UI (optional)

Get the login from `.env`:

```bash
grep RABBITMQ .env
```

Open <http://localhost:15672> and log in with `RABBITMQ_USER` (`controlplane`)
and `RABBITMQ_PASSWORD`. The **Queues** tab shows `tasks`, `task_updates` and
the dead-letter queue `task_updates.dlq`.

## 6. Run the tests

```bash
make test     # all tests, with the race detector, against real Postgres and RabbitMQ
make check    # lint + security checks + vulnerability scan + all tests
```

`make test` also saves its output to `test-results/test-<timestamp>.txt`.

## 7. Stop or reset

| Command | What it does |
|---|---|
| `make logs` | Follow the logs of every service (Ctrl-C to stop following) |
| `make down` | Stop the containers. The database is kept. |
| `make up` | Start again, with the same data |
| `make clean` | Stop and **delete** the database and build caches. `.env` is kept. |

To run everything on a local Kubernetes cluster instead of Docker Compose, see
[Kubernetes](kubernetes.md).

## Troubleshooting

**`make up` says `container controlplane-controlplane-1 is unhealthy`.**
Look at its logs:

```bash
docker compose logs --tail 20 controlplane
```

- `database not ready, retrying … password authentication failed`: the
  database was created with a different password than the one now in `.env`.
  This happens after deleting `.env`, changing a password, or starting a second
  copy of the repo, since all copies share one database volume. Fix:

  ```bash
  make clean && make up
  ```

  This deletes the local database, so you lose any tenants you created.

- `database not ready, retrying … connection refused`: Postgres isn't up yet or
  has crashed. Check `docker compose logs --tail 20 postgres`.

**`Bind for 0.0.0.0:8000 failed: port is already allocated`.** Something else
is using that port. Stop it, or run the API on another port with
`HTTP_PORT=8080 make up` (then use `localhost:8080` in the commands).

**`Cannot connect to the Docker daemon`.** Docker Desktop isn't running. Start it and try again.

**RabbitMQ login fails.** Use the password from `.env` in the folder you ran
`make up` from. A second copy of the repo has its own `.env` with a different password.
