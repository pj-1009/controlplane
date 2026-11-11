# Running on Kubernetes

How to run the whole system on a local Kubernetes cluster instead of Docker
Compose, what's deployed, and how to adapt it for a real cluster.

The Go code is the same for both. Kubernetes runs the same two images, with
the same environment variables, as Docker Compose does.

## 1. What you need

Everything from [Setup](setup.md#1-what-you-need), plus:

| Tool | Why | Install | Check it with |
|---|---|---|---|
| kubectl | Talks to the cluster | `brew install kubectl` | `kubectl version --client` |
| kind | Runs a Kubernetes cluster inside Docker | `brew install kind`, or `go install sigs.k8s.io/kind@v0.27.0` | `kind version` |

If you installed kind with `go install`, it is in `~/go/bin`. Either add that
to your PATH or pass it to make: `make k8s-up KIND=~/go/bin/kind`.

Docker Desktop needs about **4 GB of memory** for the cluster (Settings →
Resources).

These ports must be free: **8080** (API) and **15673** (RabbitMQ UI). They are
different from the Compose stack's, so both can run at the same time.

## 2. Start everything

```bash
make k8s-up
```

The first run takes a few minutes, mostly to download the Kubernetes node
image. It:

1. creates `.env` with random passwords if it doesn't exist yet (the same file
   Docker Compose uses);
2. writes the five credentials from `.env` into `deploy/k8s/secret.env`
   (git-ignored), for the Kubernetes Secret;
3. creates a one-node kind cluster called `controlplane`, if it doesn't exist;
4. builds the `controlplane` and `worker` images and loads them into the cluster;
5. applies everything in [`deploy/k8s/`](../deploy/k8s/) to the
   `controlplane` namespace;
6. waits until Postgres, RabbitMQ, both control plane replicas and the worker
   are ready.

It ends with:

```
API: http://localhost:8080   RabbitMQ UI: http://localhost:15673
```

Run `make k8s-up` again after changing code or manifests. It rebuilds the
images, applies the changes and restarts the control plane and worker pods.

## 3. Check it works

```bash
make k8s-status
```

The output looks like this (the `IP` and `NODE` columns are left out here):

```
NAME                                READY   STATUS    RESTARTS   AGE
pod/controlplane-677ffb664f-7dfmg   1/1     Running   0          2m8s
pod/controlplane-677ffb664f-k7cdt   1/1     Running   0          114s
pod/postgres-0                      1/1     Running   0          3m38s
pod/rabbitmq-0                      1/1     Running   0          2m8s
pod/worker-5cdccbc565-ptb52         1/1     Running   0          15s

NAME                   TYPE        CLUSTER-IP      EXTERNAL-IP   PORT(S)           AGE
service/controlplane   NodePort    10.96.203.56    <none>        8000:30080/TCP    3m38s
service/postgres       ClusterIP   None            <none>        5432/TCP          3m38s
service/rabbitmq       ClusterIP   None            <none>        5672/TCP          3m38s
service/rabbitmq-ui    NodePort    10.96.213.198   <none>        15672:31672/TCP   3m38s
```

Then use the API exactly as in the other guides, on port **8080** instead of
8000:

```bash
curl -s localhost:8080/healthz
curl -s -X POST localhost:8080/tenants -H 'Content-Type: application/json' \
  -d '{"slug":"acme","name":"Acme Corp"}'
```

The RabbitMQ UI is at <http://localhost:15673>. The user and password are the
same as for Docker Compose: `RABBITMQ_USER` and `RABBITMQ_PASSWORD` in `.env`.

## 4. Everyday commands

| Command | What it does |
|---|---|
| `make k8s-up` | Create the cluster if needed, build, deploy and wait until ready |
| `make k8s-status` | List the pods and services |
| `make k8s-logs` | Follow the control plane and worker logs from every pod, each line prefixed with its pod |
| `make k8s-worker-fail` | Make every task fail (sets `WORKER_FAIL_RATE=1` on the worker) |
| `make k8s-worker-normal` | Undo `k8s-worker-fail` |
| `make k8s-race` | Run the concurrent-requests race test against the cluster's API |
| `make k8s-down` | Remove the app from the cluster, but keep the cluster and the data volumes |
| `make k8s-clean` | Delete the whole cluster, data included |

Other useful commands, all against the `controlplane` namespace:

```bash
kubectl --context kind-controlplane -n controlplane scale deployment/worker --replicas=3
kubectl --context kind-controlplane -n controlplane logs deployment/worker
kubectl --context kind-controlplane -n controlplane delete pod -l app.kubernetes.io/name=worker
kubectl --context kind-controlplane -n controlplane exec -it postgres-0 -- sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"'
```

`make test` and `make check` still use Docker Compose for Postgres and
RabbitMQ; they don't need the cluster.

## 5. What's deployed

| File | Kubernetes objects | Notes |
|---|---|---|
| [kustomization.yaml](../deploy/k8s/kustomization.yaml) | ConfigMaps `controlplane-config` and `worker-config`, Secret `controlplane-secrets` | Ties everything together. Holds every non-secret setting, and the image names and tags |
| [controlplane.yaml](../deploy/k8s/controlplane.yaml) | Deployment (2 replicas), NodePort Service on 30080 | The API, outbox relay and update consumer |
| [worker.yaml](../deploy/k8s/worker.yaml) | Deployment (1 replica) | No Service: it only talks to RabbitMQ |
| [postgres.yaml](../deploy/k8s/postgres.yaml) | StatefulSet with a 1 Gi volume, headless Service | For local clusters only |
| [rabbitmq.yaml](../deploy/k8s/rabbitmq.yaml) | StatefulSet with a 1 Gi volume, headless Service, NodePort Service for the UI | For local clusters only |
| [kind-cluster.yaml](../deploy/k8s/kind-cluster.yaml) | The kind cluster itself | Maps NodePorts 30080 and 31672 to localhost 8080 and 15673 |

