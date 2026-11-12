---
audience: "whoever installs, upgrades and protects a deployment — IT / SRE with shell access to the host and deployment-administrator rights"
source_of_truth: "the runbooks under `deploy/` for commands; this shelf for what each step decides"
---

# Operating a deployment

English | [日本語](README.ja.md)

What people running Agent Fleet on their own infrastructure want to do, one task per page.

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
| [Understand the ECS layout](aws-ecs/overview.md) | Learn which stack builds what, and in which order to build them |
| [Build on ECS](aws-ecs/install.md) | Prepare the account, deploy the stacks, push the images and get to the first sign-in |
| [Turn on optional features with stack parameters](aws-ecs/options.md) | Turn on preview subdomains, KMS, remote MCP, provided routes, timers and outside engines, and WAF |
| [Set up the alarm for an unreachable database](aws-ecs/alarms.md) | Get an email when the Control Plane can no longer reach its database |
| [Upgrade on ECS](aws-ecs/upgrade.md) | Move to a new release with `update.sh`, roll back, and clear a parameter |
| [Keep templates under the size limit](aws-ecs/template-size.md) | Deploy `30-ingress`, which is over 51,200 bytes, through S3 |
| [IAM permissions for deploying](aws-ecs/iam.md) | Give the people or CI who deploy the least permissions, service by service |
| [Set the default workspace size](aws-ecs/workspace-size.md) | Set the CPU, memory and working disk every workspace gets, and when caches move off EFS |
| [Turn on timed wakes](aws-ecs/schedules.md) | Switch the scheduler off, or give a slow-booting deployment more time to wake a workspace |
| [Pin the outbound IP address](aws-ecs/egress-ip.md) | Find the address to allow-list and keep it across a rebuild |
| [Control outbound traffic with a gateway](aws-ecs/egress-gateway.md) | Route workspace egress through the egress gateway, register its address, pause it and go back |
| [Add Japanese read-aloud](aws-ecs/tts.md) | Build the VOICEVOX engine stack and tune its automatic start and stop |
| [Add inference engines](aws-ecs/engines.md) | Build the GPU engine stack, stage models into S3 by hand and tune the controller |
| [Keep costs down](aws-ecs/cost.md) | Know the standing cost, pick the levers, and move a running stack onto the S3 gateway endpoint |
| [Back up and restore EFS](aws-ecs/backup.md) | Turn on daily EFS backups and restore one member or the whole file system |
| [Stand up, pause and tear down with the scripts](aws-ecs/lifecycle.md) | Record a deployment, pause and resume it, and know what each script does in which order |
| [Tear a deployment down](aws-ecs/teardown.md) | Delete everything in an order that does not stall, and know what survives |
| [Replace and stop slots](aws-ecs/slot-maintenance.md) | On `ecs-ec2`, replace outdated slots and notice and stop a slot that does not answer |
| [Troubleshoot ECS](aws-ecs/troubleshooting.md) | Find build, upgrade and start problems from the symptom |

## Inference engines

| Page | What you can do |
|---|---|
| [Run inference engines on your own GPUs](engines/gpu.md) | choose each role's mode, set the idle stop and the GPU instance class, and the words kept out of every picture |
| [Take models into the inference engines](engines/models.md) | find models on Hugging Face and Civitai, take them in and enable them; let a tenant take models in |
| [Check the inference engines' state and history](engines/status.md) | read the running instance and the requests, and look at 14 days of uptime |
| [Run the read-aloud engine](engines/read-aloud.md) | provide and run the VOICEVOX engine, and keep the shared pronunciation dictionary |
| [Generate images on your own ComfyUI](engines/comfyui.md) | Point the image engine at a ComfyUI on your network, register its models and close the routes to it |
| [Borrow another deployment's engines](engines/borrow.md) | Use the chat and image engines of an Agent Fleet on AWS from a deployment on your own host, and issue the token as the lender |
| [Chat on a llama.cpp on your LAN](engines/llama-cpp.md) | Point the chat engine at a llama-server on your network |

## Docker Compose

| Page | What you can do |
|---|---|
| [Deploy with Docker Compose](compose/install.md) | Write `.env`, start from source or from the published release images, and sign in as the first administrator |
| [Run on an Apple Silicon Mac](compose/macos.md) | Run it for one person on a Mac's Docker engine or in a Linux VM |
| [When workspaces cannot reach the CP](compose/workspace-listener.md) | Give workspaces a way to the CP that does not leave the host, through the workspace listener |

## Native package

| Page | What you can do |
|---|---|
| [Install the native package](native/install.md) | Install it on a single-user Linux host or WSL2 without Docker, and start with `af start` |
| [Update the native package](native/update.md) | Stage a new version and switch with a restart, pin a version, carry it into a closed network |
| [Run it as a systemd service](native/service.md) | Make `af start` a user service that keeps running after you close the terminal |
| [Reset or uninstall](native/uninstall.md) | Wipe the data and start over, or remove it from the host |

## Other setups

| Page | What you can do |
|---|---|
| [Run it for yourself on WSL](wsl.md) | Try it alone, with no sign-in, on Docker inside WSL2 |
| [Deploy on a single EC2 instance](aws-ec2-single.md) | Stand up one EC2 instance with CloudFormation and run the Docker Compose deployment from the release bundle |

## Kubernetes

| Page | What you can do |
|---|---|
| [The Kubernetes deployment at a glance](kubernetes/overview.md) | Learn what gets built where, the preconditions the cluster must meet, and the GKE disk quota |
| [Build on GKE](kubernetes/gke.md) | Stand it up with Terraform and the manifests, from the project to checking the load balancer |
| [Build on another cluster](kubernetes/other-clusters.md) | Stand it up on a cluster other than GKE from the generic overlay |
| [Upgrade the CP](kubernetes/upgrade.md) | Back up, move to a new version, and go back if you must |
| [Back up and restore](kubernetes/backup.md) | Schedule snapshots of the CP's disk and rehearse restoring the database and the disk |
| [Set up alerts](kubernetes/alerts.md) | Watch the CP, the certificate, pending workspaces and node disk in Cloud Monitoring |
| [Replace nodes and recover from a stopped node](kubernetes/nodes.md) | Replace nodes without cutting sessions, recover from a node that stopped answering, clean up what Destroy left |
| [Pause and see the bill](kubernetes/pause.md) | Put the deployment to sleep with `pause.sh`, and know what it costs |
| [Tear down](kubernetes/teardown.md) | Remove the deployment and the leftovers |

What each target supports is in [Deployment targets](../ref/deploy-targets.md); who may do what is in [Roles](../ref/roles.md).
