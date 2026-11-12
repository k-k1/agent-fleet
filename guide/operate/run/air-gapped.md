---
audience: "Whoever installs a deployment on a host that cannot reach the outside network"
source_of_truth: "`deploy/compose/release.sh`, `load-images.sh` and `control-plane drawio-preseed` (where they disagree, the scripts and the code win)"
---

# Install into a closed network

English | [日本語](air-gapped.ja.md)

Agent Fleet also installs on a host that cannot reach the internet. You provide the images, TLS, the agent CLIs and the diagram icons without relying on the outside.

## How it works

| What you do | What happens |
|---|---|
| Mirror the images into an internal registry, or carry them in by hand | The deployment starts even though the host cannot reach a registry |
| Use a self-signed certificate or your internal CA | The Console opens over HTTPS without Let's Encrypt |
| Use a workspace image with the agent CLIs baked in | Workspaces do not fetch the CLIs at start |
| Fill the diagram icon cache in advance | `.drawio` diagrams draw with their icons |

> [!IMPORTANT]
> **"Runs in a closed network" means the deployment starts.** An agent can do nothing unless it reaches its model endpoint.
> This path is for hosts on a restricted internal network, not for a truly disconnected one.

## Provide the images

Images are distributed through GHCR, and releases carry no image tarball. On a host that cannot reach a registry, choose one:

| Way | When it fits |
|---|---|
| Mirror `ghcr.io/k-k1/agent-fleet/*` into an internal registry and point `REGISTRY` at it | You have an internal registry. `docker compose pull` works as is and upkeep is light |
| Export the images with `release.sh --save` on a networked machine and import them on the target with `load-images.sh` | You have no registry. Every upgrade repeats the build, copy and load |

To carry them in by hand, export the images on a machine with network access.

```bash
VERSION=<version> deploy/compose/release.sh --save
#   -> deploy/compose/dist/agent-fleet-images-<version>.tar.gz  (docker save, gzip)
```

Copy that tar plus the bundle to the target host and import it there.

```bash
deploy/compose/load-images.sh agent-fleet-images-<version>.tar.gz
docker compose up -d
```

Images carried in by hand are named `agent-fleet/{control-plane,workspace}:<version>` (override with `REGISTRY=`). Set `REGISTRY=agent-fleet` in the target's `.env` to match.

## Provide TLS

Let's Encrypt is out of reach from a closed network. Switch to `tls internal` (self-signed) as described in [Choose the tenant and provisioning model](../install-decisions.md#whether-to-use-tls-internal), or use a certificate from your internal CA.

## Bake in the agent CLIs

The default workspace image is the lean variant: it ships without the agent CLIs and installs them at container start, at the versions pinned in the image's `versions.json`. On a host that cannot reach the outside, that fails.

1. Build an image with the CLIs baked in (`BAKE_AGENT_CLIS=1`)
2. Pass `CLAUDE_INSTALL=0` through `WS_ENV` so Claude is not fetched at start

## Fill the diagram icon cache

The bundled drawio viewer draws diagrams offline, but vendor icons (AWS, GCP, Azure, Kubernetes, rack equipment and so on) are not bundled.
Normally the Control Plane fetches and caches them on first use. Without the outside, that fetch fails and **diagrams silently degrade to boxes, colours and labels**.

To avoid it, fill the cache from a directory you carry in.

1. On a machine that reaches the outside, clone the drawio repository once (`v31.1.8`)
2. Carry `drawio/src/main/webapp/stencils` in
3. On the Control Plane's host, run `control-plane drawio-preseed --from <directory>`

```sh
control-plane drawio-preseed --from /path/to/stencils        # default bundle: 49 sets, 17.0 MB
control-plane drawio-preseed --from /path/to/stencils --all  # everything: 203 sets, 40.8 MB
```

- The default bundle covers cloud and infrastructure diagrams and general drawing. `--list` shows beforehand what is in it and what is not
- Each file is checked against the bundled manifest's SHA-256 before it is stored, so the contents are guaranteed even when the transport is not trusted
- The cache is content-addressed with no index file. Carrying a filled directory to another host as a tar does the same job

## Related

- [Upgrade](upgrade.md)
- [Update the native package](../native/update.md#carry-it-in-by-hand): bring the native package onto a host that cannot reach the internet
- [Install Agent Fleet](../install.md)