### How it maps from Docker Compose

| Docker Compose | Kubernetes |
|---|---|
| `controlplane` and `worker` services | Deployments; scale with `replicas` |
| `postgres` and `rabbitmq` with named volumes | StatefulSets with persistent volume claims |
| Variables in `docker-compose.yml` | ConfigMaps from `kustomization.yaml` |
| Passwords in `.env` | A Secret generated from `.env` |
| `healthcheck` with `/controlplane -healthcheck` | HTTP probes on `/livez` and `/healthz` |
| `depends_on: service_healthy` | Nothing: both services retry their connections until the database and broker are up |
| `restart: unless-stopped` | Kubernetes restarts failed containers by itself |

### Probes

The control plane has three probes. They check different things on purpose:

| Probe | Endpoint | Checks | On failure |
|---|---|---|---|
| Startup | `/livez` | The process is serving HTTP. It allows up to 60 seconds, because the server only listens once the database is connected and migrated | Restart the container |
| Liveness | `/livez` | The process is still serving HTTP. It checks no dependency | Restart the container |
| Readiness | `/healthz` | The database and RabbitMQ are reachable | Stop sending traffic to this pod until it recovers; no restart |

`/livez` deliberately ignores the database. If liveness used `/healthz`, a
database outage would make Kubernetes restart every control plane pod, which
can't fix the database and only adds load.

The worker has no HTTP server, so it has no probes. If it hits a fatal error,
it exits and Kubernetes restarts it.

### Shutdown

When a control plane pod stops, it first waits 5 seconds (`preStop`), so the
Service stops routing new requests to it. Then it gets SIGTERM, stops
accepting requests and finishes the ones in flight, for up to 10 seconds. A
worker that is stopped mid-task leaves the task unacknowledged. RabbitMQ gives
it to another worker, and the deterministic message ids make the repeat
harmless.

### Security settings

Both app containers run as the distroless non-root user (65532) with a
read-only root filesystem, no privilege escalation, all Linux capabilities
dropped, and the default seccomp profile.

## 6. Changing settings

Non-secret settings are in `configMapGenerator` in
[kustomization.yaml](../deploy/k8s/kustomization.yaml): for example
`WORKER_CONCURRENCY`, `LOG_LEVEL` or `RELAY_INTERVAL`. Edit the value and run
`make k8s-up`. The ConfigMap gets a new name with each change, so the pods
restart with the new value automatically.

For a quick experiment without editing files:

```bash
kubectl --context kind-controlplane -n controlplane set env deployment/worker WORKER_CONCURRENCY=8
```

To change a password, see [Troubleshooting](#8-troubleshooting).

## 7. Running on a real cluster

The manifests are written for kind. For a real cluster (EKS, GKE, AKS and so
on), change these:

1. **Use managed Postgres and RabbitMQ.** Remove `postgres.yaml` and
   `rabbitmq.yaml` from `resources`, and set `DATABASE_URL` and `AMQP_URL` to
   the managed services. Put the full URLs in the Secret rather than building
   them from parts.
2. **Push the images to a registry.** Build and push `controlplane` and
   `controlplane-worker`, then set `newName` and `newTag` under `images` in
   `kustomization.yaml`. Use a real version tag, not `local`.
3. **Manage secrets properly.** Replace `secretGenerator` with your
   platform's secret store, such as External Secrets with AWS Secrets Manager,
   or Sealed Secrets.
4. **Expose the API with an Ingress or Gateway**, with TLS, instead of a
   NodePort.
5. **Add availability settings:** a PodDisruptionBudget for the control plane,
   and a HorizontalPodAutoscaler if load varies.

## 8. Troubleshooting

**`kind is not installed`.** Install it (see [What you need](#1-what-you-need)),
or pass its path: `make k8s-up KIND=~/go/bin/kind`.

**A control plane pod stays `0/1` and its logs say `database not ready, retrying`
with `password authentication failed`.** The Postgres volume was created with
an older password. Postgres, like RabbitMQ, only sets its password on first
boot. Delete the cluster and start again; this deletes the cluster's data:

```bash
make k8s-clean && make k8s-up
```

**`ErrImageNeverPull` or `ImagePullBackOff` for `controlplane:local`.** The
image wasn't loaded into kind. Run `make k8s-up` again; it builds and loads
both images.

**A pod is `Pending`.** Docker Desktop is short of memory. Give it at least
4 GB, or stop the Compose stack with `make down`.

**Port 8080 or 15673 is already in use.** Something else on your machine uses
it. Stop that program, or change `hostPort` in
[kind-cluster.yaml](../deploy/k8s/kind-cluster.yaml) and recreate the cluster
with `make k8s-clean && make k8s-up`.

**See why a pod isn't ready:**

```bash
kubectl --context kind-controlplane -n controlplane describe pod <pod name>
kubectl --context kind-controlplane -n controlplane logs <pod name>
```
