# Docs

Step-by-step guides for running and using the control plane. The main
[README](../README.md) covers the design and the full API reference.

| Guide | What it covers |
|---|---|
| [Setup](setup.md) | What to install, how to start everything, check it works, and fix common problems |
| [Create a tenant](create-tenant.md) | Create a tenant and watch it go from `provisioning` to `active` |
| [Create a tenant and see it fail](create-tenant-errors.md) | Every way creation is rejected, and how to make provisioning itself fail |
| [Update a tenant](update-tenant.md) | Rename a tenant, why repeated or parallel PATCHes apply only once, and each way an update fails |
| [Delete a tenant](delete-tenant.md) | Delete a tenant, see it succeed, and see each way it can fail |
| [Error messages](errors.md) | Every error code and message, and exactly when each is returned |

Every command output in these guides was captured from a real run. Your ids
and timestamps will differ.
