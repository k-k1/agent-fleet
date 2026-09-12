---
audience: "a deployment administrator who already runs a ComfyUI somewhere on the network and wants sessions to use it"
source_of_truth: "the environment variables the Control Plane reads at startup; the runbook next to your target (`deploy/native/README.md`, `deploy/compose/.env.example`) for the exact syntax"
updated: "2026-09"
---

# 07. Image generation on your own ComfyUI

English | [日本語](07-image-engine.ja.md)

A session can generate an image from a prompt. Out of the box that runs on **the
member's own CLI plan** — the ChatGPT login behind Codex, the Gemini/Antigravity
login behind agy — and the fleet's own GPU engines are bought on AWS, so they exist
on the `ecs-ec2` target only.

This chapter is the third possibility: **a ComfyUI that is already running on your
own network**, pointed at from a `compose`, `native` or `docker` deployment. A GPU
box under someone's desk, the Windows side of a WSL2 machine, a shared server. The
Control Plane relays to it, so no session has to know where it is or hold a
credential for it.

It is two environment variables and a manually-kept model list. **The engine stays
yours**: the Control Plane never starts it, never stops it, and never bills for it.

There is a fourth possibility, and it is a different chapter: if you already run an
Agent Fleet on AWS, this deployment can **borrow that one's engines** —
[08 Borrowing another deployment's engines](08-borrowed-engine.md). That covers the
chat engine as well as the image one, and the far fleet does the waking and the
paying.

## The two variables

Both are read by the Control Plane **once at startup**. Changing either one is a CP
restart — there is no field for them in the Console today.

| Variable | Meaning |
|---|---|
| `AF_COMFY_URL` | Full URL including the port, as the CP sees it — e.g. `http://192.168.1.20:8188`. Setting it is what turns the route on. |
| `AF_COMFY_API_KEY` | Optional. Sent upstream as `Authorization: Bearer`, on generation calls **and on the health check**. |

Where to put them depends on the target:

| Target | Where |
|---|---|
| compose / docker | the `.env` next to the compose file (the CP service reads the whole file) |
| native | the environment of `af start`, or `Environment=` in the systemd unit — [deploy/native/README.md](../../deploy/native/README.md) |

The server build of ComfyUI answers on loopback only until it is started with
`--listen`, and the host firewall has to allow the port inbound from the machine
running the CP. **ComfyUI Desktop on Windows is untested**: the setting that
corresponds to `--listen` there has not been confirmed, so the procedure above is
written for the server build.

## What the modes mean here

In the Console under **Admin → Inference engines** an engine like this shows as
**externally managed**, and it takes only two settings: **on** and **off**. There is
no "on demand" — that setting buys and releases a cloud instance, and there is
nothing here to buy.

🔴 **"off" closes the route, it does not stop ComfyUI.** Sessions stop being offered
the engine and requests are refused, and the machine on your network goes on doing
exactly what it was doing. It is the same meaning "off" has for the speech engine
when that is externally managed. Stopping the process is yours to do, on the host it
runs on.

The fields that describe a cloud instance — state, desired count, which box, when it
will stop — are **left out** rather than guessed at, because the deployment does not
know them.

## Registering the models by hand

Nothing is taken in for you here: there is no ingest job, no bucket, and the CP
never reads the model directory. **The catalogue is a declaration you write.** For
each model, register in the Console under **Admin → Inference engines**:

- the **id** you want members to see,
- its **`base_model`** — this is not a display name, it is what decides which
  workflow is used, so a misspelling only surfaces when someone tries to generate,
- the **file names**, spelled exactly as ComfyUI's loaders list them.

A row missing a file for one of the roles it needs is marked **files missing**. That
check reads your declaration only — it is not evidence that the file is on the disk.
If you declare a name that is not there, the failure arrives at generation time.

🔴 **A checkpoint published without a VAE needs one declared next to it.** Plenty
of SDXL-family checkpoints on the model sites ship the UNet and the text encoders
alone. A row naming that file alone passes every check there is, switches the
engine's checkpoint (1–2.5 minutes) and then fails **every** request with `VAE is
invalid: None` — in the decode for a plain generate, in the encode for an edit.
There is nothing a member can do about it from their side: the tool has no VAE
argument. Declare the family's standalone VAE as a **second file on the same row,
with the flag `--vae`** (an SDXL-family checkpoint takes an `sdxl_vae`); that file
is then what the workflow encodes and decodes with, in place of the checkpoint's
own. Until you do, **disable the row** — it is offered to every session, and each
attempt costs a checkpoint switch before it fails.

🔴 **Put the files directly under ComfyUI's type folders** — `checkpoints/`,
`diffusion_models/`, `clip/`, `vae/`, `loras/`. Only the part after the last `/` is
handed to the loader, so a file in a subfolder (`checkpoints/sdxl/x.safetensors`)
arrives as `x.safetensors` and fails with `Value not in list`. Subfolders are not
supported yet.

## What a picture keeps out

Three places say what should NOT be drawn, and they are added together rather than overriding
one another:

- **the model row's own** — what this checkpoint's publisher recommends keeping out. Register it
  in the same panel, on the row.
- **the member's**, per request (`negative_prompt` on the image tool).
- **yours, for the whole engine** — the box labelled "excluded from every image". Applied to
  every request, whoever made it and whichever checkpoint answers.

When all three are empty, a fixed default is used (`blurry, lowres, deformed, watermark, text`).
A row that declares its own replaces that default rather than being added to it.

🔴 **This is a negative prompt, not a content filter.** The words are handed to the sampler as
something to steer away from. They are not a gate, a determined prompt outweighs them, and — most
importantly — **three of the five checkpoint families ignore them completely**: Z-Image and
FLUX.2 klein sample at cfg 1, where the negative branch cancels out exactly, and FLUX.1 has no
negative input at all. Only SDXL and SD3.5 are steered by what you type. A request answered by one of them **says so in its warnings**,
naming the family. If a deployment needs a guarantee about what can be produced, this is not
where it lives.

## The network is yours to close

On AWS the GPU engine is reachable from the Control Plane and nothing else, and a
security group is what guarantees it. **On your own network there is no equivalent,
and the deployment does not pretend there is.**

Specifically: a workspace container reaches the rest of the network through NAT, so
**a session can talk to ComfyUI directly**, bypassing the Control Plane. ComfyUI has
no authentication of its own and its API includes calls that change things — queue a
prompt, upload an image. Treat "only the CP can reach it" as false unless you have
made it true.

Two ways to make it true:

- **Egress control in enforce mode** ([04 Securing it](04-secure.md)): a private
  address that is not on the allowlist is refused. Note the caveat on that page —
  observation and allowlist management work today, enforcement itself is follow-up
  work — so this is the direction, not yet the answer.
- **A reverse proxy in front of ComfyUI** that requires a bearer token, with that
  token given to the Control Plane alone through `AF_COMFY_API_KEY`. This is the one
  that works today. **The proxy must also let `/system_stats` through with the same
  bearer** — that is the health check, and an engine that never looks healthy is an
  engine that never answers.

Failing both, the honest position is that anyone with a session on this deployment
can use that GPU, and you are relying on trusting them.

## When it is down

The Control Plane does not wait for an engine it does not own. A request made while
ComfyUI is down is refused **immediately** with `503 engine_unavailable`, and the
message names the URL that was tried and the health path. Nothing retries for
minutes in the hope that a box is still booting — that budget exists for a cloud
instance being bought, and a machine on your network that is switched off does not
come back because someone waited.

So the two things to check, in order, are whether ComfyUI is running on that host
and whether the CP can reach it on that address and port.

## What the panel will and will not show you

- **warm** is a liveness check taken **the moment you open the panel** — one request,
  a two-second limit, cached for ten seconds. It means "it answered just now", not
  "it has been up".
- **There is no uptime history.** The heatmap is drawn from the samples a control
  loop takes, and an externally managed engine has no control loop.
- **No cost is attributed.** A machine on your own network has no hourly rate the
  deployment could know, so nothing appears under cloud cost. What it costs you in
  electricity is outside the deployment entirely.

Which target supports which of these is in
[ref/deploy-targets.md](../ref/deploy-targets.md).
