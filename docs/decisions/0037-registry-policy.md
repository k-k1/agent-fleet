# 0037. Registry policy — distribute images via GHCR, and retire the air-gap image tar (B)

English | [日本語](0037-registry-policy.ja.md)

- Status: **adopted** (2026-08-02). This settles docs/35 §35.9's open item, "decide the registry
  policy".
- See also: [docs/35 §35.4](../log/35-packaging.md) (distribution channels and air-gap) / §35.7.7 (the bake-in decision) /
  `deploy/compose/release.sh` and `deploy/release/dist-repo/install-compose.sh` (the implementation)

## Context

Artifact B (`agent-fleet-images-<v>.tar.gz`, about 960MB) is `docker save` of the CP and workspace
images, distributed as "air-gap images". The default was a tar rather than a registry because the
original audience was group companies where a container registry could not be assumed. docs/35 §35.9
left this as "the registry policy is undecided".

## The question that prompted the decision

**"What is the point of B, which works with no network? Doesn't an agent need a network in the
first place?"** — the question is right. And this repository already agrees with it in substance.
§35.4.1 decided that because claude, agy and copilot cannot be redistributed, **both C and B are
lean (without the CLIs)**, and states explicitly that **a company that is fully offline and needs the
CLIs is directed to building its own** (`BAKE_AGENT_CLIS=1`).

In other words, **B was never "a fleet that runs offline".** It fails twice over:

1. An agent does not work unless it can reach an LLM endpoint.
2. Before that, **B's own bootstrap needs npm and GitHub** (being lean, the boot-install on first
   start goes and fetches the CLIs). And unlike point 1, this **cannot be solved by baking them in,
   for licensing reasons**.

## Decision

**Distribute images via GHCR (`ghcr.io`). Retire B.**

The reasons:

- **B's only real benefit was "no container registry required".** The public README says exactly
  that ("The images are **not** published to a registry, so ... ship on Releases"). The only thing
  that overclaimed was the `(air-gap images)` label in the artifact table, and since that conflicts
  with reality it has to be corrected in any case.
- **Going public changed the premise.** B's design predates the decision to go public. For a public
  repository **GHCR is free in both storage and transfer**, and moreover **anyone can build from
  source**. The hole B was filling got considerably smaller by publishing.
- **The cost does not add up.** About 960MB per release accounts for most of the publish time and
  storage, while actual downloads number about one.
- **B is the only artifact that bakes chromium at an exact apt version pin**, and it is **the very
  reason 0.4.0 could not be rebuilt** (the native rootfs uses `BAKE_OPTIONAL_TOOLS=0`). Without B,
  0.4.0's `build.sh --all` would have passed.

## Options considered and not taken

- **Keep B but drop chromium from it**: rebuildability and size improve, but it **weakens the
  browser sandbox**. As §35.4.1 says, under docker the SUID `chrome-sandbox` (4755) works properly,
  whereas a version downloaded into home cannot hold SUID. That is a trade rather than a plain
  improvement, so it is not taken. With GHCR distribution **the same image ships as is**, so the
  sacrifice does not arise.
- **Keep B and just fix the label**: the conflict with reality goes away, but the 960MB and the open
  question remain.
- **Stop baking things in for the sake of rebuilding past versions**: that is the wrong lever. If you
  want reproducibility, **pin where things are fetched from** (snapshot.debian.org for apt), not stop
  baking (§35.7.7). Held as a point independent of whether B survives.

## Consequences

- **The answer for a genuinely offline user does not change** — build your own, as §35.4.1 already
  set out. Even with B they had to bake the CLIs themselves, so no capability is lost.
- **For users who cannot reach a registry**, the self-help procedure of making an image tar locally
  with `deploy/compose/release.sh --save` remains (it is just not shipped as an artifact).
- The installation procedure for compose users changes (`docker load` → `docker compose pull`), so it
  is stated in the release notes as a **user-visible change**.
- The ECS route (`aws/ecs/release-ecr.sh` pushing to ECR) is unrelated and unchanged.
