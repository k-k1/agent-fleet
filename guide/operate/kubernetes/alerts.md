---
audience: "whoever watches a Kubernetes (GKE) deployment (an SRE who can set up Google Cloud monitoring)"
source_of_truth: "the manifests under `deploy/kubernetes/` and the CP's audit log (they win where this page disagrees)"
---

# Set up alerts

English | [日本語](alerts.ja.md)

On a GKE deployment, find out before the members do when the CP stops answering, a workspace cannot start or a node runs short of disk.
Logs go to Cloud Logging. Set the alerts up in Cloud Monitoring.

## How it works

| What you do | What happens |
|---|---|
| Create the alerts in the table below in Cloud Monitoring | You are notified when something fails |
| After removing members, query the audit log with SQL | You see what Destroy left behind. This alone is not in Cloud Logging |

## The alerts to set up

| Alert | How |
|---|---|
| The CP is not ready (its database included) | An uptime check on `https://<fqdn>/readyz` |
| The certificate is about to expire | The same uptime check, with SSL certificate validation and its expiry alert |
| A workspace stays `Pending` | A log-based metric on the `FailedScheduling` events of the workspace namespace, alerting when it persists for 10 minutes; the start dialog shows the same reason |
| The quota refused a start | A log-based metric on events whose message contains `exceeded quota` in the workspace namespace |
| A workspace node is short of disk | The node condition `DiskPressure`, or `kubernetes.io/node/ephemeral_storage/allocatable_bytes` against use, on the workspace pool |

## See what Destroy left behind

Destroy writes what it could not confirm gone to the audit log, which lives in the database, not in Cloud Logging. Check it after removing members:

```sql
SELECT at, target, detail FROM audit_log WHERE action = 'workspace.destroy' AND detail LIKE '%NOT deleted:%'
```

How to clear what you find is in [Replace nodes and recover from a stopped node](nodes.md#clean-up-what-destroy-left).

## Related

- [Replace nodes and recover from a stopped node](nodes.md): why a node runs short of disk
- [The Kubernetes deployment at a glance](overview.md#disk-quota-gke): the disk quota on GKE
