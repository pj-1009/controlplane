# Docs

Guides for running and using the control plane. The main [README](../README.md)
covers the design and the API reference.

| Guide | What it covers |
|---|---|
| [Setup](setup.md) | What to install, how to start everything, check it works, and fix common problems |
| [Kubernetes](kubernetes.md) | Running everything on a local kind cluster with `make k8s-up`, what's deployed, and what to change for a real cluster |
| [Create a tenant](create-tenant.md) | Create a tenant and watch it go from `provisioning` to `active` |
| [Create a tenant and see it fail](create-tenant-errors.md) | Every way creation is rejected, and how to make provisioning itself fail |
| [Update a tenant](update-tenant.md) | Rename a tenant, why repeated or parallel PATCHes apply only once, and each way an update fails |
| [Delete a tenant](delete-tenant.md) | Delete a tenant, see it succeed, and see each way it can fail |
| [Running the tests](testing.md) | How to run all or part of the tests, read the results, and the other checks (lint, vulnerabilities, race test) |
| [CI/CD](cicd.md) | Running lint, formatting and vulnerability checks on demand, and using `make check` as a CI gate |
| [The API contract](spec-driven.md) | `spec/openapi.yaml`, how the tests enforce it, and the spec-first workflow for API changes |
| [Database providers](database-providers.md) | Switching between Postgres and the in-memory store with `DB_DRIVER`, the guarantees a provider must keep, and how to add one |
| [Error messages](errors.md) | Every error code and message, and exactly when each is returned |

Ids and timestamps in the example outputs will differ from yours.
