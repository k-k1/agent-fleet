#!/usr/bin/env python3
"""ADR 0072 image-engine bench: drives a ComfyUI next to it over HTTP and logs timings.

Runs inside the bench task (bench-image-engine.sh) beside the ComfyUI container, on
localhost:8188. Everything it prints goes to CloudWatch; the pictures and results.jsonl go to
/out, which the upload container copies to S3 when this exits.

Phases: PHASES is a comma-separated list of labels, one per flag set the comfy container
starts ComfyUI with. The scenarios run once per phase; between phases this touches
/out/next, which the comfy container's loop reads as "restart with the next flags".
"""
import json
import os
import sys
import time
import urllib.error
import urllib.request

BASE = os.environ.get("COMFY_URL", "http://127.0.0.1:8188")
OUT = os.environ.get("OUT_DIR", "/out")
TE = os.environ.get("TEXT_ENCODER", "qwen_3_4b_fp8_mixed.safetensors")
SEED = 1234
PROMPT = "a red fox sitting on a mossy rock in a misty forest at dawn, detailed fur, soft light"
NEG = "blurry, lowres, deformed, watermark, text"


def log(*a):
    print(time.strftime("%H:%M:%S"), *a, flush=True)


def get(path, timeout=30):
    with urllib.request.urlopen(BASE + path, timeout=timeout) as r:
        return json.loads(r.read())


def post(path, body):
    req = urllib.request.Request(BASE + path, data=json.dumps(body).encode(),
                                 headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            return r.status, json.loads(r.read())
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read() or b"{}")


def stats():
    s = get("/system_stats")
    d = s["devices"][0]
    return {"vram_total_mib": d["vram_total"] // 2**20, "vram_free_mib": d["vram_free"] // 2**20,
            "torch_vram_free_mib": d["torch_vram_free"] // 2**20,
            "ram_total_mib": s["system"]["ram_total"] // 2**20, "ram_free_mib": s["system"]["ram_free"] // 2**20,
            "comfyui": s["system"].get("comfyui_version"), "torch": s["system"].get("pytorch_version")}


# --- graphs, in the API format /prompt takes -------------------------------------------
#
# Node ids are words rather than numbers so a validation error names something readable.
# The parameter values are the ones the official templates carry (Comfy-Org/workflow_templates:
# image_sdxl_simple, image_z_image, image_flux2_klein_text_to_image), except SDXL's 20 steps,
# which is what ADR 0071's ComfyUI measurement used and what keeps it comparable.

def g_sdxl(name, seed, lora=None, steps=20, size=1024):
    g = {
        "ckpt": {"class_type": "CheckpointLoaderSimple", "inputs": {"ckpt_name": "sd_xl_base_1.0.safetensors"}},
        "pos": {"class_type": "CLIPTextEncode", "inputs": {"text": PROMPT, "clip": ["ckpt", 1]}},
        "neg": {"class_type": "CLIPTextEncode", "inputs": {"text": NEG, "clip": ["ckpt", 1]}},
        "lat": {"class_type": "EmptyLatentImage", "inputs": {"width": size, "height": size, "batch_size": 1}},
        "ks": {"class_type": "KSampler", "inputs": {
            "seed": seed, "steps": steps, "cfg": 7, "sampler_name": "dpmpp_2m", "scheduler": "karras", "denoise": 1,
            "model": ["ckpt", 0], "positive": ["pos", 0], "negative": ["neg", 0], "latent_image": ["lat", 0]}},
        "dec": {"class_type": "VAEDecode", "inputs": {"samples": ["ks", 0], "vae": ["ckpt", 2]}},
        "save": {"class_type": "SaveImage", "inputs": {"filename_prefix": name, "images": ["dec", 0]}},
    }
    if lora:
        g["lora"] = {"class_type": "LoraLoader", "inputs": {
            "lora_name": lora, "strength_model": 1.0, "strength_clip": 1.0, "model": ["ckpt", 0], "clip": ["ckpt", 1]}}
        g["pos"]["inputs"]["clip"] = ["lora", 1]
        g["neg"]["inputs"]["clip"] = ["lora", 1]
        g["ks"]["inputs"]["model"] = ["lora", 0]
    return g


