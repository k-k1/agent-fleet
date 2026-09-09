# ComfyUI, pinned — the fleet's own image (ADR 0072 decision 4, phase P2; ADR 0072 "未解決の点" 8).
#
# The community image measured for ADR 0072 (ghcr.io/lecode-official/comfyui-docker) bakes
# ComfyUI v0.8.2 and checks out v0.34.0 at CONTAINER START, which cost 20-26 seconds of NAT-
# dependent git+pip work on every cold start on top of a 205-second, 5.36 GB pull. This image
# bakes v0.34.0 directly so a cold start pays for neither.
#
# ⚠️ The revision is pinned on purpose (ADR 0071 decision 6: this deployment tracks upstream
# revisions explicitly, never `latest`) — bump COMFYUI_REF deliberately, rebuild via
# .github/workflows/comfyui-image.yml, and re-run the golden workflow-template tests, because
# ComfyUI's node graph API has no version-to-version compatibility promise (ADR 0072 decision 4).
#
# No ComfyUI-Manager: it exists to install custom nodes at runtime from inside the UI, which is
# both unreachable (P2 ships no pane — that is P5) and the one thing a pinned, golden-tested
# deployment must not do to itself. ComfyUI-GGUF is not baked either — the ADR's own GPU
# measurement got SDXL, Z-Image-Turbo and FLUX.2 [klein] 4B running on fp8 safetensors alone.
#
# Base: an official PyTorch image already carries a CUDA-matched torch build. Cloning ComfyUI's
# requirements.txt on TOP of that (rather than the other way around) is what stops pip silently
# replacing a CUDA wheel with a CPU-only one from PyPI — the trap every "pip install -r
# requirements.txt first" ComfyUI Dockerfile on the internet has hit at least once.
#
# 🔴 torch 2.5.1 measured BROKEN for v0.34.0 (real GPU, 2026-09-10): comfy-kitchen==0.2.31 (a
# requirements.txt dependency since this ADR was drafted) registers a custom op with a
# `list[int]`-typed parameter, and torch 2.5.1's `torch.library.infer_schema` does not
# recognise that PEP 585 generic spelling — ComfyUI fails at import, before ever binding a
# port (`ValueError: infer_schema(func): Parameter kernel_size has unsupported type
# list[int]`). ComfyUI's own README states "torch 2.7 is minimally supported"; 2.9.1 is used
# here as a comfortably-past-the-floor, non-bleeding-edge release (their own advice is against
# anything less than two weeks old).
FROM pytorch/pytorch:2.9.1-cuda12.8-cudnn9-runtime

ARG COMFYUI_REF=v0.34.0

LABEL org.opencontainers.image.source="https://github.com/k-k1/agent-fleet" \
      org.opencontainers.image.description="Agent Fleet's pinned ComfyUI build for the image engine role (ADR 0072 P2)" \
      org.opencontainers.image.licenses="GPL-3.0"

RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /ComfyUI
# Shallow + pinned tag: this is a supply-chain-relevant fetch (ADR 0071 decision 6), and a
# shallow clone at a tag is the smallest one that still records which commit it is.
RUN git clone --depth 1 --branch "${COMFYUI_REF}" https://github.com/comfyanonymous/ComfyUI.git . \
 && echo "${COMFYUI_REF}" > /ComfyUI/.af-pinned-ref
# torch/torchvision/torchaudio are already the base image's CUDA build; ComfyUI's own
# requirements.txt lists them unpinned, so pip resolves them as already satisfied and installs
# only what is actually missing (numpy, pillow, the transformers/tokenizers pair, etc.).
RUN pip install --no-cache-dir -r requirements.txt

# The models directory is NEVER baked in — the fetch sidecar in the image role's task
# definition (60-engines.yaml) points it at the shared model volume at container start
# (`ln -sfn /models/image /ComfyUI/models`), which is what makes the S3 layout ADR 0071
# decision 6 already follows ("ComfyUI's own convention") work with zero copying.
#
# No EXPOSE/CMD pinning the port: like the llama.cpp and stable-diffusion.cpp roles, the ECS
# task definition's own idle wrapper decides the port and every other flag at container start,
# not this image.
CMD ["python3", "main.py", "--listen", "0.0.0.0", "--port", "8080"]
