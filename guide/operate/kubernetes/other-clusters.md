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
| Copy, edit and apply `overlays/generic`, then create the Secret with `AF_DATABASE_URL` | The namespaces, the CP, RBAC and NetworkPolicies are created, and the CP starts |
| Put your own ingress in front of the Service `af-cp` | The Console is reachable from outside |
| Add a backup for `af-cp-data` and an ingress policy in the CP namespace | The same protection as the GKE setup |

The list of preconditions is in [The Kubernetes deployment at a glance](overview.md#preconditions).

## Steps

Set the shell variables and copy the overlay. If it lives outside this clone, fix its paths as in [Keep it outside the clone](gke.md#keep-it-outside-the-clone).

```bash
OVERLAY=<your overlay>     # where your copy of overlays/generic lives
CP_NS=<cpNamespace>        # deployment.yaml's cpNamespace
WS_NS=<wsNamespace>        # deployment.yaml's wsNamespace
cp -r deploy/kubernetes/overlays/generic "$OVERLAY"
```

1. Set the copy's `deployment.yaml` from your cluster
   - the namespaces `cpNamespace` and `wsNamespace` (with your own prefix)
   - a StorageClass that meets P3
   - `workspaceImage`, and the CP's tag under `images:` in `kustomization.yaml`
   - check too that the pod, service, node and control-plane ranges meet P15
2. Edit `cp.env`: `PUBLIC_BASE_URL`, `PUBLIC_DOMAIN`, the sign-in settings and `SUPER_ADMIN_EMAILS`, and where needed:

| When | Setting |
|---|---|
| Workspaces must run on particular nodes | `AF_K8S_NODE_SELECTOR` in `cp.env` |
| The images need a pull secret | `AF_K8S_IMAGE_PULL_SECRET` in `cp.env`; the secret itself is created in step 4 |

3. Apply the overlay. It creates the two namespaces, the CP, RBAC and the NetworkPolicies; the CP pod waits in `CreateContainerConfigError` until step 4
   ```bash
   kubectl config current-context                # the cluster you mean
   kubectl apply -k "$OVERLAY"
   ```
4. Create the CP's Secret in the CP namespace, with the store's DSN as `AF_DATABASE_URL` besides the keys, and wait for the rollout. Put each sign-in secret you use in the same Secret (`GOOGLE_OAUTH_CLIENT_SECRET`, `AF_OIDC_<ID>_CLIENT_SECRET`, `GITHUB_OAUTH_CLIENT_SECRET`); what the keys must be is in [Handle the deployment keys](../secure/deployment-keys.md). If the images need a pull secret, create it now in `$WS_NS` under the name you gave `AF_K8S_IMAGE_PULL_SECRET`
   ```bash
   kubectl -n "$CP_NS" create secret generic af-cp-secrets \
     --from-literal=AF_MASTER_KEY="$(cat master-key)" \
     --from-literal=AF_COOKIE_SECRET="$(openssl rand -hex 32)" \
     --from-literal=AF_DATABASE_URL="$(cat database-url)" \
     --from-literal=GOOGLE_OAUTH_CLIENT_SECRET="$(cat google-client-secret)"
   kubectl -n "$CP_NS" rollout status deployment/af-cp
   ```
5. Put your cluster's ingress in front of the Service `af-cp` (port 8099)
   - with WebSockets and a long idle timeout
   - set `AF_TRUSTED_PROXY_HOPS` to the number of proxies that append to `X-Forwarded-For`, counting a proxy only if the CP cannot be reached except through it (why: [Build on GKE](gke.md#7-know-the-load-balancer)), and apply the overlay again
6. Give the claim `af-cp-data` a backup. What it looks like on GKE is in [Back up and restore](backup.md#what-has-to-survive)
7. Add, in the CP namespace, a policy like `components/gke/cp-networkpolicy.yaml` that admits only your ingress to port 8099. On a cluster that runs other workloads, anything that reaches that port can name any user
8. Meet P1–P14 yourself. Nothing else checks them

The rules for changing the manifests are in [The Kubernetes deployment at a glance](overview.md#rules-for-changing-the-manifests).

## Related

- [Build on GKE](gke.md): the checks in step 8 apply here too
- [Replace nodes and recover from a stopped node](nodes.md)
