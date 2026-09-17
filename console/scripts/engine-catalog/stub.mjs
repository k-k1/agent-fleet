// Stub Control Plane for the model-catalogue screens (ADR 0085 P2).
//
// Same idea as scripts/gallery-perf/stub.mjs next door, with one difference: the CP half of ADR
// 0085 is not merged yet, so there is no deployment anywhere that answers `GET …/objects`, the
// plan on `…/ingest/resolve` or `…/models/{id}/complete`. This is what the Console is driven
// against until there is — it answers exactly the wire contract in the ADR, and everything else
// (whoami, sessions, the shell the pane lives in) is proxied to scripts/shots/server.mjs, which
// already boots the real bundle from fixtures.
//
// 🔴 The fixtures are the af-sandbox state of 2026-09-15, which is what the ADR was written
// about: a misplaced 13 GB main file nobody declares, a loose text encoder, a failed job holding
// a key, and a row that cannot be enabled. That state had no screen at all — it is the thing to
// look at when judging whether these screens work.
//
//   npm --prefix console run build
//   node console/scripts/engine-catalog/stub.mjs [--port 8801]
import http from "node:http";
import path from "node:path";
import zlib from "node:zlib";
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const argv = process.argv.slice(2);
const arg = (n, d) => {
  const i = argv.indexOf(`--${n}`);
  return i >= 0 && argv[i + 1] ? argv[i + 1] : d;
};
const PORT = Number(arg("port", 8801));
const SHELL_PORT = Number(arg("shell-port", PORT + 1));
const LOCALE = arg("locale", "ja");

// ---- fixtures ----------------------------------------------------------------------
const ENGINES = {
  super_admin: true,
  engines: [
    {
      key: "image",
      api: "images",
      provider: "comfyui",
      managed: true,
      enabled: true,
      state: "running",
      base_models: ["sdxl", "anima", "krea2", "flux1"],
      file_flags: ["", "--diffusion-model", "--clip_l", "--t5xxl", "--vae"],
      class: { id: "g6e-xlarge", vram_mib: 24576 },
      model_rows: [
        {
          id: "anima-aesthetic-v1.1", kind: "model", enabled: false, base_model: "anima",
          description: "Anima Aesthetic — 2B, Cosmos 2 の分割族",
          license_name: "Anima Community License", commercial_use: "no",
          files_missing: ["--clip_l"], vram_need_mib: 9800, vram_need_source: "floor",
          file_rows: [
            { s3Key: "image/diffusion_models/anima-aesthetic-v1.1.safetensors", flag: "--diffusion-model", bytes: 4_182_230_656, source: "hf:circlestone-labs/Anima" },
            { s3Key: "image/vae/qwen_image_vae.safetensors", flag: "--vae", bytes: 253_806_080 },
          ],
        },
        {
          id: "sdxl-base-1.0", kind: "model", enabled: true, selected: true, base_model: "sdxl",
          description: "SDXL 1.0 base — 既定のチェックポイント",
          license_name: "CreativeML Open RAIL++-M", commercial_use: "yes", vram_need_mib: 7400,
          // ADR 0088: what the publisher calls it, and the example image it publishes. The row
          // above deliberately carries neither — the two states have to be on one screen, because
          // the button that fills the second in is offered on exactly that difference.
          display_name: "Stable Diffusion XL", version_name: "base 1.0",
          preview_url: "/stub/preview/sdxl-large.png", thumb_url: "/stub/preview/sdxl.png",
          source: "hf:stabilityai/stable-diffusion-xl-base-1.0/sd_xl_base_1.0.safetensors",
          file_rows: [{ s3Key: "image/checkpoints/sd_xl_base_1.0.safetensors", bytes: 6_938_040_576, source: "hf:stabilityai/stable-diffusion-xl-base-1.0" }],
        },
        {
          id: "meinamix_meinav11_5038", kind: "model", enabled: true, base_model: "sdxl",
          display_name: "MeinaMix", version_name: "Meina V11",
          preview_url: "/stub/preview/meina-large.png", thumb_url: "/stub/preview/meina.png",
          license_name: "see civitai model page", vram_need_mib: 5312, source: "civitai:5038",
          file_rows: [{ s3Key: "image/checkpoints/meinamix_meinav11.safetensors", bytes: 2_132_625_894, source: "civitai:5038" }],
        },
        {
          id: "abyssorangemix2_hard_8832", kind: "model", enabled: false,
          license_name: "see civitai model page", vram_need_mib: 5312, source: "civitai:8832",
          file_rows: [{ s3Key: "image/checkpoints/abyssorangemix2_Hard_8832.safetensors", bytes: 5_600_000_000, source: "civitai:8832" }],
        },
      ],
    },
    {
      key: "llm", api: "chat", provider: "llamacpp", managed: true, enabled: true, state: "running",
      class: { id: "g6.xlarge", label: "g6.xlarge", vram_mib: 22000 },
      classes: [
        { id: "g6.xlarge", label: "g6.xlarge", vram_mib: 22000 },
        { id: "g6e.xlarge", label: "g6e.xlarge", vram_mib: 46068 },
      ],
      // ADR 0089: two quantisations of ONE repository, which is the shape the repository card
      // exists for. Sizes and names are the real ones (unsloth/Qwen3.8-27B-GGUF, 2026-09-18).
      model_rows: [
        {
          id: "qwen3_8_27b_ud_iq2_xxs", kind: "gguf", enabled: true, default: true,
          display_name: "unsloth/Qwen3.8-27B-GGUF", context_tokens: 32768, max_output_tokens: 4096,
          source: "hf:unsloth/Qwen3.8-27B-GGUF/Qwen3.8-27B-UD-IQ2_XXS.gguf",
          license_name: "apache-2.0",
          file_rows: [{ s3Key: "llm/models/Qwen3.8-27B-UD-IQ2_XXS.gguf", bytes: 7_270_000_000 }],
        },
        {
          id: "qwen3_8_27b_ud_iq2_s", kind: "gguf", enabled: false,
          display_name: "unsloth/Qwen3.8-27B-GGUF", context_tokens: 32768, max_output_tokens: 4096,
          source: "hf:unsloth/Qwen3.8-27B-GGUF/Qwen3.8-27B-UD-IQ2_S.gguf",
          license_name: "apache-2.0",
          file_rows: [{ s3Key: "llm/models/Qwen3.8-27B-UD-IQ2_S.gguf", bytes: 8_370_000_000 }],
        },
      ],
    },
  ],
};

