---
audience: "a deployment administrator who already runs llama.cpp somewhere on the network (or is about to) and wants sessions' chat completions to use it"
source_of_truth: "the environment variables the Control Plane reads at startup; llama-server's own `--help` for its startup flags"
updated: "2026-09"
---

# 09. Chat inference on your own llama.cpp

English | [日本語](09-llm-lan.ja.md)

The fleet's own GPU engine behind chat completions — the `llm` role — is bought on
AWS, so it exists on the `ecs-ec2` target only. If you already run an Agent Fleet on
AWS, [08 Borrowing another deployment's engines](08-borrowed-engine.md) lets you use
that fleet's `llm` role instead.

This chapter is for neither of those. It points a `compose` / `native` / `docker`
deployment at **a llama.cpp that is already running on your own network** (or that
you are about to start). It is the chat counterpart of what
[07 Image generation on your own ComfyUI](07-image-engine.md) does for images — the
same shape, pointed at a GPU machine under someone's desk, the Windows side of a WSL2
machine, or a shared server.

It is an environment variable and a model list you keep yourself. **The engine stays
yours**: the Control Plane never starts it, never stops it, and never bills for it.

## Two paths; the code change was the shortcut

Turning the `llm` role into an externally managed row already had two paths.

| Path | What it is | Variables needed |
|---|---|---|
| **A. Shortcut** | Set `AF_LLM_URL`. The CP synthesises one `llm` row at startup. | `AF_LLM_URL`, optionally `AF_ENGINE_API_KEY_LLM` |
| **B. Write the table** | Declare a `lifecycle: "external"` row yourself, in `AF_ENGINES_JSON` (or, on AWS, the engine stack's table). | The same `AF_ENGINE_API_KEY_LLM` |

**A is the same shape of shortcut `AF_COMFY_URL` already is for 07, and B already
works today with no code at all.** This chapter mostly covers A, since it is the one
that is a single line.

Both are read by the Control Plane **once at startup**. Changing either one is a CP
restart.

| Variable | Meaning |
|---|---|
| `AF_LLM_URL` | Full URL including the port, as the CP sees it — e.g. `http://192.168.1.20:8080`. Setting it is what turns path A on. |
| `AF_ENGINE_API_KEY_LLM` | Optional. Sent upstream as `Authorization: Bearer`. **There is no dedicated `AF_LLM_API_KEY`** — see below. |

Where to put them depends on the target:

| Target | Where |
|---|---|
| compose / docker | the `.env` next to the compose file (the CP service reads the whole file) |
| native | the environment of `af start`, or `Environment=` in the systemd unit — [deploy/native/README.md](../../deploy/native/README.md) |

### Why not `AF_LLM_API_KEY`

07 gave the image role a variable of its own, `AF_COMFY_API_KEY`. This chapter does
not repeat that shape, because **`AF_ENGINE_API_KEY_LLM` is already the bearer every
external row reads** — a hand-written `llm` row (path B) as much as any other
externally managed row. A second, dedicated name would only add a reconciliation
between the two — "which one wins" — and produce a 401 for whichever one an operator
just edited. **The key lives in one variable.**

## Running llama-server on the LAN box

**This runs on your other host, not inside the Workspace.** What follows is a command
to paste and run there; this deployment never executes it.

Getting it (easiest first):

| | Linux | Windows |
|---|---|---|
| Container | `docker run ghcr.io/ggml-org/llama.cpp:server-cuda` (**the same image this deployment itself runs**) | — |
| Package manager | `winget install llama.cpp` | same |
| Release archive | `llama-bNNNNN-bin-ubuntu-cuda-*.tar.gz` (**not `.zip`**) | `llama-bNNNNN-bin-win-cuda-*.zip` **+ `cudart-*.zip` (the CUDA runtime ships separately)** |

🔴 **A CUDA-enabled Linux archive only exists on newer builds.** It is absent from
`b10830`'s asset list, the build this deployment runs (vulkan/rocm/sycl only, back
then), and appears from `b11065` on as `ubuntu-cuda-12.8`/`ubuntu-cuda-13.3`. Naming
an older build number will not find one — on an NVIDIA GPU, **the container is the
safer bet**.

The shape of the command:

```
llama-server -hf <repo>:<quant> --alias <catalogue id> -c <window> -ngl 99 --jinja \
             --host 0.0.0.0 --port 8080 --api-key <key>
```

- 🔴 **`--alias` is close to mandatory.** This deployment sends the catalogue's **id**
  as `model` on every request, so the request never arrives unless the alias matches
  it (without `--alias`, a single-model server's `/v1/models` reports whatever path
  was passed to `-hf`/`-m` as its id).
- `-c` (the window) has to be at least the `context_tokens` you register for this row
  in the catalogue — 8192 or more is a reasonable floor.
- Without `--host 0.0.0.0` the server only answers on loopback. **Give `--port`
  explicitly too** — upstream has announced it plans to move the default.
- 🔴 **Without `--api-key` the server opens on your LAN with no authentication at
  all.**
- `--jinja` is on by default upstream, but stating it explicitly is cheap insurance
  against an older build.
- A gated Hugging Face repository needs `HF_TOKEN=...` (or `-hft <token>`).
- Optionally, `--sleep-idle-seconds <N>` lets the box release VRAM when idle — a
  partial escape from the "always on" this deployment's own `llm` role otherwise
  assumes.

Families confirmed working here: **Qwen3, GPT-OSS, and Gemma**. **`llama-3.1` does
not carry tool calls through this engine version** — this harness's tool loop is what
coding work depends on, so keep `llama-3.1` to tool-free uses if you pick it.

## Switch first, then register the models

🔴 **While this deployment is still borrowing an AWS fleet's engines (08), you cannot
edit the `llm` model list.** A write to a borrowed row is refused, naming the far
deployment. The order matters:

1. Set `AF_LLM_URL` (and `AF_ENGINE_API_KEY_LLM` if you set one on the server), and
   restart the CP.
2. Only then register models for `llm` under **Admin → Inference engines** in the
   Console — using the same **id** you gave `--alias`.

Registering a model is the same shape as 07's "Registering the models by hand".
Nothing is taken in for you here either. **The catalogue is a declaration you write**
— id, `base_model`, and the window (`context_tokens`), kept consistent with what you
started the LAN box with.

## What you lose (said plainly)

The row you were borrowing under 08 carried authentication the CP could revoke and
track per membership — the far deployment issues a token per membership and cutting
it stops that one borrower. Switching to `AF_LLM_URL` (or a hand-written external
row) trades that for **a single shared bearer**: everyone who knows
`AF_ENGINE_API_KEY_LLM` uses the same key, and there is no way to cut off one of them
without rotating it for everyone.

**An externally managed row has no mechanism to wake anything.** The "slow but it
connects" of a borrowed cold start becomes "fast, or it fails immediately" — see the
next section.

## What stays the same

**Usage still goes through the CP's gateway.** The `usage` (token counts) a chat
completion carries is injected by the gateway for an externally managed row exactly
as it is for a managed one, so members' usage ledgers are written the same way they
always were — streaming included.

## What it looks like when it is down

The Control Plane does not wait for an engine it does not own. A request made while
the LAN box is down is refused **immediately** with `503 engine_unavailable`, and the
message names the URL that was tried and the health path (`/health`). Nothing retries
for minutes — that budget exists for a cloud instance being bought.

## What the panel will and will not show you

Same as the image engine in 07.

- **warm** is a liveness check taken the moment you open the panel, read from
  `/models` (not `/health` — a llama.cpp router answers that with `ok` even while
  holding no weights at all, so it is not what warm reads).
- **There is no uptime history.** An externally managed row has no control loop.
- **No cost is attributed.** A machine on your own network has no hourly rate the
  deployment could know.

Which target supports which of these is in
[ref/deploy-targets.md](../ref/deploy-targets.md).
