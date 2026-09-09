// The self-hosted engine panel (ADR 0071).
//
// What is pinned here is the same distinction the VOICEVOX panel was audited for: the MODE is
// the administrator's intent and the STATE is what ECS is doing about it, and for the minute
// after "off" they disagree. A panel that echoed ECS back would report the opposite of the
// button just pressed.
//
// Plus the thing that is specific to this screen: a GPU box is $1.26/hour, so "always on" has
// to be visibly different from the other two rather than just another segment.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
// Only the transport is stubbed. errDetail is the REAL one, because how this panel words a
// refusal is part of what is under test: a hand-written stub that echoed `message` back would
// have reported the English developer text as a pass.
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));

import { EnginesAdminView } from "./adminEngines.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const row = (over: Record<string, unknown> = {}) => ({
  key: "image",
  api: "images",
  provider: "sdcpp",
  models: ["sdxl-base-1.0"],
  mode: "ondemand",
  enabled: true,
  managed: true,
  state: "stopped",
  desired: 0,
  ...over,
});

async function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<EnginesAdminView />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const seg = (label: string) =>
  Array.from(host!.querySelectorAll(".seg-btn")).find((b) => b.textContent === label) as
    | HTMLButtonElement
    | undefined;

const click = async (el: HTMLElement | undefined) => {
  expect(el).toBeTruthy();
  await act(async () => {
    el!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
  await act(async () => {
    await Promise.resolve();
  });
};

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  api.mockReset();
  apiJSON.mockReset();
  vi.useRealTimers();
});

