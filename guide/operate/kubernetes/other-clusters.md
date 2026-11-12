---
audience: "whoever stands up Agent Fleet on a Kubernetes cluster other than GKE (an SRE with admin rights on the cluster)"
source_of_truth: "the manifests under `deploy/kubernetes/` (they win where this page disagrees)"
---

# Build on another cluster

English | [日本語](other-clusters.ja.md)

Stand Agent Fleet up on a cluster other than GKE from the generic overlay, `deploy/kubernetes/overlays/generic`.
That overlay is the base with nothing provider-specific. Everything Terraform does on GKE is yours to provide.

## How it works

| What you do | What happens |
|---|---|
| Meet preconditions P1–P14 yourself and check that the ranges meet P15 | The cluster can isolate workspaces. Nothing else checks them |
| Copy, edit and apply `overlays/generic` | The namespaces, the CP, RBAC and NetworkPolicies are created |
| Put your own ingress in front of the Service `af-cp` | The Console is reachable from outside |
| Add a backup for `af-cp-data` and an ingress policy in the CP namespace | The same protection as the GKE setup |

The list of preconditions is in [The Kubernetes deployment at a glance](overview.md#preconditions).

## Steps

Copy `overlays/generic`, and:

1. Set the copy's `deployment.yaml` from your cluster
   - the namespaces (with your own prefix)
   - a StorageClass that meets P3
   - check too that the pod, service, node and control-plane ranges meet P15
2. Put the store's DSN in `af-cp-secrets` as `AF_DATABASE_URL`
3. Edit `cp.env` where needed

| When | Setting |
|---|---|
| Workspaces must run on particular nodes | `AF_K8S_NODE_SELECTOR` in `cp.env` |
| The images need a pull secret | Create it in the workspace namespace and set `AF_K8S_IMAGE_PULL_SECRET` |

4. Put your cluster's ingress in front of the Service `af-cp` (port 8099)
   - with WebSockets and a long idle timeout
   - set `AF_TRUSTED_PROXY_HOPS` to the number of proxies that append to `X-Forwarded-For`, counting a proxy only if the CP cannot be reached except through it (why: [Build on GKE](gke.md#7-know-the-load-balancer))
5. Give the claim `af-cp-data` a backup. What it looks like on GKE is in [Back up and restore](backup.md#what-has-to-survive)
6. Add, in the CP namespace, a policy like `components/gke/cp-networkpolicy.yaml` that admits only your ingress to port 8099. On a cluster that runs other workloads, anything that reaches that port can name any user
7. Meet P1–P14 yourself. Nothing else checks them

The rules for changing the manifests are in [The Kubernetes deployment at a glance](overview.md#rules-for-changing-the-manifests).

## Related

- [Build on GKE](gke.md): creating the Secret and the checks are the same
- [Replace nodes and recover from a stopped node](nodes.md)
