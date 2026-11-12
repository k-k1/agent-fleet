---
audience: "whoever installs, upgrades and protects a deployment — IT / SRE with shell access to the host and deployment-administrator rights"
source_of_truth: "the runbooks under `deploy/` for commands; this shelf for what each step decides"
---

# Operating a deployment

English | [日本語](README.ja.md)

What people running Agent Fleet on their own infrastructure want to do, one task per page. The old chapters are being rewritten into these pages one by one.
Anything not here yet is in "Chapters not yet moved" below. The commands are in each target's runbook; these pages say what they decide.

## Getting started

| Page | What you can do |
|---|---|
| [Self-hosting at a glance](overview.md) | Learn what it does, what you need, and what the operator is responsible for. Read it before deciding to adopt |
| [Choose where to deploy](choose.md) | Choose between compose, native, AWS and Kubernetes, and compare AWS cost and egress routes |

## Install

| Page | What you can do |
|---|---|
| [Install Agent Fleet](install.md) | Generate the secrets, configure sign-in, start it, sign in as the first administrator and welcome members |
| [Choose the tenant and provisioning model](install-decisions.md) | Decide how the certificate is obtained, invitation or automatic admission, how tenants are split, and each tenant's sign-in |

## Day-to-day operations

| Page | What you can do |
|---|---|
| [Back up and restore](run/backup-restore.md) | Run `backup.sh` on a schedule and restore on another host |
| [Upgrade](run/upgrade.md) | Move to a new release and move workspaces onto the new image |
| [Install into a closed network](run/air-gapped.md) | Provide images, TLS, CLIs and diagram icons on a host that cannot reach the outside |
| [Give every agent instructions](run/fleet-policy.md) | Have every agent read operating rules through the fleet policy |
| [Run opencode's shared server](run/opencode-server.md) | Manage the server behind Terminal sessions and the opencode release |
| [Expose it to the internet](run/public-internet.md) | Know what gets through when probes arrive, and narrow the entry |
| [Tell deployments apart](run/deployment-label.md) | Give each deployment a colour and a label |
| [Use idle stop and force-stop](run/idle-stop.md) | Set the auto-stop times, stop workspaces that will not stop, pause the deployment |

## Sign-in

| Page | What you can do |
|---|---|
| [Set up how people sign in](signin/overview.md) | Learn what every IdP has in common, the redirect URI and where each setting lives, and check that it worked |
| [Sign in with Google](signin/google.md) | Create a client in Google Cloud Console and put it in `.env` |
| [Sign in with Microsoft Entra ID](signin/entra.md) | Register an app in Entra and pin the issuer to your directory |
| [Sign in with GitHub](signin/github.md) | Let in only members of your GitHub organisations |
| [Sign in with another OIDC IdP](signin/oidc.md) | Connect Okta, Keycloak, Auth0, Cognito, GitLab and others |
| [Approve a tenant's own sign-in](signin/tenant-idp.md) | Check, approve and stop an IdP a tenant registered |

## When something goes wrong

| Page | What you can do |
|---|---|
| [Find the cause from the symptom](diagnose/checklist.md) | Read the log and health, and narrow the cause down from the symptom table: the three constraints of driving Docker from a container, TLS, the FAQ |
| [Cannot log in](diagnose/cannot-login.md) | Find out why sign-in is refused, a button is missing, or a removed person still gets in |

## Security

| Page | What you can do |
|---|---|
| [Understand the threat model and residual risks](secure/threat-model.md) | Learn where the isolation boundary is, prepare for the remaining risks, report a vulnerability |
| [Handle the deployment keys](secure/deployment-keys.md) | Meet what `AF_MASTER_KEY` and `AF_COOKIE_SECRET` must be, add a key to a deployment without one, seal old values |
| [Control outbound traffic](secure/egress.md) | Observe workspaces' outbound traffic and narrow it with an allowlist |
| [Operate MCP and external-connection sign-in](secure/mcp.md) | Open `/mcp` with OAuth, decide which client hosts to trust, read what arrived in the log |
| [Use development sign-in](secure/dev-signin.md) | Know where `AUTH=dev` may be used, turn the development OIDC IdP on or off |
| [Other operational controls](secure/controls.md) | Keep the preview domain, the sign-in allowlist, the audit log and the host's cloud identity right |
| [Revoke access and hand over super_admin](secure/offboarding.md) | Remove people, end everybody's sessions, change the deployment administrator |

## Keys

| Page | What you can do |
|---|---|
| [Put the keys at rest on AWS KMS](keys/kms.md) | On AWS, protect what the Control Plane seals with KMS and re-seal values stored before the switch |
| [Give each home its own key](keys/home-keys.md) | On AWS, seal each member's credential store under a key of its home's own |
| [Rotate the bridge tokens](keys/bridge-tokens.md) | Invalidate workspace tokens for one member or for everyone at once |

## ECS

| Page | What you can do |
|---|---|
| [Replace and stop slots](aws-ecs/slot-maintenance.md) | On `ecs-ec2`, replace outdated slots and notice and stop a slot that does not answer |

## Inference engines

| Page | What you can do |
|---|---|
| [Run inference engines on your own GPUs](engines/gpu.md) | choose each role's mode, set the idle stop and the GPU instance class, and the words kept out of every picture |
| [Take models into the inference engines](engines/models.md) | find models on Hugging Face and Civitai, take them in and enable them; let a tenant take models in |
| [Check the inference engines' state and history](engines/status.md) | read the running instance and the requests, and look at 14 days of uptime |
| [Run the read-aloud engine](engines/read-aloud.md) | provide and run the VOICEVOX engine, and keep the shared pronunciation dictionary |

## Chapters not yet moved

- [08 Image generation on your own ComfyUI](08-image-engine.md): pointing sessions at a ComfyUI on your own network
- [09 Borrowing another deployment's engines](09-borrowed-engine.md): using an AWS fleet's llm and image engines from a deployment on your own host
- [10 Chat inference on your own llama.cpp](10-llm-lan.md): pointing sessions' chat at a llama.cpp on your own network

The content of these chapters has moved to the pages above: [01 Choosing a deployment target](01-choose.md), [02 Initial setup](02-install.md), [03 Running it](03-run.md), [04 Securing it](04-secure.md), [05 Sign-in methods](05-signin.md), [06 Diagnosing it](06-diagnose.md), [11 The ecs-ec2 slot pool](11-slot-pool.md), [12 Keys at rest](12-keys.md), [07 Inference engines and read aloud](07-engines.md)

What each target supports is in [Deployment targets](../ref/deploy-targets.md); who may do what is in [Roles](../ref/roles.md).
