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

## Chapters not yet moved

- [03 Running it](03-run.md): backup, restore, upgrades, air-gapped networks, idle stop and pausing
- [04 Securing it](04-secure.md): threat model, the remaining risks, egress control, MCP sign-in and its trusted hosts, the development sign-in, offboarding, reporting channel
- [05 Sign-in methods](05-signin.md): what to create at Google, Entra ID, GitHub or another OIDC provider, which value goes where, and how to check it
- [06 Diagnosing it](06-diagnose.md): incident response and FAQ, including the three constraints that break silently
- [07 Inference engines and read aloud](07-engines.md): the Admin panel for the deployment's own GPU engines, the speech engine and the shared dictionary
- [08 Image generation on your own ComfyUI](08-image-engine.md): pointing sessions at a ComfyUI on your own network
- [09 Borrowing another deployment's engines](09-borrowed-engine.md): using an AWS fleet's llm and image engines from a deployment on your own host
- [10 Chat inference on your own llama.cpp](10-llm-lan.md): pointing sessions' chat at a llama.cpp on your own network
- [11 The ecs-ec2 slot pool](11-slot-pool.md): replacing slots, memory protection for the management plane, stopping a wedged slot
- [12 Keys at rest](12-keys.md): AWS KMS custody, re-sealing earlier values, a key of its own for each home

The content of these chapters has moved to the pages above: [01 Choosing a deployment target](01-choose.md), [02 Initial setup](02-install.md)

What each target supports is in [Deployment targets](../ref/deploy-targets.md); who may do what is in [Roles](../ref/roles.md).
