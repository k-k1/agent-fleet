---
audience: "Deployment administrators who want sessions on a compose / native / docker deployment to chat through a llama.cpp on their own network"
source_of_truth: "The environment the Control Plane reads at start. llama-server's own arguments: upstream `--help`"
---

# Chat on a llama.cpp on your LAN

English | [日本語](llama-cpp.ja.md)

Let a `compose` / `native` / `docker` deployment use a llama.cpp (llama-server) on your own network as its chat engine (`llm`).
It works like the image ComfyUI: sessions reach it through the Control Plane.

## How it works

| What you do | What happens |
|---|---|
| Start llama-server on a machine with a GPU | An OpenAI-compatible API listens on a port |
| Set `AF_LLM_URL` (and the key) and restart the Control Plane | The Control Plane makes one external `llm` row and relays the sessions' chat to it |
| Register the models on the Inference engines screen | Members can start sessions with those models |

llama-server stays yours. The Control Plane neither starts nor stops it, and puts no cost on it.
Usage is recorded as before: the Control Plane's gateway reads the token counts in the answer and writes them to the member's usage, streaming included.

## Start llama-server

Run these commands on the LAN machine, not inside a workspace.

| How to get it | Linux | Windows |
|---|---|---|
| Container | `ghcr.io/ggml-org/llama.cpp:server-cuda` (the same image this deployment uses) | — |
| Package | `winget install llama.cpp` | The same |
| Release archive | `llama-bNNNNN-bin-ubuntu-cuda-*.tar.gz` | `llama-bNNNNN-bin-win-cuda-*.zip` and `cudart-*.zip` (the CUDA runtime ships separately) |

CUDA builds for Linux exist only in newer releases (b11065 and later). With an NVIDIA GPU, the container is the safest choice.

The shape of the command:

```
llama-server -hf <repo>:<quant> --alias <model id> -c <window> -ngl 99 --jinja \
             --host 0.0.0.0 --port 8080 --api-key <key>
```

| Argument | Why |
|---|---|
| `--alias` | Becomes the name in `/v1/models`. Without it the `-hf` / `-m` path appears as is, and an unreadable name stays in the launch menu and the records. Serving one model, it answers even when the request's `model` does not match. In router mode, which serves several models, a name that does not match does not arrive |
| `-c` | At least the `context_tokens` of the model you register. 8192 or more is a guide |
| `--host 0.0.0.0` | Without it, it answers on loopback only |
| `--port` | Set it explicitly. llama.cpp has announced a change to its default port |
| `--api-key` | **Without it, the server is open to the LAN with no authentication** |
| `--jinja` | On by default, but set it for older builds |

- A gated Hugging Face repository needs `HF_TOKEN=...` (or `-hft <token>`)
- `--sleep-idle-seconds <N>` unloads the model when unused and gives the VRAM back
- The families confirmed to work are Qwen3, GPT-OSS and Gemma. `llama-3.1` does not get tool calls through, so keep it to uses without tools

## Tell the Control Plane where it is

The Control Plane reads these once at start. Restart the Control Plane after changing them.

| Variable | Meaning |
|---|---|
| `AF_LLM_URL` | The full URL with the port, as the Control Plane sees it (for example `http://192.168.1.20:8080`). Setting it creates the `llm` row |
| `AF_ENGINE_API_KEY_LLM` | Optional. Sent as `Authorization: Bearer`. Use the same value as llama-server's `--api-key` |

| Target | Where it goes |
|---|---|
| compose / docker | `.env` next to the compose file |
| native | The environment of `af start`, or `Environment=` in the systemd unit ([the native runbook](../../../deploy/native/README.md)) |

- `AF_LLM_URL` is a shortcut. Writing a `llm` row with `lifecycle: "external"` yourself in `AF_ENGINES_JSON` (on AWS, the engine stack's table) does the same
- Either way the key is read from `AF_ENGINE_API_KEY_LLM`. There is no `AF_LLM_API_KEY`

## Register the models

**While this deployment borrows another deployment's engines, `llm` models cannot be registered.** Always go in this order:

1. Set `AF_LLM_URL` (and `AF_ENGINE_API_KEY_LLM`) and restart the Control Plane.
2. Under Admin → "**Inference engines**", register the models on the `llm` row. Make the **id** the name you gave `--alias`.

Nothing takes files in. Write the id, `base_model` and window (`context_tokens`) so they match llama-server's command. The screen is in [Take models into the inference engines](models.md).

## Switching from a borrowed engine

Coming from [Borrow another deployment's engines](borrow.md), the following changes.

| | Borrowed | llama.cpp on the LAN |
|---|---|---|
| Authentication | A token per membership. Deleting the membership stops it | One shared bearer. There is no way to shut out one person, and rotating it shuts out everyone |
| When it is stopped | Waits for the cold start. Slow, but it gets there | Does not wait. Fails at once with `503 engine_unavailable` |

## When it goes wrong

### Chat fails at once

The Control Plane does not wait for an engine it does not own. When llama-server does not answer, the request is refused at once with `503 engine_unavailable`. The body carries the URL it tried and the health path (`/health`).

1. Is llama-server running on that machine?
2. Can the Control Plane's machine reach that address and port?
3. Is `AF_ENGINE_API_KEY_LLM` the same as `--api-key`?

### What the panel shows and does not show

| Item | Shown |
|---|---|
| warm | A liveness check when the panel opens. It looks at `/models` (`/health` answers ok even with no model loaded) |
| History | Not shown. An external row has no control loop to record it |
| Cost | None |

## When a member connects to their own llama-server

Everything above is a deployment-wide setting. Separately, a member can connect straight to their own llama-server under Settings → Agents → llama.cpp, "**Your own connection**" ([Use lcpp](../../user/agents/lcpp.md)).

- Once set, it wins over the deployment's `llm` engine for that member's sessions
- It does not go through the Control Plane, so it is not stopped even when a tenant administrator has turned chat inference off. Like any other tool a member points at a server on their own network, this is intended

## Related

- [Generate images on your own ComfyUI](comfyui.md)
- [Borrow another deployment's engines](borrow.md)
- [Take models into the inference engines](models.md)
- [Deployment targets](../../ref/deploy-targets.md)