const OBJECTS = [
  {
    key: "image/checkpoints/split_files/diffusion_models/krea2-v2.safetensors",
    bytes: 13_100_000_000, last_modified: "2026-09-14T22:10:00Z",
    role_dir: "other", placement: "misplaced", state: "present", declared_by: [],
    source: "hf:krea-ai/krea2", license: "other",
  },
  // 🔴 A re-ingest over bytes that are already there: the ledger keeps `present` and only the JOB
  // says `uploading`. This is the row that offered 登録 and answered 409 on af-sandbox.
  {
    key: "image/diffusion_models/krea2_raw_fp8_scaled.safetensors",
    bytes: 13_100_000_000, last_modified: "2026-09-15T08:00:00Z",
    role_dir: "diffusion_models", placement: "ok", state: "present", declared_by: [],
    source: "hf:krea-ai/krea2",
    job: { id: "job-11", state: "uploading", created_at: "2026-09-15T08:00:00Z" },
  },
  {
    key: "image/text_encoders/qwen_3_06b_base.safetensors",
    bytes: 1_190_000_000, last_modified: "2026-09-14T22:06:00Z",
    role_dir: "text_encoders", placement: "ok", state: "present", declared_by: [],
    source: "hf:circlestone-labs/Anima",
  },
  {
    key: "image/vae/flux_vae.safetensors", bytes: 335_304_388, last_modified: "2026-09-15T02:30:00Z",
    role_dir: "vae", placement: "ok", state: "failed", declared_by: [],
    job: { id: "job-7", state: "failed", message: "403 Forbidden — the account has not accepted this repository's terms", created_at: "2026-09-15T02:30:00Z" },
  },
  {
    key: "image/diffusion_models/anima-aesthetic-v1.1.safetensors",
    bytes: 4_182_230_656, last_modified: "2026-09-13T11:02:00Z",
    role_dir: "diffusion_models", placement: "ok", state: "present",
    declared_by: [{ model_id: "anima-aesthetic-v1.1", flag: "--diffusion-model" }],
    source: "hf:circlestone-labs/Anima", license: "other",
  },
  {
    key: "image/vae/qwen_image_vae.safetensors", bytes: 253_806_080, last_modified: "2026-09-13T11:04:00Z",
    role_dir: "vae", placement: "ok", state: "present",
    declared_by: [{ model_id: "anima-aesthetic-v1.1", flag: "--vae" }],
  },
  // A row pointing at bytes that are not there: the ledger names the row, and the act is that
  // row's 揃える (the object side has nothing anybody could press).
  {
    key: "image/vae/sdxl_vae.safetensors", role_dir: "vae", placement: "ok", state: "missing",
    declared_by: [{ model_id: "sdxl-base-1.0", flag: "--vae" }],
  },
  {
    key: "image/checkpoints/sd_xl_base_1.0.safetensors", bytes: 6_938_040_576, last_modified: "2026-08-02T09:00:00Z",
    role_dir: "checkpoints", placement: "ok", state: "present",
    declared_by: [{ model_id: "sdxl-base-1.0" }],
    source: "hf:stabilityai/stable-diffusion-xl-base-1.0", license: "openrail++",
  },
];

