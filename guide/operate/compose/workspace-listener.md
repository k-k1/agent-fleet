---
audience: "someone whose Docker Compose deployment's workspaces can no longer reach the Control Plane"
source_of_truth: "how the Control Plane handles `AF_CP_INTERNAL_LISTEN` and `AF_CP_INTERNAL_URL` (`control-plane/main.go`, `workspace_listener.go`) and `deploy/compose/.env.example`"
---

# When workspaces cannot reach the CP

English | [日本語](workspace-listener.ja.md)

When the host reaches the Control Plane but workspaces alone cannot reach `PUBLIC_BASE_URL`, give workspaces a way to it that does not leave the host.
This is for Docker Compose deployments on rootful Docker on Linux.

## How it works

| What you do | What happens |
|---|---|
| Look up the docker0 address and write two variables into `.env` | The Control Plane opens a second, workspace-only port on that address |
| Bring the Control Plane up again with `docker compose up -d` | The Control Plane starts with the new setting |
| Stop and start each workspace | The workspace's Agent calls the Control Plane on this port instead of the public URL |

## The symptom

The Agent in each workspace calls the Control Plane back for many things: the docs pull, branch rules, the tenant MCP registry, AWS and Google Cloud profiles, memos, schedules, git over the Control Plane.

By default it calls `PUBLIC_BASE_URL`, so it goes out through the Docker bridge's NAT and back in through whatever serves the public URL.

- When that front end (a tunnel daemon, a reverse proxy) stops accepting connections from the Docker bridges, all of those calls fail **at once**
- The Control Plane itself is healthy, and the host and browsers reach it as usual
- One case: a tunnel daemon's `:443` socket became bound to one host interface, and containers alone got an immediate RST

## Set it up

1. Look up the docker0 gateway address (normally `172.17.0.1`)
2. Write the two variables into `.env`
3. Bring the Control Plane up again with `docker compose up -d`. It reads both from `.env`
4. Stop and start each workspace in the Console. The setting reaches a workspace at its next start

```bash
# the docker0 gateway address (normally 172.17.0.1):
docker network inspect bridge -f '{{(index .IPAM.Config 0).Gateway}}'
# .env
AF_CP_INTERNAL_LISTEN=172.17.0.1:8098
AF_CP_INTERNAL_URL=http://host.docker.internal:8098
```

| Variable | What to put in it |
|---|---|
| `AF_CP_INTERNAL_LISTEN` | Where the Control Plane opens the second port: the docker0 address and a port |
| `AF_CP_INTERNAL_URL` | The URL workspaces call the Control Plane at |

> [!WARNING]
> **Bind the docker0 address, never `0.0.0.0` or `:8098`.**
> The docker0 address is local to the host, so every per-user network reaches it (the packet is delivered to the host, not forwarded between bridges), while no LAN or tailnet interface carries it.
> A wildcard bind puts the listener on the LAN too; the Control Plane then logs a `WARNING` at start.

## What the listener serves

- Only the routes the Agent calls, each behind its own per-membership bearer token
- The Console, the admin API, sign-in and the egress proxy's routes answer 404
- It drops the identity header and every forwarding header
- Links a person opens, and `af-curl` / `af-shot`, stay on `PUBLIC_BASE_URL`

## What to line up on the host

| Item | What to do |
|---|---|
| `host.docker.internal` | When `AF_CP_INTERNAL_URL` names it, the Control Plane starts each workspace with `--add-host host.docker.internal:host-gateway` (Docker Engine 20.10 or later). `host-gateway` is docker0's address; if the daemon sets `host-gateway-ip`, bind that address instead. Any other host in the URL is left to DNS |
| The host firewall | If the INPUT policy drops (ufw's default), admit TCP to that address and port from the Docker networks |
| The egress proxy | With `AF_EGRESS_PROXY_ADDR` set, the host of `AF_CP_INTERNAL_URL` is added to the workspaces' `NO_PROXY`. It is on the built-in egress allowlist too |
| Reach from the LAN | Linux accepts a packet for that address on any interface, so a LAN machine that deliberately routes the docker0 range through this host could reach it. A firewall that admits the port only from the Docker bridges closes that too |

- It is off by default. The address depends on the host's Docker configuration and firewall, and an address that does not exist stops the Control Plane. Unset, there is no second port and nothing changes
- If docker0 does not exist when the Control Plane starts, the bind fails and the Control Plane exits

## Where this setup does not apply

| Environment | Why |
|---|---|
| Docker Desktop | `host-gateway` maps to the desktop machine, not to the engine where the Control Plane listens |
| Rootless Docker | Unverified. docker0 lives in RootlessKit's namespace, while from Docker Engine 29.5 `network_mode: host` is the real host's, where that address does not exist and the Control Plane fails to bind |

The Mac overlay (`docker-compose.macos.yml`) uses this mechanism from the start ([Run on an Apple Silicon Mac](macos.md)).

## When it goes wrong

| Symptom | Where to look |
|---|---|
| The Control Plane exits after you set it | Does the `AF_CP_INTERNAL_LISTEN` address exist on the host? Is docker0 missing, or does `host-gateway-ip` make it another address? |
| The log says `AF_CP_INTERNAL_URL is set without AF_CP_INTERNAL_LISTEN` | `AF_CP_INTERNAL_LISTEN` is missing. Workspaces are sent to an address this Control Plane does not serve |
| It still does not reach after you set it | Did you stop and start that workspace? Does the host firewall admit the port? |

## Related

- [Deploy with Docker Compose](install.md)
- [Find the cause from the symptom](../diagnose/checklist.md)
- [Control outbound traffic](../secure/egress.md)