describe("EnginesAdminView", () => {
  it("switches an engine off through the per-engine route", async () => {
    api.mockResolvedValue({ engines: [row()] });
    apiJSON.mockResolvedValue(row({ mode: "off", enabled: false, state: "stopping" }));
    await mount();

    await click(seg("無効"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image", "PUT", { mode: "off" });
    // The answer, not a re-fetch, is what the row becomes — the same reason the TTS panel
    // does it: a poll landing in between would show the pre-click state.
    expect(host!.textContent).toContain("停止処理中");
  });

  it("shows the mode the admin chose, not what ECS is still doing", async () => {
    // The disagreement window: mode off, task still going away.
    api.mockResolvedValue({ engines: [row({ mode: "off", enabled: false, state: "stopping" })] });
    await mount();
    expect(seg("無効")?.className).toContain("active");
    expect(seg("常時稼働")?.className).not.toContain("active");
  });

  it("warns about the bill only while an engine is pinned on", async () => {
    api.mockResolvedValue({ engines: [row({ mode: "ondemand" })] });
    await mount();
    expect(host!.textContent).not.toContain("$1.26");

    apiJSON.mockResolvedValue(row({ mode: "on", state: "running" }));
    await click(seg("常時稼働"));
    expect(host!.textContent).toContain("$1.26");
  });

  it("says so rather than showing an empty screen when nothing is deployed", async () => {
    api.mockResolvedValue({ engines: [] });
    await mount();
    expect(host!.querySelectorAll(".seg-btn").length).toBe(0);
    expect(host!.textContent).toContain("動かしていません");
  });

  // The status block, and the rule it is written to: a field the CP has no answer for is
  // ABSENT, and the panel must not fill the hole. Each assertion below is a hole that would
  // otherwise be filled with something an operator would act on.
  it("shows when the box started and when it will stop by itself", async () => {
    const now = Date.now();
    api.mockResolvedValue({
      engines: [
        row({
          state: "running",
          desired: 1,
          warm: true,
          // The BOX's own clock, not the service's: `service_since` moves on a stack update
          // without a new box being bought, and it is the box that costs $1.26/hour.
          box: { id: "i-08a9", status: "ACTIVE", since: new Date(now - 3720_000).toISOString() },
          service_since: new Date(now - 99_000_000).toISOString(),
          stop_eta: new Date(now + 600_000).toISOString(),
          window_secs: 300,
          window_counted_secs: 300,
          window_units: 3,
          last_demand: new Date(now - 120_000).toISOString(),
        }),
      ],
    });
    await mount();
    const text = host!.textContent || "";
    expect(text).toContain("i-08a9");
    expect(text).toContain("1 時間 2 分 経過"); // the box's age, not the service's
    expect(text).toContain("あと 10 分");
    expect(text).toContain("直近 5 分の要求: 3 件");
    expect(text).toContain("（読み込み済）");
    // The count covers the whole window, so it must NOT be qualified — a warning that is
    // always on is a warning nobody reads.
    expect(text).not.toContain("この CP が数えているのは");
  });

  it("does not promise a stop for an engine that will not stop", async () => {
    // Pinned on. The CP omits stop_eta, and the panel must not substitute anything for it —
    // not even the idle policy, which does not apply while the engine is pinned.
    api.mockResolvedValue({
      engines: [
        row({
          mode: "on",
          state: "running",
          desired: 1,
          idle_secs: 900,
          window_secs: 300,
          window_counted_secs: 300,
        }),
      ],
    });
    await mount();
    expect(host!.textContent).not.toContain("自動停止");
  });

  it("states the idle window for a stopped on-demand engine, as a policy and not a time", async () => {
    // Nothing to stop, so there is no countdown — but the window itself is still worth knowing,
    // and it is a different claim ("30 minutes after the last request") from a clock time.
    api.mockResolvedValue({
      engines: [row({ mode: "ondemand", state: "stopped", desired: 0, idle_secs: 900 })],
    });
    await mount();
    const text = host!.textContent || "";
    expect(text).toContain("誰も使わなくなってから 15 分で自動停止します");
    expect(text).not.toContain("あと");
  });

  // 🔴 The one number on this panel that can be confidently wrong. The rolling count lives in
  // the control plane's memory, so a CP replaced two minutes ago answers "0 requests in the
  // last 5 minutes" while somebody is mid-conversation with the engine.
  it("says so when it has not been counting for a whole window", async () => {
    api.mockResolvedValue({
      engines: [
        row({
          state: "running",
          desired: 1,
          window_secs: 300,
          window_counted_secs: 90,
          window_units: 0,
          last_demand: new Date(Date.now() - 60_000).toISOString(),
        }),
      ],
    });
    await mount();
    const text = host!.textContent || "";
    expect(text).toContain("直近 5 分の要求: 0 件");
    expect(text).toContain("この CP が数えているのは");
    // The last-request time is persisted, so it stays true across the restart the count did
    // not survive — which is what makes the 0 above readable rather than alarming.
    expect(text).toContain("最後の要求");
  });

  // The service events are the only place ECS writes down why a start failed, and an engine
  // stuck in `starting` is exactly when somebody needs them.
  it("shows why a start is stuck, only while it is stuck", async () => {
    api.mockResolvedValue({
      engines: [
        row({
          state: "starting",
          desired: 1,
          events: ["(service af-image) was unable to place a task because no container instance met all of its requirements."],
        }),
      ],
    });
    await mount();
    expect(host!.textContent).toContain("no container instance met all of its requirements");
  });

  // <details> hides its children, it does not unmount them. Leaving the heatmap inside a closed
  // one fires a 14-day query per engine on load, for a section nobody opened.
  it("does not fetch the history until the section is opened", async () => {
    api.mockResolvedValue({ engines: [row(), row({ key: "llm" })] });
    await mount();
    const called = api.mock.calls.map((c) => String(c[0]));
    // The heatmap is a 14-day query per engine and nothing on screen shows it yet.
    expect(called.filter((p) => p.includes("/hourly"))).toEqual([]);
    // The ingest list IS fetched, once per engine: a download started before lunch has to be
    // visible on the panel that is opened after it, and the call is a cheap read that also
    // reconciles a finished job.
    expect(called.sort()).toEqual([
      "api/admin/engines",
      "api/admin/engines/image/ingest",
      "api/admin/engines/llm/ingest",
    ]);
  });

  it("lists every engine, each with its own control", async () => {
    api.mockResolvedValue({
      engines: [
        row({ key: "llm", api: "chat", provider: "llamacpp", models: ["qwen3-coder-30b-a3b"] }),
        row(),
      ],
    });
    await mount();
    expect(host!.querySelectorAll(".admin-panel").length).toBe(2);
    // Three segments each: off / on-demand / always-on.
    expect(host!.querySelectorAll(".seg-btn").length).toBe(6);
    expect(host!.textContent).toContain("qwen3-coder-30b-a3b");
    expect(host!.textContent).toContain("sdxl-base-1.0");
  });

  // --- the model catalogue (ADR 0072) ---------------------------------------

  // "may this deployment use it" and "this is the one the engine starts with" are different
  // questions, and the panel has to offer both: sd-server holds ONE checkpoint chosen by a
  // startup flag, so enabling a second one does not load it.
  it("selects a checkpoint through the model route, without restarting anything", async () => {
    api.mockResolvedValue({
      engines: [
        row({
          has_models: true,
          model_rows: [
            { id: "sdxl-base-1.0", kind: "checkpoint", enabled: true, selected: true },
            { id: "sdxl-fine-tune", kind: "checkpoint", enabled: true },
          ],
        }),
      ],
    });
    apiJSON.mockResolvedValue(
      row({
        has_models: true,
        model_rows: [
          { id: "sdxl-base-1.0", kind: "checkpoint", enabled: true },
          { id: "sdxl-fine-tune", kind: "checkpoint", enabled: true, selected: true },
        ],
      }),
    );
    await mount();
    // Only the model that is NOT already the one started with offers the control.
    const select = Array.from(host!.querySelectorAll(".engines-model button")).filter(
      (b) => b.textContent === "これで起動する",
    );
    expect(select.length).toBe(1);
    await click(select[0] as HTMLElement);
    expect(apiJSON).toHaveBeenCalledWith(
      "api/admin/engines/image/models/sdxl-fine-tune",
      "PUT",
      { selected: true },
    );
    // ⚠️ The panel must say that a running engine is not swapped. Without it an administrator
    // presses this mid-generation expecting an immediate change (ADR 0072 decision 4).
    expect(host!.textContent).toContain("次の起動から効きます");
  });

  // The llm role's equivalent is the model a request that named none gets, so the same button
  // sends a different field. Getting this wrong would set `selected` on an engine that has no
  // such concept and silently change nothing.
  it("sends default, not selected, for a chat engine", async () => {
    api.mockResolvedValue({
      engines: [
        row({
          key: "llm",
          api: "chat",
          provider: "llamacpp",
          has_models: true,
          model_rows: [{ id: "qwen3", kind: "gguf", enabled: true }],
        }),
      ],
    });
    apiJSON.mockResolvedValue(row({ key: "llm", api: "chat", has_models: true, model_rows: [] }));
    await mount();
    const select = Array.from(host!.querySelectorAll(".engines-model button")).find(
      (b) => b.textContent === "これで起動する",
    );
    await click(select as HTMLElement);
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/llm/models/qwen3", "PUT", {
      default: true,
    });
  });

  // A disabled model stays on the panel — this is the only place it can be turned back on.
  it("keeps a disabled model reachable and offers to enable it", async () => {
    api.mockResolvedValue({
      engines: [
        row({
          has_models: true,
          model_rows: [
            { id: "sdxl-base-1.0", kind: "checkpoint", enabled: true, selected: true },
            { id: "parked", kind: "checkpoint", enabled: false },
          ],
        }),
      ],
    });
    apiJSON.mockResolvedValue(row({ has_models: true, model_rows: [] }));
    await mount();
    expect(host!.textContent).toContain("parked");
    const enable = Array.from(host!.querySelectorAll(".engines-model button")).find(
      (b) => b.textContent === "有効にする",
    );
    await click(enable as HTMLElement);
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/models/parked", "PUT", {
      enabled: true,
    });
  });

  // A LoRA is never something an engine is started with, so the control that would say so is
  // not offered — the check the Agent also makes when it builds the tool's enum.
  it("does not offer to start with a LoRA", async () => {
    api.mockResolvedValue({
      engines: [
        row({
          has_models: true,
          model_rows: [
            { id: "sdxl-base-1.0", kind: "checkpoint", enabled: true, selected: true },
            { id: "watercolour", kind: "lora", enabled: true, base_model: "sdxl" },
          ],
        }),
      ],
    });
    await mount();
    const select = Array.from(host!.querySelectorAll(".engines-model button")).filter(
      (b) => b.textContent === "これで起動する",
    );
    expect(select.length).toBe(0);
    expect(host!.textContent).toContain("LoRA");
  });

  // An empty catalogue is the reason the controller refuses to start the engine, so it gets a
  // sentence. A blank area here reads as "still loading" and an administrator waits for a box
  // that is never coming.
  it("says why an engine with no catalogue will not start", async () => {
    api.mockResolvedValue({ engines: [row({ has_models: false, model_rows: [] })] });
    await mount();
    expect(host!.textContent).toContain("カタログは空です");
  });

  it("says when models exist but none is enabled", async () => {
    api.mockResolvedValue({
      engines: [
        row({ has_models: false, model_rows: [{ id: "parked", kind: "checkpoint", enabled: false }] }),
      ],
    });
    await mount();
    expect(host!.textContent).toContain("有効なモデルがありません");
  });

  // P0's definition of done is "switch the image checkpoint to another one without touching
  // CloudFormation", and the seed creates exactly ONE row per role — so there has to be a way
  // to add the second. This is not P4's ingest: it writes down a file that is already staged.
  it("registers a staged file as a catalogue row, disabled", async () => {
    api.mockResolvedValue({
      engines: [
        row({
          has_models: true,
          model_rows: [{ id: "sdxl-base-1.0", kind: "checkpoint", enabled: true, selected: true }],
        }),
      ],
    });
    apiJSON.mockResolvedValue(row({ has_models: true, model_rows: [] }));
    await mount();
    const open = Array.from(host!.querySelectorAll("button")).find(
      (b) => b.textContent === "バケットのファイルを登録する",
    );
    await click(open as HTMLElement);

    // id, key, description, size. The WINDOW fields are chat-only, and this is the image role.
    const inputs = Array.from(host!.querySelectorAll(".engines-model-add input"));
    expect(inputs.length).toBe(4);
    const type = async (el: Element, v: string) => {
      await act(async () => {
        const setter = Object.getOwnPropertyDescriptor(
          HTMLInputElement.prototype,
          "value",
        )!.set!;
        setter.call(el, v);
        el.dispatchEvent(new Event("input", { bubbles: true }));
      });
    };
    await type(inputs[0], "juggernaut-xl-v9");
    await type(inputs[1], "image/checkpoints/juggernaut_xl_v9.safetensors");
    await type(inputs[2], "a photographic SDXL fine-tune");

    // ⚠️ The form must not imply the key was checked. The CP holds no S3 permission at all
    // (ADR 0072 review R3), so a typo only surfaces at the next cold start. Asserted while the
    // form is open, because submitting closes it.
    expect(host!.textContent).toContain("CP は S3 を見ません");

    const go = Array.from(host!.querySelectorAll(".engines-model-add button")).find(
      (b) => b.textContent === "登録する",
    );
    await click(go as HTMLElement);
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/models", "POST", {
      id: "juggernaut-xl-v9",
      kind: "checkpoint",
      files: [{ s3Key: "image/checkpoints/juggernaut_xl_v9.safetensors", bytes: 0 }],
      description: "a photographic SDXL fine-tune",
      context_tokens: 0,
      max_output_tokens: 0,
    });
  });

  // "Forget" is the row, not the file: the CP has no s3:DeleteObject. The one the engine starts
  // with cannot be forgotten, or the role is left with no checkpoint at all.
  it("forgets a row, and refuses to forget the one in use", async () => {
    api.mockResolvedValue({
      engines: [
        row({
          has_models: true,
          model_rows: [
            { id: "sdxl-base-1.0", kind: "checkpoint", enabled: true, selected: true },
            { id: "parked", kind: "checkpoint", enabled: false },
          ],
        }),
      ],
    });
    apiJSON.mockResolvedValue(row({ has_models: true, model_rows: [] }));
    await mount();
    const forget = Array.from(host!.querySelectorAll(".engines-model")).map((li) =>
      Array.from(li.querySelectorAll("button")).find((b) => b.textContent === "登録を消す"),
    );
    expect((forget[0] as HTMLButtonElement).disabled).toBe(true);
    expect((forget[1] as HTMLButtonElement).disabled).toBe(false);
    await click(forget[1] as HTMLElement);
    expect(apiJSON).toHaveBeenCalledWith(
      "api/admin/engines/image/models/parked",
      "DELETE",
      undefined,
    );
  });

  // ADR 0072 P1. A router role holds ONE model at a time, so two sessions on two models take
  // turns and every turn costs an unload plus 267 s of weights. The panel is where that price is
  // stated: "warm" alone describes a swapping engine and a settled one identically.
  it("names the model in VRAM and how often it has changed", async () => {
    api.mockResolvedValue({
      engines: [
        row({
          key: "llm",
          api: "chat",
          provider: "llamacpp",
          state: "running",
          desired: 1,
          warm: true,
          warm_model: "qwen2.5-coder-1.5b",
          model_swaps: 3,
          has_models: true,
          model_rows: [
            { id: "qwen3-coder-30b-a3b", kind: "gguf", enabled: true, default: true, sync_secs: 179 },
            { id: "qwen2.5-coder-1.5b", kind: "gguf", enabled: true, sync_secs: 11 },
          ],
        }),
      ],
    });
    await mount();
    expect(host!.textContent).toContain("qwen2.5-coder-1.5b");
    expect(host!.textContent).toContain("モデル交替: 3 回");
    // The cold-start cost of each model, next to the toggle that adds it — and marked as an
    // estimate, because the control plane has never looked at the bucket.
    expect(host!.textContent).toContain("同期 +179 秒（推定）");
    expect(host!.textContent).toContain("同期 +11 秒（推定）");
  });

  // Nobody declared the sizes (every row the seed makes, and every row registered before the
  // field existed). "+0 s" would be a claim; nothing is the truth.
  it("says nothing about the sync when no size was declared", async () => {
    api.mockResolvedValue({
      engines: [
        row({ has_models: true, model_rows: [{ id: "sdxl-base-1.0", kind: "checkpoint", enabled: true }] }),
      ],
    });
    await mount();
    expect(host!.textContent).not.toContain("同期 +");
  });

  // ADR 0072 P1: a chat engine's row carries its OWN window, and this form is the only way to
  // declare one until P4's ingest reads it off the model card. A model registered without one
  // reaches opencode as context 0 — which switches auto-compaction off.
  it("declares a window and a size when registering a model for a chat engine", async () => {
    api.mockResolvedValue({
      engines: [
        row({
          key: "llm",
          api: "chat",
          provider: "llamacpp",
          has_models: true,
          model_rows: [{ id: "qwen3-coder-30b-a3b", kind: "gguf", enabled: true, default: true }],
        }),
      ],
    });
    apiJSON.mockResolvedValue(row({ key: "llm", api: "chat", has_models: true, model_rows: [] }));
    await mount();
    await click(
      Array.from(host!.querySelectorAll("button")).find(
        (b) => b.textContent === "バケットのファイルを登録する",
      ) as HTMLElement,
    );
    // One field per ROW, each with its own label: six labelled rows, not a strip of six
    // look-alike boxes whose placeholder captions vanish as soon as somebody types into them.
    const rows = Array.from(host!.querySelectorAll(".engines-model-add-row"));
    expect(rows.length).toBe(6);
    expect(rows.every((r) => r.querySelector("span") && r.querySelector("input"))).toBe(true);
    const inputs = Array.from(host!.querySelectorAll(".engines-model-add input"));
    expect(inputs.length).toBe(6);
    const type = async (el: Element, v: string) => {
      await act(async () => {
        const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
        setter.call(el, v);
        el.dispatchEvent(new Event("input", { bubbles: true }));
      });
    };
    await type(inputs[0], "qwen2.5-coder-1.5b");
    await type(inputs[1], "llm/qwen2.5-coder-1.5b-instruct-q4_k_m.gguf");
    await type(inputs[2], "small and quick");
    await type(inputs[3], "32768");
    await type(inputs[4], "4096");
    await type(inputs[5], "1117320768");
    await click(
      Array.from(host!.querySelectorAll(".engines-model-add button")).find(
        (b) => b.textContent === "登録する",
      ) as HTMLElement,
    );
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/llm/models", "POST", {
      id: "qwen2.5-coder-1.5b",
      kind: "gguf",
      files: [{ s3Key: "llm/qwen2.5-coder-1.5b-instruct-q4_k_m.gguf", bytes: 1117320768 }],
      description: "small and quick",
      context_tokens: 32768,
      max_output_tokens: 4096,
    });
  });

  // Half a window is worse than none: opencode reads an output cap of 0 as 32,000, so a 32k
  // context declared alone leaves 768 usable tokens. Both halves or neither.
  it("drops a context declared without an output cap", async () => {
    api.mockResolvedValue({
      engines: [row({ key: "llm", api: "chat", has_models: true, model_rows: [] })],
    });
    apiJSON.mockResolvedValue(row({ key: "llm", api: "chat", has_models: true, model_rows: [] }));
    await mount();
    await click(
      Array.from(host!.querySelectorAll("button")).find(
        (b) => b.textContent === "バケットのファイルを登録する",
      ) as HTMLElement,
    );
    const inputs = Array.from(host!.querySelectorAll(".engines-model-add input"));
    const type = async (el: Element, v: string) => {
      await act(async () => {
        const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
        setter.call(el, v);
        el.dispatchEvent(new Event("input", { bubbles: true }));
      });
    };
    await type(inputs[0], "half-declared");
    await type(inputs[1], "llm/x.gguf");
    await type(inputs[3], "32768");
    await click(
      Array.from(host!.querySelectorAll(".engines-model-add button")).find(
        (b) => b.textContent === "登録する",
      ) as HTMLElement,
    );
    const body = apiJSON.mock.calls.at(-1)![2] as Record<string, number>;
    expect(body.context_tokens).toBe(0);
    expect(body.max_output_tokens).toBe(0);
  });

  // ADR 0072 decision 6 / 10. The order is the whole point: look the source up, SEE the licence
  // and the gating, and only then is there a checkbox to accept it — an acceptance offered
  // before the terms is not one.
  it("shows the licence before offering to accept it, and refuses a gated repo with no token", async () => {
    api.mockResolvedValue({
      engines: [row({ key: "llm", api: "chat", has_models: true, model_rows: [] })],
    });
    await mount();
    await click(
      Array.from(host!.querySelectorAll("button")).find(
        (b) => b.textContent === "Hugging Face などから取り込む",
      ) as HTMLElement,
    );
    const type = async (el: Element, v: string) => {
      await act(async () => {
        const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
        setter.call(el, v);
        el.dispatchEvent(new Event("input", { bubbles: true }));
      });
    };
    const inputs = Array.from(host!.querySelectorAll(".engines-ingest input"));
    await type(inputs[0], "black-forest-labs/FLUX.1-dev");
    await type(inputs[1], "flux1-dev.safetensors");
    await type(inputs[2], "flux1-dev");

    // Nothing to accept yet: the source has not been read.
    expect(host!.querySelector(".engines-ingest-accept")).toBe(null);

    apiJSON.mockResolvedValueOnce({
      sha256: "4610115bb0c89560703c892c59ac2742fa821e60ef5871b33493ba544683abd7",
      bytes: 23802932552,
      gated: true,
      license: "other",
      license_name: "flux-1-dev-non-commercial-license",
      commercial_use: "no",
      can_ingest: false,
    });
    await click(
      Array.from(host!.querySelectorAll(".engines-ingest button")).find(
        (b) => b.textContent === "調べる",
      ) as HTMLElement,
    );
    // The two licence fields, the size and both warnings — the non-commercial one because the
    // deployment may be charging, the gated one because it cannot be fetched at all here.
    expect(host!.textContent).toContain("flux-1-dev-non-commercial-license");
    expect(host!.textContent).toContain("23.8 GB");
    expect(host!.textContent).toContain("非商用ライセンス");
    expect(host!.textContent).toContain("トークンがありません");
    // 🔴 And the acceptance is unusable: pressing on would spend a Fargate task to earn a 401.
    const box = host!.querySelector(".engines-ingest-accept input") as HTMLInputElement;
    expect(box.disabled).toBe(true);
    const go = Array.from(host!.querySelectorAll(".engines-ingest button")).find(
      (b) => b.textContent === "取り込む",
    ) as HTMLButtonElement;
    expect(go.disabled).toBe(true);
  });

  it("starts an ingest once the licence is accepted, and shows the job", async () => {
    api.mockImplementation(async (p: string) =>
      p.endsWith("/ingest")
        ? { jobs: [] }
        : { engines: [row({ key: "llm", api: "chat", has_models: true, model_rows: [] })] },
    );
    await mount();
    await click(
      Array.from(host!.querySelectorAll("button")).find(
        (b) => b.textContent === "Hugging Face などから取り込む",
      ) as HTMLElement,
    );
    const type = async (el: Element, v: string) => {
      await act(async () => {
        const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
        setter.call(el, v);
        el.dispatchEvent(new Event("input", { bubbles: true }));
      });
    };
    const inputs = Array.from(host!.querySelectorAll(".engines-ingest input"));
    await type(inputs[0], "Qwen/Qwen2.5-Coder-1.5B-Instruct-GGUF");
    await type(inputs[1], "qwen2.5-coder-1.5b-instruct-q4_k_m.gguf");
    await type(inputs[2], "qwen2.5-coder-1.5b");
    await type(inputs[4], "32768");
    await type(inputs[5], "4096");

    apiJSON.mockResolvedValueOnce({ sha256: "cc32", bytes: 1117320768, gated: false,
      license: "apache-2.0", commercial_use: "yes", can_ingest: true });
    await click(
      Array.from(host!.querySelectorAll(".engines-ingest button")).find(
        (b) => b.textContent === "調べる",
      ) as HTMLElement,
    );
    await act(async () => {
      const box = host!.querySelector(".engines-ingest-accept input") as HTMLInputElement;
      box.click();
    });
    apiJSON.mockResolvedValueOnce({ id: "j1", model_id: "qwen2.5-coder-1.5b", state: "running" });
    // After starting, the panel re-reads the job list — which is how a running download appears.
    api.mockImplementation(async (p: string) =>
      p.endsWith("/ingest")
        ? { jobs: [{ id: "j1", model_id: "qwen2.5-coder-1.5b", state: "running", source: "hf:Qwen/…", bytes: 1117320768 }] }
        : { engines: [row({ key: "llm", api: "chat", has_models: true, model_rows: [] })] },
    );
    await click(
      Array.from(host!.querySelectorAll(".engines-ingest button")).find(
        (b) => b.textContent === "取り込む",
      ) as HTMLElement,
    );
    const body = apiJSON.mock.calls.at(-1)!;
    expect(String(body[0])).toBe("api/admin/engines/llm/ingest");
    expect(body[2]).toMatchObject({
      id: "qwen2.5-coder-1.5b",
      kind: "gguf",
      s3Key: "llm/qwen2.5-coder-1.5b-instruct-q4_k_m.gguf",
      context_tokens: 32768,
      max_output_tokens: 4096,
      license_accepted: true,
      source: { hf: { repo: "Qwen/Qwen2.5-Coder-1.5B-Instruct-GGUF", file: "qwen2.5-coder-1.5b-instruct-q4_k_m.gguf", revision: "" } },
    });
    expect(host!.textContent).toContain("取り込み中");
  });

  // 🔴 Measured on the dev deployment (2026-09-09): a filename typed one letter short answered
  // "the repository does not list flux1-dev.safetensor" — the CP's developer message, in
  // English, on a Japanese screen. Every code this panel can raise was a string literal in
  // engine_ingest.go, and the catalogue gate reads only the constants in errcodes.go, so none
  // of them had ever been checked for a translation.
  //
  // Both halves are pinned here: the sentence has to be Japanese, and the file the CP named has
  // to survive into it. Translating alone would have answered "そのリポジトリにそのファイルが
  // ありません" over a form with no way to tell WHICH file was wrong.
  it("says why an ingest was refused in the user's language, without losing the detail", async () => {
    api.mockImplementation(async (p: string) =>
      p.endsWith("/ingest")
        ? { jobs: [] }
        : { engines: [row({ key: "image", has_models: true, model_rows: [] })] },
    );
    await mount();
    await click(
      Array.from(host!.querySelectorAll("button")).find(
        (b) => b.textContent === "Hugging Face などから取り込む",
      ) as HTMLElement,
    );
    await act(async () => {
      const el = host!.querySelector(".engines-ingest input")!;
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      setter.call(el, "black-forest-labs/FLUX.1-dev");
      el.dispatchEvent(new Event("input", { bubbles: true }));
    });
    apiJSON.mockResolvedValueOnce({
      error: { code: "file_unknown", message: "the repository does not list flux1-dev.safetensor" },
    });
    await click(
      Array.from(host!.querySelectorAll(".engines-ingest button")).find(
        (b) => b.textContent === "調べる",
      ) as HTMLElement,
    );
    const shown = host!.querySelector(".engines-ingest .form-err")!.textContent!;
    expect(shown).toContain("そのリポジトリにそのファイルがありません");
    expect(shown).toContain("flux1-dev.safetensor");
  });

  // 🔴 Measured on the dev deployment (2026-09-09): the ingest finished in 72 seconds, the job
  // said 完了 — and the model list went on showing the two rows it already had. The row an
  // ingest creates is disabled by design, so it is precisely the row an administrator came here
  // to switch on, and it was reachable only by pressing refresh. Nothing else re-reads it: the
  // job poll reads only the job list, and the engine poll is off because an on-demand engine
  // parked at "stopped" is a settled state.
  it("re-reads the catalogue when an ingest finishes, so the new disabled row appears", async () => {
    vi.useFakeTimers();
    const catalogue = (extra: Record<string, unknown>[]) =>
      row({
        key: "llm",
        api: "chat",
        has_models: true,
        model_rows: [{ id: "qwen2.5-coder-1.5b", enabled: true }, ...extra],
      });
    let finished = false;
    api.mockImplementation(async (p: string) =>
      p.endsWith("/ingest")
        ? {
            jobs: [
              {
                id: "j1",
                model_id: "qwen2.5-coder-0.5b",
                state: finished ? "done" : "running",
                source: "hf:Qwen/Qwen2.5-Coder-0.5B-Instruct-GGUF/…",
                bytes: 491400064,
              },
            ],
          }
        : {
            engines: [
              catalogue(finished ? [{ id: "qwen2.5-coder-0.5b", enabled: false }] : []),
            ],
          },
    );
    await mount();

    // The catalogue rows only — the job list is the other <ul> and it names the model too, so
    // asserting on the panel's whole text would pass with the bug still in place.
    const catalogueIds = () =>
      Array.from(
        host!.querySelectorAll("ul.engines-model-list:not(.engines-ingest-jobs) .mono"),
      ).map((n) => n.textContent);
    expect(catalogueIds()).toEqual(["qwen2.5-coder-1.5b"]);
    expect(host!.textContent).toContain("取り込み中");

    finished = true;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10000);
    });

    expect(host!.textContent).toContain("完了");
    expect(catalogueIds()).toEqual(["qwen2.5-coder-1.5b", "qwen2.5-coder-0.5b"]);
    // Disabled, so what it offers is the switch-on — decision 6: taken in is not the same as
    // on offer.
    const fresh = Array.from(host!.querySelectorAll("li.engines-model")).find(
      (li) => li.querySelector(".mono")?.textContent === "qwen2.5-coder-0.5b",
    )!;
    expect(fresh.className).not.toContain("on");
    expect(fresh.textContent).toContain("有効にする");
  });
});