const HITS = [
  {
    source: "civitai", ref: "782002", model_ref: "914", name: "Anima Aesthetic v1.1",
    base_model: "Anima", license_name: "Anima Community License", commercial_use: "no",
    downloads: 41200, likes: 3100, published_at: "2026-08-28T00:00:00Z", updated_at: "2026-09-12T00:00:00Z",
    preview_url: "/stub/preview/anima-large.png", thumb_url: "/stub/preview/anima.png",
    url: "https://example.invalid/models/914", nsfw_level: 1,
  },
  {
    source: "civitai", ref: "781001", model_ref: "913", name: "Krea2 Photoreal",
    base_model: "Krea 2", license_name: "CreativeML Open RAIL++-M", commercial_use: "yes",
    downloads: 128000, likes: 9800, updated_at: "2026-09-10T00:00:00Z",
    preview_url: "/stub/preview/krea-large.png", thumb_url: "/stub/preview/krea.png",
    url: "https://example.invalid/models/913",
  },
  {
    source: "civitai", ref: "780500", model_ref: "912", name: "Pony V7 Illustration",
    base_model: "AuraFlow", license_name: "Fair AI Public License 1.0-SD",
    downloads: 76000, likes: 5400, updated_at: "2026-09-08T00:00:00Z",
    restrictions: ["no_derivatives"], login_required: "yes",
    preview_url: "/stub/preview/pony-large.png", thumb_url: "/stub/preview/pony.png",
    url: "https://example.invalid/models/912",
  },
];

/** The plan the CP answers for the first hit: a split family, one part already in the bucket. */
const PLAN = {
  plan_token: "sha256:6f1c…e2",
  id: "anima-aesthetic-v1-1",
  base_model: "anima",
  main_flag: "--diffusion-model",
  files: [
    { flag: "--diffusion-model", name: "anima-aesthetic-v1.1.safetensors", bytes: 4_182_230_656, action: "download", source: "civitai:782002", key: "image/diffusion_models/anima-aesthetic-v1.1.safetensors" },
    // 🔴 `source` on a move and a reuse is the key the bytes are at TODAY, not an upstream: the
    // move's origin rides inside the plan's hash, so an object that wanders makes the token stale.
    { flag: "--clip_l", name: "qwen_3_06b_base.safetensors", bytes: 1_190_000_000, action: "move", source: "image/text_encoders/qwen_3_06b_base.safetensors", key: "image/text_encoders/qwen_3_06b_base.safetensors" },
    { flag: "--vae", name: "qwen_image_vae.safetensors", bytes: 253_806_080, action: "reuse", source: "image/vae/qwen_image_vae.safetensors", key: "image/vae/qwen_image_vae.safetensors" },
  ],
  bytes_to_download: 4_182_230_656,
  warnings: ["このライセンスは商用利用を認めていません。生成物の扱いは配布元の条項を読んでください。"],
};

const RESOLVED = {
  sha256: "f0d1…", artifact_identity: "civitai:782002/anima-aesthetic-v1.1.safetensors#sha256:f0d1",
  bytes: 4_182_230_656, can_ingest: true,
  license: "other", license_name: "Anima Community License", commercial_use: "no",
  restrictions: ["noncommercial"],
  plan: PLAN,
};