def g_zimage(name, seed):
    # Z-Image-Turbo: guidance-distilled, 8 steps at cfg 1 (the model card's recipe).
    return {
        "unet": {"class_type": "UNETLoader", "inputs": {"unet_name": "z_image_turbo_bf16.safetensors", "weight_dtype": "default"}},
        "clip": {"class_type": "CLIPLoader", "inputs": {"clip_name": TE, "type": "lumina2", "device": "default"}},
        "vae": {"class_type": "VAELoader", "inputs": {"vae_name": "ae.safetensors"}},
        "ms": {"class_type": "ModelSamplingAuraFlow", "inputs": {"shift": 3, "model": ["unet", 0]}},
        "pos": {"class_type": "CLIPTextEncode", "inputs": {"text": PROMPT, "clip": ["clip", 0]}},
        "neg": {"class_type": "CLIPTextEncode", "inputs": {"text": "", "clip": ["clip", 0]}},
        "lat": {"class_type": "EmptySD3LatentImage", "inputs": {"width": 1024, "height": 1024, "batch_size": 1}},
        "ks": {"class_type": "KSampler", "inputs": {
            "seed": seed, "steps": 8, "cfg": 1, "sampler_name": "res_multistep", "scheduler": "simple", "denoise": 1,
            "model": ["ms", 0], "positive": ["pos", 0], "negative": ["neg", 0], "latent_image": ["lat", 0]}},
        "dec": {"class_type": "VAEDecode", "inputs": {"samples": ["ks", 0], "vae": ["vae", 0]}},
        "save": {"class_type": "SaveImage", "inputs": {"filename_prefix": name, "images": ["dec", 0]}},
    }


def g_klein(name, seed):
    # FLUX.2 [klein] 4B, the distilled checkpoint: 4 steps, cfg 1, zeroed negative.
    return {
        "unet": {"class_type": "UNETLoader", "inputs": {"unet_name": "flux-2-klein-4b.safetensors", "weight_dtype": "default"}},
        "clip": {"class_type": "CLIPLoader", "inputs": {"clip_name": TE, "type": "flux2", "device": "default"}},
        "vae": {"class_type": "VAELoader", "inputs": {"vae_name": "flux2-vae.safetensors"}},
        "pos": {"class_type": "CLIPTextEncode", "inputs": {"text": PROMPT, "clip": ["clip", 0]}},
        "zero": {"class_type": "ConditioningZeroOut", "inputs": {"conditioning": ["pos", 0]}},
        "guider": {"class_type": "CFGGuider", "inputs": {"model": ["unet", 0], "positive": ["pos", 0], "negative": ["zero", 0], "cfg": 1}},
        "sampler": {"class_type": "KSamplerSelect", "inputs": {"sampler_name": "euler"}},
        "sigmas": {"class_type": "Flux2Scheduler", "inputs": {"steps": 4, "width": 1024, "height": 1024}},
        "lat": {"class_type": "EmptyFlux2LatentImage", "inputs": {"width": 1024, "height": 1024, "batch_size": 1}},
        "noise": {"class_type": "RandomNoise", "inputs": {"noise_seed": seed}},
        "sca": {"class_type": "SamplerCustomAdvanced", "inputs": {
            "noise": ["noise", 0], "guider": ["guider", 0], "sampler": ["sampler", 0], "sigmas": ["sigmas", 0], "latent_image": ["lat", 0]}},
        "dec": {"class_type": "VAEDecode", "inputs": {"samples": ["sca", 0], "vae": ["vae", 0]}},
        "save": {"class_type": "SaveImage", "inputs": {"filename_prefix": name, "images": ["dec", 0]}},
    }


# The order is the measurement: first visit (load), second visit (warm), a round trip through
# the others (does switching back cost a reload?), the LoRA with and without at one seed, and a
# 512px SDXL to separate load time from compute.
#
# ⚠️ The seed offsets are not decoration. ComfyUI caches node outputs by their inputs, so a
# prompt identical to an earlier one executes nothing (measured: 0.5 s, "warm" in name only).
# A different seed re-runs the sampler and everything after it while the loaders stay cached —
# which is exactly the warm case. The LoRA pair (sdxl_1 / sdxl_4_lora) shares a seed on purpose:
# that picture comparison is the point, and its timing is read off sdxl_5.
SCENARIOS = [
    ("sdxl_1", lambda n, s: g_sdxl(n, s), 0),
    ("sdxl_2", lambda n, s: g_sdxl(n, s), 1),
    ("zimage_1", g_zimage, 0),
    ("zimage_2", g_zimage, 1),
    ("klein_1", g_klein, 0),
    ("klein_2", g_klein, 1),
    ("sdxl_3_switchback", lambda n, s: g_sdxl(n, s), 2),
    ("zimage_3_switchback", g_zimage, 2),
    ("klein_3_switchback", g_klein, 2),
    ("sdxl_4_lora", lambda n, s: g_sdxl(n, s, lora="pixel-art-xl.safetensors"), 0),
    ("sdxl_5_lora_again", lambda n, s: g_sdxl(n, s, lora="pixel-art-xl.safetensors"), 1),
    ("sdxl_6_nolora_again", lambda n, s: g_sdxl(n, s), 3),
    ("sdxl_7_512", lambda n, s: g_sdxl(n, s, size=512), 0),
]


