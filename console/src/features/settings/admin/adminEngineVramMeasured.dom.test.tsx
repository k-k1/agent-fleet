// 実測 VRAM の導線（ADR 0094 決定 8）。押さえるのは 3 つ。
//
//   1. 測った族の行を編集すると、測定値と測定条件が出て、1 押しで欄に入る、
//   2. 入るだけで、押さなければ何も変わらない——機械は vram_mib を書かない、
//   3. 測っていない族には何も出ない（対照）。
//
// 🔴 3 が無いと 1 は「常に何か出ている」でも緑になる。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));

import { EngineAddView } from "./adminEngineAdd.tsx";
import { clearCatalogMemory } from "./catalogMemory.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const qwen = {
  id: "zz-qwen-image-edit-2509",
  kind: "checkpoint",
  enabled: false,
  base_model: "qwen-image-edit-2509",
  vram_mib: 0,
  file_rows: [
    { flag: "--diffusion-model", s3Key: "image/diffusion_models/qwen_image_edit_2509_fp8_e4m3fn.safetensors", bytes: 21939677184 },
    { flag: "--clip_l", s3Key: "image/text_encoders/qwen_2.5_vl_7b_fp8_scaled.safetensors", bytes: 9384670680 },
    { flag: "--vae", s3Key: "image/vae/qwen_image_vae.safetensors", bytes: 253806246 },
  ],
};

const sdxl = { id: "abyssorangemix2_hard_8832", kind: "checkpoint", enabled: true, base_model: "sdxl" };

// 同じ族の別ビルド。bf16 は 40.86 GB で L4 に載らないので、fp8 の 20,862 を入れられると
// 梯子が L4 の段を通して OOM になる（`vram_mib` は合計の下限を上書きする）。
const qwenBf16 = {
  id: "zz-qwen-image-edit-2509-bf16",
  kind: "checkpoint", enabled: false, base_model: "qwen-image-edit-2509", vram_mib: 0,
  file_rows: [{ flag: "--diffusion-model", s3Key: "image/diffusion_models/qwen_image_edit_2509_bf16.safetensors", bytes: 43876536320 }],
};

const imageRow = {
  key: "image",
  api: "images",
  provider: "comfy",
  managed: true,
  base_models: ["sdxl", "qwen-image-edit-2509", "qwen-image-edit-2511"],
  class: { id: "g6", label: "L4 24GB", vram_mib: 22000, types: ["g6.xlarge"] },
  classes: [{ id: "g6", label: "L4 24GB", vram_mib: 22000, types: ["g6.xlarge"] }],
  model_rows: [qwen, sdxl, qwenBf16],
};

function mockEngineAPI() {
  api.mockImplementation((path: string) => {
    if (path === "api/admin/engines") return Promise.resolve({ super_admin: true, engines: [imageRow] });
    if (path.endsWith("/objects")) return Promise.resolve({ objects: [] });
    return Promise.resolve({});
  });
}

async function flush(turns = 4) {
  for (let i = 0; i < turns; i += 1) {
    await act(async () => { await Promise.resolve(); });
  }
}

async function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => { root!.render(<EngineAddView engineKey="image" lora={false} initialView="registered" />); });
  await flush();
}

const labelled = (label: string) => Array.from(document.querySelectorAll<HTMLButtonElement>("button"))
  .find((candidate) => candidate.getAttribute("aria-label") === label);

const button = (text: string) => Array.from(document.querySelectorAll<HTMLButtonElement>("button"))
  .find((candidate) => candidate.textContent === text);

const hint = () => document.querySelector<HTMLElement>(".engine-operation-vram-measured");

const vramField = () => Array.from(document.querySelectorAll<HTMLLabelElement>(".engine-registered-edit label"))
  .find((label) => label.querySelector("span")?.textContent === "実測 VRAM（MiB）")
  ?.querySelector<HTMLInputElement>("input");

async function click(element: HTMLElement | undefined) {
  expect(element).toBeTruthy();
  await act(async () => { element!.dispatchEvent(new MouseEvent("click", { bubbles: true })); });
  await flush();
}

async function openEdit(id: string) {
  await click(labelled(`編集: ${id}`));
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  mockEngineAPI();
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  clearCatalogMemory();
  root = null;
  host = null;
  api.mockReset();
  apiJSON.mockReset();
  delete (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT;
});