// ---- a tiny PNG, so the cards have their example images without reaching the internet ------
function swatchPNG(key, edge) {
  const W = edge || 64;
  const H = Math.round(W * 1.18);
  let h = 2166136261;
  for (let i = 0; i < key.length; i++) h = Math.imul(h ^ key.charCodeAt(i), 16777619);
  const base = [64 + (Math.abs(h) % 120), 64 + (Math.abs(h >> 8) % 120), 64 + (Math.abs(h >> 16) % 120)];
  const raw = Buffer.alloc(H * (1 + W * 3));
  for (let y = 0; y < H; y++) {
    const row = y * (1 + W * 3);
    raw[row] = 0;
    for (let x = 0; x < W; x++) {
      const o = row + 1 + x * 3;
      const t = (x + y) / (W + H);
      raw[o] = Math.round(base[0] * (0.5 + t * 0.6));
      raw[o + 1] = Math.round(base[1] * (0.5 + t * 0.6));
      raw[o + 2] = Math.round(base[2] * (0.5 + t * 0.6));
    }
  }
  const chunk = (type, body) => {
    const len = Buffer.alloc(4);
    len.writeUInt32BE(body.length);
    const td = Buffer.concat([Buffer.from(type, "ascii"), body]);
    const crc = Buffer.alloc(4);
    crc.writeUInt32BE(crc32(td) >>> 0);
    return Buffer.concat([len, td, crc]);
  };
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(W, 0);
  ihdr.writeUInt32BE(H, 4);
  ihdr[8] = 8;
  ihdr[9] = 2;
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", ihdr),
    chunk("IDAT", zlib.deflateSync(raw)),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}
const CRC_TABLE = (() => {
  const t = new Int32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    t[n] = c;
  }
  return t;
})();
function crc32(buf) {
  let c = -1;
  for (let i = 0; i < buf.length; i++) c = CRC_TABLE[(c ^ buf[i]) & 0xff] ^ (c >>> 8);
  return c ^ -1;
}

// ---- the engine half of the API ----------------------------------------------------
function engineRoute(method, pathname) {
  const engine = pathname.match(/^\/api\/admin\/engines\/([^/]+)/)?.[1];
  const rest = engine ? pathname.slice(`/api/admin/engines/${engine}`.length) : "";
  if (pathname === "/api/admin/engines") return () => ENGINES;
  if (!engine) return null;
  if (rest === "/objects" && method === "GET") return () => ({ objects: OBJECTS, checked_at: new Date().toISOString() });
  if (rest === "/objects" && method === "DELETE") return (body) => ({ deleting: body?.key });
  if (rest === "/objects/register") return () => ({
    model_id: "krea2-v2", moved: true, jobs: [],
    complete: { action: "attached", files: [], bytes_to_download: 0 },
  });
  if (rest === "/ingest/search") return () => ({ hits: HITS });
  if (rest === "/ingest/versions") return () => ({ versions: [{ ref: "782002", name: "v1.1" }, { ref: "770100", name: "v1.0" }] });
  if (rest === "/ingest/files") {
    // The repository ladder asks for a whole repository (no `file`); the wizard asks for the one
    // it is about to resolve. ADR 0089: the answer carries the role of each file and the KV cost
    // of a 1,024-token window, which is what prices every line of the ladder.
    return (body) => body?.source?.hf?.repo?.includes("Qwen3.8-27B")
      ? {
        kv_mib_per_1k_tokens: 260, kv_from: "Qwen3.8-27B-UD-IQ4_XS.gguf",
        files: [
          { name: "imatrix_unsloth.gguf", bytes: 10_000_000, sha256: "a1", role: "imatrix" },
          { name: "mmproj-F16.gguf", bytes: 930_000_000, sha256: "a2", role: "projector" },
          { name: "Qwen3.8-27B-UD-IQ1_S.gguf", bytes: 6_190_000_000, sha256: "a3", role: "model" },
          { name: "Qwen3.8-27B-UD-IQ2_XXS.gguf", bytes: 7_270_000_000, sha256: "a4", role: "model" },
          { name: "Qwen3.8-27B-UD-IQ2_S.gguf", bytes: 8_370_000_000, sha256: "a5", role: "model" },
          { name: "Qwen3.8-27B-UD-Q2_K_XL.gguf", bytes: 9_830_000_000, sha256: "a6", role: "model" },
          { name: "Qwen3.8-27B-UD-IQ3_XXS.gguf", bytes: 10_930_000_000, sha256: "a7", role: "model" },
          { name: "Qwen3.8-27B-UD-IQ3_S.gguf", bytes: 12_040_000_000, sha256: "a8", role: "model" },
          { name: "Qwen3.8-27B-UD-Q3_K_XL.gguf", bytes: 13_150_000_000, sha256: "a9", role: "model" },
          { name: "Qwen3.8-27B-UD-IQ4_XS.gguf", bytes: 14_250_000_000, sha256: "b1", role: "model" },
        ],
      }
      : { files: [{ name: "anima-aesthetic-v1.1.safetensors", bytes: 4_182_230_656, sha256: "f0d1", role: "model" }] };
  }
  if (rest === "/ingest/resolve") return () => RESOLVED;
  // 🔴 POST only. `GET …/ingest` (the job list) is gone with the history tab (ADR 0085 decision
  // 6), and a stub that answered it would let a screen read a route no deployment has.
  if (rest === "/ingest" && method === "POST") return () => ({ id: "job-9", model_id: PLAN.id, state: "pending", action: "download", created_at: new Date().toISOString() });
  // Dismissing a failed job is the one act left on one: the ledger drops the entry on the next
  // listing, so the answer carries nothing.
  if (/^\/ingest\/[^/]+$/.test(rest) && method === "DELETE") return () => ({});
  if (/^\/models\/[^/]+\/complete$/.test(rest)) {
    return (body) => body?.check
      ? {
        action: "choose", bytes_to_download: 0,
        files: [{
          flag: "--clip_l", action: "choose",
          candidates: [
            { key: "image/text_encoders/qwen_3_06b_base.safetensors", bytes: 1_190_000_000, source: "hf:circlestone-labs/Anima" },
            { key: "image/text_encoders/clip_l.safetensors", bytes: 246_144_152 },
          ],
        }],
      }
      : { action: "attached", files: [], bytes_to_download: 0 };
  }
  // ADR 0088: re-reading a model page for the name and the picture.
  if (/^\/models\/[^/]+\/meta$/.test(rest)) {
    return () => ({ id: "abyssorangemix2_hard_8832", display_name: "AbyssOrangeMix2",
      version_name: "Hard", preview_url: "/stub/preview/abyss-large.png",
      thumb_url: "/stub/preview/abyss.png", found: true });
  }
  if (/^\/models\/[^/]+$/.test(rest)) return () => ({});
  return null;
}