def run(name, graph):
    t0 = time.time()
    code, resp = post("/prompt", {"prompt": graph, "client_id": "bench"})
    if code != 200:
        log(f"RESULT {name}: submit failed {code}: {json.dumps(resp)[:1500]}")
        return {"name": name, "ok": False, "submit_error": resp}
    pid = resp["prompt_id"]
    hist = None
    while time.time() - t0 < 1800:
        h = get(f"/history/{pid}")
        st = h.get(pid, {}).get("status", {})
        if st.get("completed") or st.get("status_str") == "error":
            hist = h[pid]
            break
        time.sleep(0.5)
    wall = time.time() - t0
    if hist is None:
        log(f"RESULT {name}: timeout after {wall:.1f}s")
        return {"name": name, "ok": False, "wall_s": wall}
    st = hist.get("status", {})
    # ComfyUI's own clock: execution_start -> execution_success, which excludes queue time.
    msgs = {m[0]: m[1].get("timestamp") for m in st.get("messages", []) if isinstance(m, list) and len(m) == 2}
    end = msgs.get("execution_success") or msgs.get("execution_error")
    exec_s = (end - msgs["execution_start"]) / 1000 if msgs.get("execution_start") and end else None
    files = []
    for out in hist.get("outputs", {}).values():
        for im in out.get("images", []):
            files.append(im["filename"])
            try:
                q = f"/view?filename={im['filename']}&subfolder={im.get('subfolder', '')}&type={im.get('type', 'output')}"
                with urllib.request.urlopen(BASE + q, timeout=60) as r:
                    data = r.read()
                with open(os.path.join(OUT, im["filename"]), "wb") as f:
                    f.write(data)
            except Exception as e:  # noqa: BLE001
                log(f"  view failed for {im['filename']}: {e}")
    s = stats()
    err = None
    if st.get("status_str") == "error":
        err = json.dumps([m[1] for m in st.get("messages", []) if m[0] == "execution_error"])[:1500]
    rec = {"name": name, "ok": st.get("status_str") == "success", "wall_s": round(wall, 2),
           "exec_s": exec_s and round(exec_s, 2), "files": files,
           "vram_used_mib": s["vram_total_mib"] - s["vram_free_mib"], "ram_free_mib": s["ram_free_mib"], "error": err}
    log("RESULT", json.dumps(rec))
    return rec


def wait_up(budget):
    t0 = time.time()
    log("waiting for ComfyUI at", BASE)
    while True:
        try:
            s = stats()
            break
        except Exception as e:  # noqa: BLE001
            if time.time() - t0 > budget:
                log("ComfyUI never answered:", e)
                sys.exit(1)
            time.sleep(3)
    log(f"ComfyUI up after {time.time() - t0:.0f}s:", json.dumps(s))


def wait_down():
    t0 = time.time()
    while time.time() - t0 < 120:
        try:
            get("/system_stats", timeout=5)
            time.sleep(1)
        except Exception:  # noqa: BLE001
            return
    log("ComfyUI did not go down in 120s")


def summary(rows):
    return json.dumps([{k: r.get(k) for k in ("name", "ok", "wall_s", "exec_s", "vram_used_mib")} for r in rows])


def main():
    os.makedirs(OUT, exist_ok=True)
    phases = [p for p in os.environ.get("PHASES", "default").split(",") if p]
    all_results = []
    for i, phase in enumerate(phases):
        if i > 0:
            log(f"=== phase {phase}: asking for a restart")
            open(os.path.join(OUT, "next"), "w").close()
            wait_down()
        wait_up(2400 if i == 0 else 600)
        log(f"=== phase {phase} ===")
        results = [dict(run(f"{phase}_{name}", build(f"{phase}_{name}", SEED + off)), phase=phase) for name, build, off in SCENARIOS]
        all_results += results
        log("PHASE SUMMARY", phase, summary(results))
    with open(os.path.join(OUT, "results.jsonl"), "w") as f:
        for r in all_results:
            f.write(json.dumps(r) + "\n")
    log("SUMMARY", summary(all_results))
    open(os.path.join(OUT, "next"), "w").close()  # lets the comfy container's loop end too


if __name__ == "__main__":
    main()