describe("行の編集で実測 VRAM を勧める", () => {
  it("測定値と測定条件を出し、1 押しで欄に入る", async () => {
    await mount();
    await openEdit(qwen.id);

    // 数字だけでは比べようがない——寸法・batch・参照枚数まで含めて 1 文。
    expect(hint()?.textContent).toContain("20,862 MiB");
    expect(hint()?.textContent).toContain("1024x1024");
    expect(hint()?.textContent).toContain("batch 1");

    // 押すまでは 0 のまま（機械は書かない）。
    expect(vramField()?.value).toBe("0");
    await click(button("実測値 20,862 を入れる"));
    expect(vramField()?.value).toBe("20862");
  });

  it("保存で初めて行に載る（PUT の本体に測定値が入る）", async () => {
    apiJSON.mockResolvedValue({});
    await mount();
    await openEdit(qwen.id);
    await click(button("実測値 20,862 を入れる"));
    await click(button("保存"));

    const put = apiJSON.mock.calls.find((call) => call[1] === "PUT");
    expect(put).toBeTruthy();
    expect((put![2] as { vram_mib: number }).vram_mib).toBe(20862);
  });

  // 🔴 対照。誰も測っていない族で同じ行が出るなら、上の 2 つは何も測っていない。
  it("測っていない族には何も出ない", async () => {
    await mount();
    await openEdit(sdxl.id);
    expect(hint()).toBeNull();
    expect(vramField()).toBeTruthy();
  });

  // 🔴 同じ族でも、測ったのと別のビルドには出さない。ここで出すと、fp8 で測った 20,862 を
  // 40.86 GB の bf16 の行に入れられる——梯子は L4 の段を通し、読み込みは OOM で落ちる。
  it("同じ族の別ビルドには出さない", async () => {
    await mount();
    await openEdit(qwenBf16.id);
    expect(hint()).toBeNull();
    expect(vramField()?.value).toBe("0");
  });
});

// 取り込みの画面（ADR 0094 未解決 5: 直後に要求されない限り忘れられる）。ここでは押させない
// ——押す欄はこの経路に無く、あったら機械が vram_mib を書く経路になる。
describe("取り込みの画面で実測 VRAM を先に言う", () => {
  async function mountSearch() {
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    await act(async () => { root!.render(<EngineAddView engineKey="image" lora={false} />); });
    await flush();
  }

  function mockIngest(family: string) {
    apiJSON.mockImplementation((path: string, _method?: string, body?: Record<string, unknown>) => {
      if (path.endsWith("/ingest/search")) {
        return Promise.resolve({ hits: [{ source: "hf", ref: "Comfy-Org/Qwen-Image-Edit_ComfyUI", model_ref: "Comfy-Org/Qwen-Image-Edit_ComfyUI", name: "Qwen-Image-Edit" }] });
      }
      if (path.endsWith("/ingest/versions")) return Promise.resolve({ versions: [{ ref: "main", name: "main" }] });
      if (path.endsWith("/ingest/files")) return Promise.resolve({ files: [{ name: "split_files/diffusion_models/qwen_image_edit_2509_fp8_e4m3fn.safetensors" }] });
      if (path.endsWith("/ingest/resolve")) {
        return Promise.resolve({
          bytes: 21_939_677_184, can_ingest: true,
          plan: {
            plan_token: "plan-q", id: "qwen-image-edit-2509", base_model: family, main_flag: "--diffusion-model",
            files: [
              { flag: "--diffusion-model", name: "qwen_image_edit_2509_fp8_e4m3fn.safetensors", bytes: 21_939_677_184, action: "download" },
              { flag: "--clip_l", name: "qwen_2.5_vl_7b_fp8_scaled.safetensors", bytes: 9_384_670_680, action: "download" },
              { flag: "--vae", name: "qwen_image_vae.safetensors", bytes: 253_806_246, action: "reuse" },
            ],
            bytes_to_download: 31_324_347_864,
          },
        });
      }
      if (path.endsWith("/ingest")) return Promise.resolve({ id: "job1", model_id: "qwen-image-edit-2509", state: "pending", action: "download", body });
      return Promise.resolve({});
    });
  }

  async function openPlan() {
    await click(button("追加"));
    await flush();
  }

  it("ファイルの合計の下に実測値を出し、入れる場所を言う", async () => {
    mockIngest("qwen-image-edit-2509");
    await mountSearch();
    await openPlan();

    // 合計（この族では 29,882 MiB 前後）と実測（20,862 MiB）の両方が、この順で出る。
    expect(document.querySelector(".engine-operation-fit")?.textContent).toContain("MiB");
    expect(hint()?.textContent).toContain("20,862 MiB");
    expect(hint()?.textContent).toContain("編集");
  });

  // 🔴 決定 8 の本体。取り込みの本体に vram_mib が乗った時点で、その欄の意味が
  // 「運用者が測った数字」から「機械が書いた数字」に変わる。
  it("取り込みの本体に vram_mib は入らない", async () => {
    mockIngest("qwen-image-edit-2509");
    await mountSearch();
    await openPlan();

    const licence = Array.from(document.querySelectorAll<HTMLInputElement>('input[type="checkbox"]'))
      .find((input) => input.parentElement?.textContent?.includes("ライセンス"))!;
    await act(async () => { licence.click(); });
    await click(button("取り込む"));

    const press = apiJSON.mock.calls.find((call) => String(call[0]).endsWith("/ingest") && call[1] === "POST");
    expect(press).toBeTruthy();
    expect(press![2]).not.toHaveProperty("vram_mib");
  });

  it("測っていない族の取り込みでは何も出ない", async () => {
    mockIngest("sdxl");
    await mountSearch();
    await openPlan();

    expect(document.querySelector(".engine-operation-fit")).toBeTruthy();
    expect(hint()).toBeNull();
  });
});