// ---- server: engine routes here, everything else proxied to the shell stub ----------
const shell = spawn(process.execPath, [
  path.resolve(HERE, "../shots/server.mjs"), "--port", String(SHELL_PORT), "--locale", LOCALE, "--admin",
], { stdio: ["ignore", "inherit", "inherit"] });
// 🔴 SIGTERM too, and explicitly: the default handler exits without running `exit` listeners, so
// a harness that kills this process leaves the shell server holding its port — and the next run
// silently reads the OLD bundle from it (measured, and it answers every route, so nothing fails).
const stopShell = () => { try { shell.kill(); } catch { /* already gone */ } };
process.on("exit", stopShell);
for (const signal of ["SIGINT", "SIGTERM", "SIGHUP"]) {
  process.on(signal, () => { stopShell(); process.exit(1); });
}

const server = http.createServer((req, res) => {
  const url = new URL(req.url, "http://127.0.0.1");
  if (url.pathname.startsWith("/stub/preview/")) {
    const body = swatchPNG(url.pathname, url.pathname.includes("-large") ? 512 : 128);
    res.writeHead(200, { "content-type": "image/png", "cache-control": "no-store" });
    res.end(body);
    return;
  }
  const handler = engineRoute(req.method, url.pathname);
  if (handler) {
    let raw = "";
    req.on("data", (chunk) => { raw += chunk; });
    req.on("end", () => {
      let body = null;
      try { body = raw ? JSON.parse(raw) : null; } catch { body = null; }
      res.writeHead(200, { "content-type": "application/json; charset=utf-8", "cache-control": "no-store" });
      res.end(JSON.stringify(handler(body)));
    });
    return;
  }
  const upstream = http.request({ host: "127.0.0.1", port: SHELL_PORT, method: req.method, path: req.url, headers: req.headers }, (answer) => {
    res.writeHead(answer.statusCode || 502, answer.headers);
    answer.pipe(res);
  });
  upstream.on("error", () => { res.writeHead(502); res.end("{}"); });
  req.pipe(upstream);
});

server.listen(PORT, "127.0.0.1", () => console.log(`[engine-catalog stub] http://127.0.0.1:${PORT}/ (shell on ${SHELL_PORT})`));
