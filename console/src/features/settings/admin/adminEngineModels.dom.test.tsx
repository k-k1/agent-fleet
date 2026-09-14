// The model half of the engine panel (ADR 0072): the catalogue this engine holds, its rows and
// what may be changed on them, and the history of the ingests that filled it.
//
// Taking a file IN — the upstream search, the licence, the four questions — is the catalogue
// pane's own screen and is tested in adminEngineCatalog.dom.test.tsx. This panel only offers the
// door to it.
//
// The machine half — mode, state, the GPU ladder, the uptime — is adminEngines.dom.test.tsx.
// Both screens read the same `GET /api/admin/engines` answer, which is why the row factory and
// the mount helper below are the same in both files: what differs is which component is
// rendered against them.
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

import {
  EngineModelsAdminView,
  engineIdFromFile,
  engineJobAdvice,
} from "./adminEngineModels.tsx";

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
    root!.render(<EngineModelsAdminView />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

/** The screen's own tabs: the role (only when there is more than one engine) and model / LoRA.
 *  A helper because the LoRA half of every form is now reached by pressing one — the kind is no
 *  longer a field inside the form, which is what stopped the list and the form disagreeing. */
const tab = (label: string) =>
  Array.from(ui().querySelectorAll(".seg-btn")).find((b) => b.textContent === label) as
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

/** What the tests read. 🔴 The document and not the mount point: 「モデルを追加」 is a DIALOG,
 *  and React portals a dialog to <body> (a transformed ancestor would otherwise become the
 *  containing block for its fixed position). A query scoped to `host` sees the panel and not the
 *  screen on top of it. */
const ui = () => document.body;

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  api.mockReset();
  apiJSON.mockReset();
  vi.useRealTimers();
});
// The GPU ladder, as a row carries it. One test here needs it — the VRAM confirmation before
// enabling a model the chosen card cannot hold — and that question belongs to the catalogue
// rather than to the ladder: it is raised where the numbers are, in front of the button that
// would load the weights (ADR 0074 decision 6).
const withClasses = (over: Record<string, unknown> = {}) =>
  row({
    classes: [
      { id: "l4", label: "L4 24GB", vram_mib: 21000, types: ["g6.xlarge"], usd_per_hour: 1.26 },
      { id: "l40s", label: "L40S 48GB", vram_mib: 44000, types: ["g6e.xlarge"] },
    ],
    class: { id: "l4", label: "L4 24GB", vram_mib: 21000, types: ["g6.xlarge"], usd_per_hour: 1.26 },
    class_default: "l4",
    class_is_default: true,
    ...over,
  });

// 🔴 A PROPOSAL, not an answer: it fills an empty id field and the field stays editable. What
// it has to get exactly right is the single-file checkpoint, which has no ambiguity at all —
// and it must drop the quantisation tag, which names the FILE and not the model (the same model
// at q4 and q8 is one model with two files).
describe("engineIdFromFile", () => {
  it("proposes the stem, without the quantisation that names the file", () => {
    expect(engineIdFromFile("flux1-dev.safetensors")).toBe("flux1-dev");
    expect(engineIdFromFile("qwen2.5-coder-0.5b-instruct-q4_k_m.gguf")).toBe("qwen2.5-coder-0.5b-instruct");
    // Two quantisations of one model propose one id — the CP then refuses the second as a
    // duplicate (409 model_id_exists), which is the correct conversation to have.
    expect(engineIdFromFile("qwen2.5-coder-0.5b-instruct-q8_0.gguf")).toBe(
      engineIdFromFile("qwen2.5-coder-0.5b-instruct-q4_k_m.gguf"),
    );
    expect(engineIdFromFile("Model-BF16.safetensors")).toBe("model");
    expect(engineIdFromFile("vae/diffusion_pytorch_model.safetensors")).toBe("diffusion_pytorch_model");
  });

});

// 🔴 The two gated refusals arrive one character apart in the task's log and need opposite
// screens: 401 = no token reached the ingest task, 403 = one did and that account has not
// accepted THAT repository (measured on af-sandbox, ADR 0072 P5 実機検証: the same token took
// FLUX.1-dev in and was refused SD3.5 Medium; accepting on the model page fixed the retry).
// Both are asserted, because with only one a table with no branch passes.
describe("engineJobAdvice", () => {
  it("sends 401 to the token field and 403 to the model page", () => {
    expect(engineJobAdvice("gated_no_token")).toBe("admin.engines_ingest_job_no_token");
    expect(engineJobAdvice("gated_not_accepted")).toBe("admin.engines_ingest_job_not_accepted");
    expect(engineJobAdvice("gated_no_token")).not.toBe(engineJobAdvice("gated_not_accepted"));
    expect(engineJobAdvice("civitai_login_required")).toBe("admin.engines_ingest_civitai_login");
    expect(engineJobAdvice("civitai_gated_no_token")).toBe("admin.engines_ingest_civitai_no_token");
    expect(engineJobAdvice("civitai_gated_no_token")).not.toBe(engineJobAdvice("civitai_login_required"));
    // A job the CP could not classify says nothing extra — the task's own words are still there.
    expect(engineJobAdvice(undefined)).toBe("");
    expect(engineJobAdvice("something_else")).toBe("");
  });

});

describe("EngineModelsAdminView", () => {
  it("asks before enabling a model that does not fit the chosen card, and never guesses", async () => {
    api.mockResolvedValue({
      super_admin: true,
      engines: [
        withClasses({
          model_rows: [
            { id: "flux-dev", enabled: false, vram_need_mib: 40000, vram_need_source: "declared" },
            { id: "mystery", enabled: false, vram_need_source: "unknown" },
          ],
        }),
      ],
    });
    await mount();
    // By its label, not by position: "start with this one" leads the row, and a positional
    // helper silently tested that button instead the moment the order changed.
    const enable = (id: string) =>
      Array.from(
        Array.from(ui().querySelectorAll(".engines-model"))
          .find((li) => li.textContent?.includes(id))
          ?.querySelectorAll("button") ?? [],
      ).find((b) => b.textContent === "有効にする") as HTMLButtonElement | undefined;

    await click(enable("flux-dev"));
    // Nothing was sent: the question comes first, with both numbers in it.
    expect(apiJSON).not.toHaveBeenCalled();
    expect(ui().textContent).toContain("40000");
    expect(ui().textContent).toContain("21000");

    apiJSON.mockResolvedValue(withClasses());
    await click(
      Array.from(ui().querySelectorAll("button")).find(
        (b) => b.textContent === "承知のうえで有効にする",
      ) as HTMLButtonElement,
    );
    expect(apiJSON).toHaveBeenCalledWith(
      "api/admin/engines/image/models/flux-dev",
      "PUT",
      { enabled: true, confirm_vram: true },
    );

    // 🔴 A model nobody measured is NOT asked about: "unknown" is not "too big", and a panel
    // that asked about every unmeasured model would teach people to click through the one
    // that matters.
    apiJSON.mockClear();
    apiJSON.mockResolvedValue(withClasses());
    await click(enable("mystery"));
    expect(apiJSON).toHaveBeenCalledWith(
      "api/admin/engines/image/models/mystery",
      "PUT",
      { enabled: true },
    );
  });

  // The licence facts of a row (ADR 0072 decision 10). All three are things the CP already
  // sends and the panel used to drop, and the last one is the reason this is not cosmetic: a
  // blank where every ingested row names a licence reads as "no restrictions".
  it("shows the licence, who accepted it, and says when nothing recorded one", async () => {
    api.mockResolvedValue({
      super_admin: true,
      engines: [
        row({
          model_rows: [
            {
              id: "flux-dev",
              enabled: true,
              license: "other",
              license_name: "flux-1-dev-non-commercial-license",
              commercial_use: "no",
              license_accepted_by: "ops@example.com",
              license_accepted_at: "2026-09-09T02:00:00Z",
            },
            { id: "seeded", enabled: true },
          ],
        }),
      ],
    });
    await mount();
    const li = (id: string) =>
      Array.from(ui().querySelectorAll(".engines-model")).find((e) =>
        e.textContent?.includes(id),
      ) as HTMLElement;
    // 🔴 In the head next to the state, not buried in the meta line: this is the one fact that
    // can make offering the model somebody's own breach.
    expect(li("flux-dev").querySelector(".engines-model-tag.warn")!.textContent).toBe("非商用");
    expect(li("flux-dev").textContent).toContain("flux-1-dev-non-commercial-license");
    expect(li("flux-dev").textContent).toContain("ops@example.com");
    // A seeded row cannot know a licence and the hand-registration form does not ask.
    expect(li("seeded").textContent).toContain("ライセンスの記録なし");
    expect(li("seeded").querySelector(".engines-model-tag.warn")).toBeNull();
  });

  // "may this deployment use it" and "this is the one the engine starts with" are different
  // questions, and the panel has to offer both: sd-server holds ONE checkpoint chosen by a
  // startup flag, so enabling a second one does not load it.
  it("selects a checkpoint through the model route, without restarting anything", async () => {
    api.mockResolvedValue({
      super_admin: true,
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
    const select = Array.from(ui().querySelectorAll(".engines-model button")).filter(
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
    expect(ui().textContent).toContain("次の起動から効きます");
  });

  // The llm role's equivalent is the model a request that named none gets, so the same button
  // sends a different field. Getting this wrong would set `selected` on an engine that has no
  // such concept and silently change nothing.
  it("sends default, not selected, for a chat engine", async () => {
    api.mockResolvedValue({
      super_admin: true,
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
    const select = Array.from(ui().querySelectorAll(".engines-model button")).find(
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
      super_admin: true,
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
    expect(ui().textContent).toContain("parked");
    const enable = Array.from(ui().querySelectorAll(".engines-model button")).find(
      (b) => b.textContent === "有効にする",
    );
    await click(enable as HTMLElement);
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/models/parked", "PUT", {
      enabled: true,
    });
  });

  // 🔴 Which state a row is IN and what pressing its button WOULD DO are different sentences,
  // and only the second was ever written down: the state was carried by dimming the row to 0.6
  // opacity — indistinguishable from a disabled control, and in the light theme barely a
  // difference at all — leaving "有効にする" as the evidence for a row that is not enabled.
  it("says on or off in a badge, not only in the label of the button that would change it", async () => {
    api.mockResolvedValue({
      super_admin: true,
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
    await mount();
    const badge = (id: string) =>
      Array.from(ui().querySelectorAll(".engines-model"))
        .find((li) => li.querySelector(".engines-model-id")?.textContent === id)
        ?.querySelector(".engines-model-tag");
    expect(badge("sdxl-base-1.0")?.textContent).toBe("有効");
    expect(badge("sdxl-base-1.0")?.className).toContain("on");
    expect(badge("parked")?.textContent).toBe("無効");
    expect(badge("parked")?.className).toContain("off");

    // "Start with this one" leads: it is what somebody came to this list to do, and enabling
    // is implied by it. Forgetting the row is last.
    const row0 = Array.from(ui().querySelectorAll(".engines-model")).find(
      (li) => li.querySelector(".engines-model-id")?.textContent === "parked",
    )!;
    expect(
      Array.from(row0.querySelectorAll(".engines-model-actions button")).map((b) => b.textContent),
    ).toEqual(["これで起動する", "有効にする", "登録を消す"]);
  });

  // A LoRA is never something an engine is started with, so the control that would say so is
  // not offered — the check the Agent also makes when it builds the tool's enum.
  it("does not offer to start with a LoRA", async () => {
    api.mockResolvedValue({
      super_admin: true,
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
    const select = Array.from(ui().querySelectorAll(".engines-model button")).filter(
      (b) => b.textContent === "これで起動する",
    );
    expect(select.length).toBe(0);
    expect(ui().textContent).toContain("LoRA");
  });

  // An empty catalogue is the reason the controller refuses to start the engine, so it gets a
  // sentence. A blank area here reads as "still loading" and an administrator waits for a box
  // that is never coming.
  it("says why an engine with no catalogue will not start", async () => {
    api.mockResolvedValue({ super_admin: true, engines: [row({ has_models: false, model_rows: [] })] });
    await mount();
    expect(ui().textContent).toContain("カタログは空です");
  });

  it("says when models exist but none is enabled", async () => {
    api.mockResolvedValue({
      super_admin: true,
      engines: [
        row({ has_models: false, model_rows: [{ id: "parked", kind: "checkpoint", enabled: false }] }),
      ],
    });
    await mount();
    expect(ui().textContent).toContain("有効なモデルがありません");
  });

  // P0's definition of done is "switch the image checkpoint to another one without touching
  // CloudFormation", and the seed creates exactly ONE row per role — so there has to be a way
  // to add the second. This is not P4's ingest: it writes down a file that is already staged.
  it("registers a staged file as a catalogue row, disabled", async () => {
    api.mockResolvedValue({
      super_admin: true,
      engines: [
        row({
          has_models: true,
          model_rows: [{ id: "sdxl-base-1.0", kind: "checkpoint", enabled: true, selected: true }],
        }),
      ],
    });
    apiJSON.mockResolvedValue(row({ has_models: true, model_rows: [] }));
    await mount();
    const open = Array.from(ui().querySelectorAll("button")).find(
      (b) => b.textContent === "バケットのファイルを登録する",
    );
    await click(open as HTMLElement);

    // id, key, size, description, licence, licence URL. The WINDOW fields are chat-only, and
    // this is the image role.
    const inputs = Array.from(ui().querySelectorAll(".engines-model-add input"));
    expect(inputs.length).toBe(6);
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
    // ⚠️ The size belongs to its FILE and now sits with it, so the order is
    // id / key / size / description. A model can be several files (ADR 0072 decision 2) and a
    // single size field at the bottom of the form could not say which one it measured.
    await type(inputs[0], "juggernaut-xl-v9");
    await type(inputs[1], "image/checkpoints/juggernaut_xl_v9.safetensors");
    await type(inputs[3], "a photographic SDXL fine-tune");

    // ⚠️ The form must not imply the key was checked. The CP holds no S3 permission at all
    // (ADR 0072 review R3), so a typo only surfaces at the next cold start. Asserted while the
    // form is open, because submitting closes it.
    expect(ui().textContent).toContain("CP は S3 を見ません");

    const go = Array.from(ui().querySelectorAll(".engines-model-add button")).find(
      (b) => b.textContent === "登録する",
    );
    await click(go as HTMLElement);
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/models", "POST", {
      id: "juggernaut-xl-v9",
      kind: "checkpoint",
      files: [{ flag: "", s3Key: "image/checkpoints/juggernaut_xl_v9.safetensors", bytes: 0 }],
      description: "a photographic SDXL fine-tune",
      // Empty because THIS engine declared no vocabulary: sd.cpp holds one checkpoint and never
      // reads a family, so the panel offered no choice and there is nothing to send.
      base_model: "",
      context_tokens: 0,
      max_output_tokens: 0,
      // 🔴 Empty because nobody typed one, and it is SENT empty: an unrecorded licence is a
      // state the row states ("licence not recorded"), not a gap the panel fills in.
      license_name: "",
      license_url: "",
    });
  });

  // The one route where a licence has to be typed — there is no source here to read one from
  // (ADR 0072 decision 6 vs. decision 10). Optional, and what it buys is the verdict: the CP
  // reads "may this be used commercially" off the words, exactly as the ingest does, so the
  // same model does not lose its mark by coming in through this door.
  it("takes a licence with a hand-registered row, and sends it as the terms", async () => {
    api.mockResolvedValue({ super_admin: true, engines: [row({ has_models: true, model_rows: [] })] });
    apiJSON.mockResolvedValue(row({ has_models: true, model_rows: [] }));
    await mount();
    await click(
      Array.from(ui().querySelectorAll("button")).find(
        (b) => b.textContent === "バケットのファイルを登録する",
      ) as HTMLElement,
    );
    const inputs = Array.from(ui().querySelectorAll(".engines-model-add input"));
    const type = async (el: Element, v: string) => {
      await act(async () => {
        const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
        setter.call(el, v);
        el.dispatchEvent(new Event("input", { bubbles: true }));
      });
    };
    await type(inputs[0], "flux-dev-local");
    await type(inputs[1], "image/checkpoints/flux1-dev.safetensors");
    await type(inputs[4], "flux-1-dev-non-commercial-license");
    await type(inputs[5], "https://example.com/LICENSE.md");
    await click(
      Array.from(ui().querySelectorAll(".engines-model-add button")).find(
        (b) => b.textContent === "登録する",
      ) as HTMLElement,
    );
    // 🔴 `license_name`, not `license`: the row reads `license_name || license`, and what a
    // person types here is the TERMS. Sent as `license` it would be shadowed by nothing and
    // read as Hugging Face's slug.
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/models", "POST", {
      id: "flux-dev-local",
      kind: "checkpoint",
      files: [{ flag: "", s3Key: "image/checkpoints/flux1-dev.safetensors", bytes: 0 }],
      description: "",
      base_model: "",
      context_tokens: 0,
      max_output_tokens: 0,
      license_name: "flux-1-dev-non-commercial-license",
      license_url: "https://example.com/LICENSE.md",
    });
  });

  // "Forget" is the row, not the file: the CP has no s3:DeleteObject. The one the engine starts
  // with cannot be forgotten, or the role is left with no checkpoint at all.
  // 🔴 Measured on the dev deployment (2026-09-09): a row was forgotten and its 491 MB file was
  // still in the bucket afterwards, because the Console never asked for `?purge=1`. The CP had
  // implemented it — ADR 0072 decision 7's MODE=delete task exists precisely because the CP has
  // no s3:DeleteObject — and there was simply no way in from the UI.
  //
  // So the two acts are told apart BEFORE the press: forgetting alone leaves bytes nothing can
  // reach and that keep being paid for, and deleting them is a task that has to be started.
  it("tells forgetting the row apart from deleting the bytes, and can ask for both", async () => {
    api.mockImplementation(async (p: string) =>
      p.endsWith("/ingest")
        ? { jobs: [] }
        : {
            super_admin: true,
            engines: [
              row({
                key: "llm",
                api: "chat",
                has_models: true,
                model_rows: [{ id: "qwen2.5-coder-0.5b", enabled: false }],
              }),
            ],
          },
    );
    await mount();

    // Pressing "forget" asks rather than acting: nothing has been sent yet.
    await click(
      Array.from(ui().querySelectorAll("button")).find((b) => b.textContent === "登録を消す") as HTMLElement,
    );
    expect(apiJSON).not.toHaveBeenCalled();
    // The default is the SAFE one, and it says what it leaves behind.
    const box = ui().querySelector(".engines-model-confirm input") as HTMLInputElement;
    expect(box.checked).toBe(false);
    expect(ui().textContent).toContain("バケットのファイルはそのまま残り");

    await act(async () => {
      box.click();
    });
    // Ticking it changes what the sentence promises, because the act is now destructive.
    expect(ui().textContent).toContain("バイト列を削除するタスクを起こします");

    apiJSON.mockResolvedValueOnce({ purge: "deleting llm/qwen2.5-coder-0.5b-instruct-q4_k_m.gguf" });
    await click(
      Array.from(ui().querySelectorAll(".engines-model-confirm button")).find(
        (b) => b.textContent === "消す",
      ) as HTMLElement,
    );
    const call = apiJSON.mock.calls.at(-1)!;
    expect(String(call[0])).toBe("api/admin/engines/llm/models/qwen2.5-coder-0.5b?purge=1");
    expect(String(call[1])).toBe("DELETE");
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    // The CP's own words: the deletion is a task that has been LAUNCHED, not a thing that has
    // already happened, and a row that just vanished would not say so.
    expect(ui().textContent).toContain("deleting llm/qwen2.5-coder-0.5b-instruct-q4_k_m.gguf");
  });

  // Leaving the box unticked must send NO purge — the destructive half has to be opt-in.
  it("forgets the row alone when the bytes were not asked for", async () => {
    api.mockImplementation(async (p: string) =>
      p.endsWith("/ingest")
        ? { jobs: [] }
        : {
            super_admin: true,
            engines: [
              row({ key: "llm", api: "chat", has_models: true, model_rows: [{ id: "m1", enabled: false }] }),
            ],
          },
    );
    await mount();
    await click(
      Array.from(ui().querySelectorAll("button")).find((b) => b.textContent === "登録を消す") as HTMLElement,
    );
    apiJSON.mockResolvedValueOnce({});
    await click(
      Array.from(ui().querySelectorAll(".engines-model-confirm button")).find(
        (b) => b.textContent === "消す",
      ) as HTMLElement,
    );
    expect(String(apiJSON.mock.calls.at(-1)![0])).toBe("api/admin/engines/llm/models/m1");
  });

  it("forgets a row, and refuses to forget the one in use", async () => {
    api.mockResolvedValue({
      super_admin: true,
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
    const forget = Array.from(ui().querySelectorAll(".engines-model")).map((li) =>
      Array.from(li.querySelectorAll("button")).find((b) => b.textContent === "登録を消す"),
    );
    expect((forget[0] as HTMLButtonElement).disabled).toBe(true);
    expect((forget[1] as HTMLButtonElement).disabled).toBe(false);
    await click(forget[1] as HTMLElement);
    // Forgetting now asks first (see the purge test above), and the default leaves the bytes.
    await click(
      Array.from(ui().querySelectorAll(".engines-model-confirm button")).find(
        (b) => b.textContent === "消す",
      ) as HTMLElement,
    );
    expect(apiJSON).toHaveBeenCalledWith(
      "api/admin/engines/image/models/parked",
      "DELETE",
      undefined,
    );
  });

  // Nobody declared the sizes (every row the seed makes, and every row registered before the
  // field existed). "+0 s" would be a claim; nothing is the truth.
  // The cold-start cost of each model, next to the toggle that adds it — and marked as an
  // estimate, because the control plane has never looked at the bucket (ADR 0072 review R3).
  it("says what enabling a model adds to the next cold start", async () => {
    api.mockResolvedValue({
      super_admin: true,
      engines: [
        row({
          key: "llm",
          api: "chat",
          provider: "llamacpp",
          has_models: true,
          model_rows: [
            { id: "qwen3-coder-30b-a3b", kind: "gguf", enabled: true, default: true, sync_secs: 179 },
            { id: "qwen2.5-coder-1.5b", kind: "gguf", enabled: true, sync_secs: 11 },
          ],
        }),
      ],
    });
    await mount();
    expect(ui().textContent).toContain("同期 +179 秒（推定）");
    expect(ui().textContent).toContain("同期 +11 秒（推定）");
  });

  it("says nothing about the sync when no size was declared", async () => {
    api.mockResolvedValue({
      super_admin: true,
      engines: [
        row({ has_models: true, model_rows: [{ id: "sdxl-base-1.0", kind: "checkpoint", enabled: true }] }),
      ],
    });
    await mount();
    expect(ui().textContent).not.toContain("同期 +");
  });

  // ADR 0072 P1: a chat engine's row carries its OWN window, and this form is the only way to
  // declare one until P4's ingest reads it off the model card. A model registered without one
  // reaches opencode as context 0 — which switches auto-compaction off.
  it("declares a window and a size when registering a model for a chat engine", async () => {
    api.mockResolvedValue({
      super_admin: true,
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
      Array.from(ui().querySelectorAll("button")).find(
        (b) => b.textContent === "バケットのファイルを登録する",
      ) as HTMLElement,
    );
    // One field per ROW, each with its own label: eight labelled rows, not a strip of look-alike
    // boxes whose placeholder captions vanish as soon as somebody types into them. 🔴 The kind
    // (model or LoRA) is NOT among them any more — it is the tab this form is under, so the
    // list beside it and the row it would register cannot disagree about which is being added.
    const rows = Array.from(ui().querySelectorAll(".engines-model-add-row"));
    expect(rows.length).toBe(8);
    expect(
      rows.every((r) => r.querySelector("span") && (r.querySelector("input") || r.querySelector("select"))),
    ).toBe(true);
    const inputs = Array.from(ui().querySelectorAll(".engines-model-add input"));
    expect(inputs.length).toBe(8);
    const type = async (el: Element, v: string) => {
      await act(async () => {
        const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
        setter.call(el, v);
        el.dispatchEvent(new Event("input", { bubbles: true }));
      });
    };
    await type(inputs[0], "qwen2.5-coder-1.5b");
    await type(inputs[1], "llm/qwen2.5-coder-1.5b-instruct-q4_k_m.gguf");
    await type(inputs[2], "1117320768");
    await type(inputs[3], "small and quick");
    // 4 and 5 are the optional licence pair; the window is the last two.
    await type(inputs[6], "32768");
    await type(inputs[7], "4096");
    await click(
      Array.from(ui().querySelectorAll(".engines-model-add button")).find(
        (b) => b.textContent === "登録する",
      ) as HTMLElement,
    );
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/llm/models", "POST", {
      id: "qwen2.5-coder-1.5b",
      kind: "gguf",
      files: [{ flag: "", s3Key: "llm/qwen2.5-coder-1.5b-instruct-q4_k_m.gguf", bytes: 1117320768 }],
      description: "small and quick",
      base_model: "",
      context_tokens: 32768,
      max_output_tokens: 4096,
      license_name: "",
      license_url: "",
    });
  });

  // ADR 0072 decision 5, the llm half: an adapter is registered like a file and pinned to the
  // model it fine-tunes. Three things move together when the kind changes, which is why it is one
  // control: the kind itself, where the file belongs in the bucket, and what "applies to" means
  // (a model ID here, a ComfyUI family on the image role).
  //
  // 🔴 Until this existed NO route in the Console could create a LoRA row for either role — both
  // forms sent `checkpoint`/`gguf` and nothing else — so the llm half of decision 5 was
  // unreachable without calling the admin API by hand.
  it("registers a LoRA against the model it fine-tunes, with no window of its own", async () => {
    api.mockResolvedValue({
      super_admin: true,
      engines: [
        row({
          key: "llm",
          api: "chat",
          provider: "llamacpp",
          has_models: true,
          model_rows: [
            { id: "qwen3-coder-30b-a3b", kind: "gguf", enabled: true, default: true },
            { id: "an-old-adapter", kind: "lora", enabled: false, base_model: "qwen3-coder-30b-a3b" },
          ],
        }),
      ],
    });
    apiJSON.mockResolvedValue(row({ key: "llm", api: "chat", has_models: true, model_rows: [] }));
    await mount();
    // 🔴 The LoRA TAB is what makes this a LoRA form. It used to be a select inside the form,
    // beside a list that was showing models — so the two halves of one screen disagreed about
    // what was being added, and the search above them always asked for checkpoints.
    await click(tab("LoRA"));
    await click(
      Array.from(ui().querySelectorAll("button")).find(
        (b) => b.textContent === "バケットのファイルを登録する",
      ) as HTMLElement,
    );
    const type = async (el: Element, v: string) => {
      await act(async () => {
        const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
        setter.call(el, v);
        el.dispatchEvent(new Event("input", { bubbles: true }));
      });
    };
    const pick = async (el: Element, v: string) => {
      await act(async () => {
        const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!;
        setter.call(el, v);
        el.dispatchEvent(new Event("change", { bubbles: true }));
      });
    };
    const selects = () => Array.from(ui().querySelectorAll(".engines-model-add select"));

    // The base is a CHOICE over this catalogue's own model ids — a typed name that matches
    // nothing is an adapter that loads nowhere and says so nowhere.
    const base = selects()[0] as HTMLSelectElement;
    const offered = Array.from(base.options).map((o) => o.value);
    expect(offered).toContain("qwen3-coder-30b-a3b");
    expect(offered).not.toContain("an-old-adapter"); // a LoRA is not a base for another LoRA

    const go = () =>
      Array.from(ui().querySelectorAll(".engines-model-add button")).find(
        (b) => b.textContent === "登録する",
      ) as HTMLButtonElement;
    let inputs = Array.from(ui().querySelectorAll(".engines-model-add input"));
    await type(inputs[0], "house-style");
    await type(inputs[1], "llm/loras/house-style.gguf");
    // Required, and the form says so by refusing rather than by letting the CP answer later.
    expect(go().disabled).toBe(true);
    await pick(base, "qwen3-coder-30b-a3b");
    expect(go().disabled).toBe(false);

    // The window is gone — an adapter has none, it is loaded with the model that does — and a
    // strength has taken its place.
    inputs = Array.from(ui().querySelectorAll(".engines-model-add input"));
    const labels = Array.from(ui().querySelectorAll(".engines-model-add-row span")).map(
      (l) => l.textContent,
    );
    expect(labels).not.toContain("コンテキストウィンドウ");
    expect(labels).toContain("強さ（0〜2・既定 1）");
    await type(inputs[inputs.length - 1], "0.8");

    await click(go());
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/llm/models", "POST", {
      id: "house-style",
      kind: "lora",
      files: [{ flag: "", s3Key: "llm/loras/house-style.gguf", bytes: 0 }],
      description: "",
      base_model: "qwen3-coder-30b-a3b",
      args: ["--scale", "0.8"],
      context_tokens: 0,
      max_output_tokens: 0,
      license_name: "",
      license_url: "",
    });
  });

  // A ComfyUI engine picks its workflow graph from the model's FAMILY and refuses to guess one
  // from a name, so the panel has to ask — and it asks with a CHOICE, because what a repository
  // calls a model ("SDXL 1.0") is a display name that names no graph.
  //
  // 🔴 Until this existed the form had one key field and no family at all, so the two things
  // ComfyUI needs were both unreachable from the Console: on af-sandbox the catalogue had to be
  // written by calling the admin API by hand (ADR 0072 P2 実機検証).
  it("asks a comfy engine for a family, and lets one model be several files", async () => {
    const comfy = row({
      provider: "comfy",
      base_models: ["sdxl", "sd35", "flux1", "flux2-klein", "zimage"],
      file_flags: ["", "--diffusion-model", "--clip_l", "--t5xxl", "--vae"],
      has_models: true,
      model_rows: [],
    });
    api.mockResolvedValue({ super_admin: true, engines: [comfy] });
    apiJSON.mockResolvedValue(comfy);
    await mount();
    await click(
      Array.from(ui().querySelectorAll("button")).find(
        (b) => b.textContent === "バケットのファイルを登録する",
      ) as HTMLElement,
    );

    const type = async (el: Element, v: string) => {
      await act(async () => {
        const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
        setter.call(el, v);
        el.dispatchEvent(new Event("input", { bubbles: true }));
      });
    };
    const pick = async (el: Element, v: string) => {
      await act(async () => {
        const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!;
        setter.call(el, v);
        el.dispatchEvent(new Event("change", { bubbles: true }));
      });
    };
    const go = () =>
      Array.from(ui().querySelectorAll(".engines-model-add button")).find(
        (b) => b.textContent === "登録する",
      ) as HTMLButtonElement;

    let inputs = Array.from(ui().querySelectorAll(".engines-model-add input"));
    await type(inputs[0], "flux2-klein-4b");
    await type(inputs[1], "image/diffusion_models/flux-2-klein-4b.safetensors");

    // ⚠️ The family is not optional here, and the form says so by refusing rather than by
    // letting the CP answer 400 after the press.
    expect(go().disabled).toBe(true);

    // [0] is the family; one part per file follows it.
    const selects = () => Array.from(ui().querySelectorAll(".engines-model-add select"));
    await pick(selects()[0], "flux2-klein");
    expect(go().disabled).toBe(false);
    // The first file's part, then two more files with their own.
    await pick(selects()[1], "--diffusion-model");

    const more = () =>
      Array.from(ui().querySelectorAll(".engines-model-add button")).find(
        (b) => b.textContent === "ファイルを追加する",
      ) as HTMLElement;
    await click(more());
    await click(more());
    inputs = Array.from(ui().querySelectorAll(".engines-model-add input"));
    // id, then (key, size) per file, then the description and the optional licence pair: three
    // files is ten inputs.
    expect(inputs.length).toBe(10);
    await type(inputs[3], "image/text_encoders/qwen_3_4b_fp8_mixed.safetensors");
    await type(inputs[5], "image/vae/flux2-vae.safetensors");
    await pick(selects()[2], "--clip_l");
    await pick(selects()[3], "--vae");

    await click(go());
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/models", "POST", {
      id: "flux2-klein-4b",
      kind: "checkpoint",
      files: [
        { flag: "--diffusion-model", s3Key: "image/diffusion_models/flux-2-klein-4b.safetensors", bytes: 0 },
        { flag: "--clip_l", s3Key: "image/text_encoders/qwen_3_4b_fp8_mixed.safetensors", bytes: 0 },
        { flag: "--vae", s3Key: "image/vae/flux2-vae.safetensors", bytes: 0 },
      ],
      description: "",
      base_model: "flux2-klein",
      context_tokens: 0,
      max_output_tokens: 0,
      license_name: "",
      license_url: "",
    });
  });

  // Where a row and each of its files came from. The value was stored since migration 0060 and
  // the panel printed it as one more word in a "·"-joined line, so the one question it answers —
  // WHICH vendor's model of that name this is — took a copy of the S3 key and a search.
  //
  // 🔴 Read from the elements, never from the page's textContent: this screen has grown a search
  // panel and a job list that print `hf:` strings of their own, and an `includes()` here would go
  // on passing with the row's own line deleted (it has happened twice on this screen).
  it("links a row and each file to where it came from, and links nothing it cannot compose", async () => {
    api.mockResolvedValue({
      super_admin: true,
      engines: [
        row({
          has_models: true,
          model_rows: [
            {
              id: "qwen2.5-coder-1.5b",
              enabled: true,
              source: "hf:Qwen/Qwen2.5-Coder-1.5B-Instruct-GGUF/qwen2.5-coder-1.5b-instruct-q4_k_m.gguf",
              source_url:
                "https://huggingface.co/Qwen/Qwen2.5-Coder-1.5B-Instruct-GGUF/blob/main/qwen2.5-coder-1.5b-instruct-q4_k_m.gguf",
              files: ["qwen2.5-coder-1.5b-instruct-q4_k_m.gguf"],
              file_rows: [
                {
                  s3Key: "llm/qwen2.5-coder-1.5b-instruct-q4_k_m.gguf",
                  source: "hf:Qwen/Qwen2.5-Coder-1.5B-Instruct-GGUF/qwen2.5-coder-1.5b-instruct-q4_k_m.gguf",
                  source_url:
                    "https://huggingface.co/Qwen/Qwen2.5-Coder-1.5B-Instruct-GGUF/blob/main/qwen2.5-coder-1.5b-instruct-q4_k_m.gguf",
                },
              ],
            },
            {
              // The case the row-level source cannot describe: four files, three of them from
              // repositories that did not publish the diffusion model.
              id: "flux1-dev",
              enabled: false,
              source: "hf:black-forest-labs/FLUX.1-dev/flux1-dev.safetensors",
              source_url: "https://huggingface.co/black-forest-labs/FLUX.1-dev/blob/main/flux1-dev.safetensors",
              files: ["flux1-dev.safetensors", "t5xxl_fp8_e4m3fn.safetensors", "ae.safetensors"],
              file_rows: [
                {
                  s3Key: "image/diffusion_models/flux1-dev.safetensors",
                  source: "hf:black-forest-labs/FLUX.1-dev/flux1-dev.safetensors",
                  source_url:
                    "https://huggingface.co/black-forest-labs/FLUX.1-dev/blob/main/flux1-dev.safetensors",
                },
                {
                  s3Key: "image/text_encoders/t5xxl_fp8_e4m3fn.safetensors",
                  source: "hf:comfyanonymous/flux_text_encoders/t5xxl_fp8_e4m3fn.safetensors",
                  source_url:
                    "https://huggingface.co/comfyanonymous/flux_text_encoders/blob/main/t5xxl_fp8_e4m3fn.safetensors",
                },
                // Staged by hand: no origin was ever recorded for this part.
                { s3Key: "image/vae/ae.safetensors" },
              ],
            },
            {
              // A plain url source. The CP composes no link for it on purpose — that href is the
              // direct download of the weights, not a page.
              id: "staged-by-url",
              enabled: false,
              source: "https://example.invalid/m.safetensors",
              files: ["m.safetensors"],
              file_rows: [{ s3Key: "image/checkpoints/m.safetensors" }],
            },
            {
              // The seed. Nobody recorded a source, which is a different fact from "recorded and
              // unreadable" and must not be drawn as a value.
              id: "seeded",
              enabled: false,
              files: ["sdxl.safetensors"],
              file_rows: [{ s3Key: "image/checkpoints/sdxl.safetensors" }],
            },
          ],
        }),
      ],
    });
    await mount();
    const provenance = (id: string) => {
      const li = Array.from(ui().querySelectorAll("li.engines-model")).find(
        (el) => el.querySelector(".engines-model-id")?.textContent === id,
      );
      expect(li, `no row for ${id}`).toBeTruthy();
      return li!.querySelector(".engines-model-provenance") as HTMLElement | null;
    };
    const links = (id: string) =>
      Array.from(provenance(id)?.querySelectorAll("a.engines-model-source") || []);

    // One file: the row's own source is the link, and the file name beside it is NOT a second
    // copy of it — for a single-file model the two are the same string.
    const one = links("qwen2.5-coder-1.5b");
    expect(one.length).toBe(1);
    expect(one[0].getAttribute("href")).toBe(
      "https://huggingface.co/Qwen/Qwen2.5-Coder-1.5B-Instruct-GGUF/blob/main/qwen2.5-coder-1.5b-instruct-q4_k_m.gguf",
    );
    expect(one[0].getAttribute("target")).toBe("_blank");
    expect(one[0].textContent).toBe(
      "hf:Qwen/Qwen2.5-Coder-1.5B-Instruct-GGUF/qwen2.5-coder-1.5b-instruct-q4_k_m.gguf",
    );
    expect(
      Array.from(provenance("qwen2.5-coder-1.5b")!.querySelectorAll(".engines-model-file")).map(
        (e) => e.textContent,
      ),
    ).toEqual(["qwen2.5-coder-1.5b-instruct-q4_k_m.gguf"]);

    // Four files, three origins: the row's, the part that came from somewhere else (by NAME,
    // pointing at its own repository), and the part nobody recorded — which stays plain text.
    const split = links("flux1-dev");
    expect(split.map((a) => a.textContent)).toEqual([
      "hf:black-forest-labs/FLUX.1-dev/flux1-dev.safetensors",
      "t5xxl_fp8_e4m3fn.safetensors",
    ]);
    expect(split[1].getAttribute("href")).toBe(
      "https://huggingface.co/comfyanonymous/flux_text_encoders/blob/main/t5xxl_fp8_e4m3fn.safetensors",
    );
    expect(split[1].getAttribute("title")).toBe(
      "hf:comfyanonymous/flux_text_encoders/t5xxl_fp8_e4m3fn.safetensors",
    );
    expect(
      Array.from(provenance("flux1-dev")!.querySelectorAll(".engines-model-file")).map(
        (e) => e.textContent,
      ),
    ).toEqual(["flux1-dev.safetensors", "ae.safetensors"]);

    // A url source is READ, not clicked: the text is there and there is no link at all.
    expect(links("staged-by-url").length).toBe(0);
    expect(
      provenance("staged-by-url")!.querySelector(".engines-model-source")!.textContent,
    ).toBe("https://example.invalid/m.safetensors");

    // And the seed says nothing rather than "unknown". The line still carries its file name, so
    // this asserts the absence of a SOURCE rather than of the whole element.
    expect(links("seeded").length).toBe(0);
    expect(provenance("seeded")!.querySelector(".engines-model-source")).toBeNull();
    expect(provenance("seeded")!.textContent).toBe("sdxl.safetensors");
  });

  // The row the SEED writes, and every row written before the CP validated one: complete in
  // every way this panel can see, and unable to generate. The CP states it because the panel
  // cannot know the vocabulary.
  it("says so on a row whose family names no workflow", async () => {
    api.mockResolvedValue({
      super_admin: true,
      engines: [
        row({
          provider: "comfy",
          base_models: ["sdxl"],
          has_models: true,
          model_rows: [
            { id: "seeded", kind: "checkpoint", enabled: true, base_model_missing: true },
            { id: "declared", kind: "checkpoint", enabled: true, base_model: "sdxl" },
          ],
        }),
      ],
    });
    await mount();
    const warnings = Array.from(ui().querySelectorAll(".form-err")).filter((e) =>
      e.textContent?.includes("モデルファミリー"),
    );
    expect(warnings.length).toBe(1);

    // The FIX sits under the warning it answers, and only there — the row that already declares
    // one shows it in its meta line and needs no control.
    const pickers = Array.from(ui().querySelectorAll(".engines-model-family"));
    expect(pickers.length).toBe(1);

    // 🔴 One field, not the whole row. Before this the only way to give a row a family was to
    // register it again from scratch: that lands it disabled and, for a split model, means
    // re-typing three S3 keys to change one word.
    apiJSON.mockResolvedValue(row({ provider: "comfy", base_models: ["sdxl"], has_models: true, model_rows: [] }));
    const sel = pickers[0].querySelector("select")!;
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!;
      setter.call(sel, "sdxl");
      sel.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/models/seeded", "PUT", {
      base_model: "sdxl",
    });
  });

  // 🔴 ADR 0072 P6 R2. Since P6 the catalogue is the only declaration in the deployment, so
  // the moment a row is forgotten is the last moment anyone can read what it WAS — and on the
  // real deployment a row was forgotten and rebuilt from a note, which worked only because it
  // was one file. A FLUX.1 row is four keys with four flags.
  //
  // The keys are shown HERE and nowhere else: with purge ticked this is also the list the
  // delete task is handed, and a row's meta line stays base names so that choosing between
  // rows does not cost four lines each.
  it("shows the S3 keys of a row at the moment it is about to be forgotten", async () => {
    api.mockResolvedValue({
      super_admin: true,
      engines: [
        row({
          has_models: true,
          model_rows: [
            {
              id: "flux1-dev-fp8",
              kind: "checkpoint",
              base_model: "flux1",
              files: ["flux1-dev-fp8.safetensors", "clip_l.safetensors"],
              file_rows: [
                { flag: "--diffusion-model", s3Key: "image/diffusion_models/flux1-dev-fp8.safetensors" },
                { flag: "--clip_l", s3Key: "image/text_encoders/clip_l.safetensors" },
              ],
            },
          ],
        }),
      ],
    });
    await mount();
    // Not on the row itself: base names are what a person chooses between.
    expect(ui().querySelector(".engines-model-keys")).toBe(null);
    expect(ui().textContent).not.toContain("image/text_encoders/clip_l.safetensors");

    await click(
      Array.from(ui().querySelectorAll("li.engines-model button")).find(
        (b) => b.textContent === "登録を消す",
      ) as HTMLElement,
    );
    const keys = ui().querySelector(".engines-model-keys")!;
    expect(keys.textContent).toContain("image/diffusion_models/flux1-dev-fp8.safetensors");
    // With the flag, because a key alone does not say which loader the part was for.
    expect(keys.textContent).toContain("--clip_l");
  });

  // 🔴 ADR 0072 P2 欠落 10. Declaring a family cleared the only mark this panel had, and the
  // row still could not generate: `flux1-dev` was one unflagged 22.2 GiB checkpoint and the
  // flux1 template reads four other files. The row came out of the fix looking healthier.
  it("keeps saying so when a row's family reads files the row does not have", async () => {
    api.mockResolvedValue({
      super_admin: true,
      engines: [
        row({
          provider: "comfy",
          base_models: ["sdxl", "flux1"],
          has_models: true,
          model_rows: [
            {
              id: "flux1-dev",
              kind: "checkpoint",
              base_model: "flux1",
              files_missing: ["--diffusion-model", "--clip_l", "--t5xxl", "--vae"],
            },
            { id: "sdxl-base-1.0", kind: "checkpoint", enabled: true, base_model: "sdxl" },
          ],
        }),
      ],
    });
    await mount();
    const li = Array.from(ui().querySelectorAll("li.engines-model")).find(
      (n) => n.querySelector(".mono")?.textContent === "flux1-dev",
    )!;
    const said = li.querySelector(".form-err")!.textContent!;
    // The family it has, and every part it still needs — those are what have to be taken in.
    expect(said).toContain("flux1");
    expect(said).toContain("--t5xxl");
    // The row that holds what its family reads says nothing: a mark on every row is no mark.
    const ok = Array.from(ui().querySelectorAll("li.engines-model")).find(
      (n) => n.querySelector(".mono")?.textContent === "sdxl-base-1.0",
    )!;
    expect(ok.querySelector(".form-err")).toBe(null);
  });

  // Half a window is worse than none: opencode reads an output cap of 0 as 32,000, so a 32k
  // context declared alone leaves 768 usable tokens. Both halves or neither.
  it("drops a context declared without an output cap", async () => {
    api.mockResolvedValue({
      super_admin: true,
      engines: [row({ key: "llm", api: "chat", has_models: true, model_rows: [] })],
    });
    apiJSON.mockResolvedValue(row({ key: "llm", api: "chat", has_models: true, model_rows: [] }));
    await mount();
    await click(
      Array.from(ui().querySelectorAll("button")).find(
        (b) => b.textContent === "バケットのファイルを登録する",
      ) as HTMLElement,
    );
    const inputs = Array.from(ui().querySelectorAll(".engines-model-add input"));
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
      Array.from(ui().querySelectorAll(".engines-model-add button")).find(
        (b) => b.textContent === "登録する",
      ) as HTMLElement,
    );
    const body = apiJSON.mock.calls.at(-1)![2] as Record<string, number>;
    expect(body.context_tokens).toBe(0);
    expect(body.max_output_tokens).toBe(0);
  });

  // 🔴 Observed on the dev deployment (2026-09-09): after a model was forgotten AND its bytes
  // purged, two finished jobs still sat under the ingest form — correctly, because a job is a
  // record of an event and "this ingest ran and finished" goes on being true. But undated, a
  // green "done" beside a model id reads as THAT MODEL's current state, i.e. as "ready to
  // use", which is the opposite of the truth for a model that no longer exists anywhere.
  it("dates the ingest history, so a finished job does not read as a model that is ready", async () => {
    api.mockImplementation(async (p: string) =>
      p.endsWith("/ingest")
        ? {
            jobs: [
              {
                id: "j1",
                model_id: "qwen2.5-coder-0.5b-instruct",
                state: "done",
                source: "hf:Qwen/Qwen2.5-Coder-0.5B-Instruct-GGUF/…",
                bytes: 491400064,
                created_at: "2026-09-09T02:59:30Z",
              },
            ],
          }
        : { super_admin: true, engines: [row({ key: "llm", api: "chat", has_models: true, model_rows: [] })] },
    );
    await mount();
    // Headed as history, not as a section of the catalogue above it.
    expect(ui().textContent).toContain("取り込みの履歴");
    const when = ui().querySelector(".engines-ingest-when");
    expect(when).toBeTruthy();
    expect(when!.textContent).toBeTruthy();
    // The job survives a model that is not in the catalogue at all — that IS the case this
    // dating exists for, so the row has to still be here.
    expect(ui().textContent).toContain("qwen2.5-coder-0.5b-instruct");
  });

  // The advice sits BESIDE the task's own words, never instead of them: `curl: (22) … 403` is
  // sometimes the only detail there is, and it is what a search of the logs matches on.
  it("says what to do about a job that failed because the terms were not accepted", async () => {
    api.mockImplementation(async (p: string) =>
      p.endsWith("/ingest")
        ? {
            jobs: [
              {
                id: "j1",
                model_id: "sd35-medium",
                state: "failed",
                source: "hf:stabilityai/stable-diffusion-3.5-medium/sd3.5_medium.safetensors",
                message: "curl: (22) The requested URL returned error: 403",
                code: "gated_not_accepted",
              },
            ],
          }
        : { super_admin: true, engines: [row({ key: "image", has_models: true, model_rows: [] })] },
    );
    await mount();
    const job = ui().querySelector("ul.engines-ingest-jobs li")!;
    expect(job.textContent).toContain("error: 403");
    expect(job.textContent).toContain("条項にまだ同意していません");
    // Not the token sentence: registering one again fixes nothing here.
    expect(job.textContent).not.toContain("トークンが取り込みタスクに届いていません");
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
            super_admin: true,
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
        ui().querySelectorAll("ul.engines-model-list:not(.engines-ingest-jobs) .mono"),
      ).map((n) => n.textContent);
    expect(catalogueIds()).toEqual(["qwen2.5-coder-1.5b"]);
    expect(ui().textContent).toContain("取り込み中");

    finished = true;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10000);
    });

    expect(ui().textContent).toContain("完了");
    expect(catalogueIds()).toEqual(["qwen2.5-coder-1.5b", "qwen2.5-coder-0.5b"]);
    // Disabled, so what it offers is the switch-on — decision 6: taken in is not the same as
    // on offer.
    const fresh = Array.from(ui().querySelectorAll("li.engines-model")).find(
      (li) => li.querySelector(".mono")?.textContent === "qwen2.5-coder-0.5b",
    )!;
    expect(fresh.className).not.toContain("on");
    expect(fresh.textContent).toContain("有効にする");
  });

});

// The operator's Hugging Face and Civitai tokens moved to their own screen
// (adminEngineTokens.dom.test.tsx) — they no longer render inside this one.

// 🔴 A deployment that has not adopted 60-engines has an EMPTY panel, and "there is nothing
// here" is the worst possible answer to "what could I run?". Looking at what Hugging Face has
// needs no engine at all — no token, no bucket, no task — so the browse stays.
describe("EnginesAdminView / browsing with no engine deployed", () => {
  const button = (label: string) =>
    Array.from(ui().querySelectorAll("button")).find((b) => b.textContent === label) as
      | HTMLButtonElement
      | undefined;
  it("still offers a look at what there is, and says it cannot take anything in", async () => {
    api.mockResolvedValue({ super_admin: true, engines: [] });
    apiJSON.mockResolvedValue({
      hits: [{ source: "hf", ref: "unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF", name: "unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF", downloads: 12623435 }],
    });
    await mount();
    // The "nothing deployed" sentence stays — the browse is added beside it, not instead of it.
    expect(ui().textContent).toContain("動かしていません");

    await click(button("人気を見る"));
    // The keyless route, with the kind stated rather than derived: there is no engine to
    // derive it from.
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/search?kind=gguf", "POST", {
      q: "",
      source: "hf",
      sort: "downloads",
    });
    expect(ui().querySelector(".engines-search-hits")!.textContent).toContain("Qwen3-Coder-30B");
    expect(ui().textContent).toContain("12.6M");
    // 🔴 A hit is NOT clickable here: picking one fills an ingest form, and this deployment has
    // no role to ingest into. The note says so instead of offering a button that cannot work.
    expect(ui().querySelector(".engines-search-hits li button")).toBeNull();
    expect(ui().textContent).toContain("閲覧だけです");
  });

  it("asks for the kind, because there is no engine to derive it from", async () => {
    api.mockResolvedValue({ super_admin: true, engines: [] });
    apiJSON.mockResolvedValue({ hits: [] });
    await mount();
    await click(button("画像（checkpoint）"));
    await click(button("人気を見る"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/search?kind=checkpoint", "POST", {
      q: "",
      source: "hf",
      sort: "downloads",
    });

    // Civitai is offered here too, and only for checkpoints.
    await click(button("Civitai"));
    await click(button("人気を見る"));
    expect(apiJSON).toHaveBeenLastCalledWith("api/admin/engines/search?kind=checkpoint", "POST", {
      q: "",
      source: "civitai",
      sort: "downloads",
    });
    // Going back to GGUF takes the source with it: Civitai hosts no GGUFs, and a search that
    // can only answer nothing reads as a broken one.
    await click(button("LLM（GGUF）"));
    expect(button("Civitai")).toBeUndefined();
    await click(button("人気を見る"));
    expect(apiJSON).toHaveBeenLastCalledWith("api/admin/engines/search?kind=gguf", "POST", {
      q: "",
      source: "hf",
      sort: "downloads",
    });
  });

});

// The model row's own negative prompt (ADR 0072 follow-up, negative prompts). It is edited in
// place like base_model and for the same reason: a split model is four S3 keys, and
// re-registering all of them to change one sentence is an edit nobody makes twice.
describe("EngineModelsAdminView / a model's own negative prompt", () => {
  const typeInto = async (el: Element, v: string) => {
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      setter.call(el, v);
      el.dispatchEvent(new Event("input", { bubbles: true }));
    });
  };
  const saveButton = () =>
    Array.from(ui().querySelectorAll(".engines-model-negative button"))[0] as HTMLElement;

  it("saves what this checkpoint should keep out, and lets it be cleared", async () => {
    api.mockResolvedValue({
      super_admin: true,
      engines: [
        row({
          provider: "comfy",
          base_models: ["sdxl"],
          has_models: true,
          model_rows: [
            {
              id: "sdxl-base-1.0",
              kind: "checkpoint",
              enabled: true,
              base_model: "sdxl",
              negative_prompt: "extra fingers",
            },
          ],
        }),
      ],
    });
    await mount();
    const box = ui().querySelector(".engines-model-negative input") as HTMLInputElement;
    expect(box.value).toBe("extra fingers");

    await typeInto(box, "extra fingers, text");
    apiJSON.mockResolvedValue(row({}));
    await click(saveButton());
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/models/sdxl-base-1.0", "PUT", {
      negative_prompt: "extra fingers, text",
    });

    // Clearing is a real edit, not a no-op: it means "stop declaring one", which the Agent
    // answers with its own measured default rather than with nothing excluded.
    apiJSON.mockClear();
    apiJSON.mockResolvedValue(row({}));
    await typeInto(ui().querySelector(".engines-model-negative input")!, "");
    await click(saveButton());
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/models/sdxl-base-1.0", "PUT", {
      negative_prompt: "",
    });
  });

  // A LoRA is not what a request names, so it has nothing to say about what a picture keeps out.
  it("is not offered on a LoRA row", async () => {
    api.mockResolvedValue({
      super_admin: true,
      engines: [
        row({
          provider: "comfy",
          base_models: ["sdxl"],
          has_models: true,
          model_rows: [{ id: "watercolor-v2", kind: "lora", enabled: true, base_model: "sdxl" }],
        }),
      ],
    });
    await mount();
    // 🔥 Onto the adapter tab first. This assertion used to be made on the MODEL tab, where a
    // LoRA row is not rendered at all — so it passed with the guard that omits this editor
    // deleted, and would have gone on passing for ever. Found while adding the window editor
    // next door, which needed the same guard and the same pairing.
    await click(tab("LoRA"));
    expect(ui().querySelector(".engines-model-id")?.textContent).toBe("watercolor-v2");
    expect(ui().querySelector(".engines-model-negative")).toBeNull();
  });
});

// The adapter's counterpart (ADR 0081 decision 5). Civitai publishes the words a LoRA answers
// to, the ingest reads them and now stores them — and publishers get them wrong often enough
// that correcting one here is the point. A LoRA loaded without its trigger changes nothing
// visible, which is indistinguishable from an ingest that failed.
describe("EngineModelsAdminView / an adapter's trigger words", () => {
  const typeInto = async (el: Element, v: string) => {
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      setter.call(el, v);
      el.dispatchEvent(new Event("input", { bubbles: true }));
    });
  };
  const saveButton = () =>
    Array.from(host!.querySelectorAll(".engines-model-trigger button"))[0] as HTMLElement;
  const loraEngine = (trained?: string[]) => ({
    super_admin: true,
    engines: [
      row({
        provider: "comfy",
        base_models: ["sdxl"],
        has_models: true,
        model_rows: [
          {
            id: "watercolor-v2",
            kind: "lora",
            enabled: true,
            base_model: "sdxl",
            ...(trained ? { trained_words: trained } : {}),
          },
        ],
      }),
    ],
  });

  it("shows the stored words comma separated and saves them as a list", async () => {
    api.mockResolvedValue(loraEngine(["watercolor", "wc style"]));
    await mount();
    await click(tab("LoRA"));
    const box = host!.querySelector(".engines-model-trigger input") as HTMLInputElement;
    // One box, not three: the column is a list because the upstream publishes a list and the
    // image generation pane draws one chip per word, but a person editing them wants a line.
    expect(box.value).toBe("watercolor, wc style");

    // A trailing comma is how every such box is typed, and the empty word it leaves must not
    // reach the catalogue — an empty chip is a trigger nobody can remove.
    await typeInto(box, " watercolor , wc style ,");
    apiJSON.mockResolvedValue(row({}));
    await click(saveButton());
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/models/watercolor-v2", "PUT", {
      trained_words: ["watercolor", "wc style"],
    });

    // Emptying the box is a real edit: it is the only way back from a word the publisher
    // recorded and this adapter's files do not use.
    apiJSON.mockClear();
    apiJSON.mockResolvedValue(row({}));
    await typeInto(host!.querySelector(".engines-model-trigger input")!, "");
    await click(saveButton());
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/models/watercolor-v2", "PUT", {
      trained_words: [],
    });
  });

  // A checkpoint has no trigger words at all, so the box would be one an operator can fill in
  // and nothing would ever read. Asserted on the MODEL tab, where a checkpoint is rendered —
  // the pairing the negative-prompt test next door had to learn.
  it("is not offered on a checkpoint row", async () => {
    api.mockResolvedValue({
      super_admin: true,
      engines: [
        row({
          provider: "comfy",
          base_models: ["sdxl"],
          has_models: true,
          model_rows: [
            { id: "sdxl-base-1.0", kind: "checkpoint", enabled: true, base_model: "sdxl" },
          ],
        }),
      ],
    });
    await mount();
    expect(host!.querySelector(".engines-model-id")?.textContent).toBe("sdxl-base-1.0");
    expect(host!.querySelector(".engines-model-negative")).not.toBeNull();
    expect(host!.querySelector(".engines-model-trigger")).toBeNull();
  });
});

// The window and the measured VRAM of one row (ADR 0079 live run, 2026-09-13). Editing them was
// the one correction the panel could not make, and the value it could not correct is the one
// that kills a GPU box four minutes into a cold start somebody paid for.
describe("the window and the VRAM measurement", () => {
  const llm = (over: Record<string, unknown> = {}) =>
    withClasses({ key: "llm", api: "chat", provider: "llama", ...over });

  /** The row's own editor, read from the ELEMENT rather than from the page. A `textContent`
   *  assertion over the whole screen goes on passing when this block disappears and something
   *  else on a long panel happens to carry the number. */
  const windowBox = (id: string) =>
    Array.from(ui().querySelectorAll(".engines-model"))
      .find((li) => li.querySelector(".engines-model-id")?.textContent === id)
      ?.querySelector(".engines-model-window") as HTMLElement | null;

  const field = (box: HTMLElement, label: string) =>
    Array.from(box.querySelectorAll("label"))
      .find((l) => l.querySelector("span")?.textContent === label)
      ?.querySelector("input") as HTMLInputElement | undefined;

  const type = async (input: HTMLInputElement | undefined, value: string) => {
    expect(input).toBeTruthy();
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      setter.call(input!, value);
      input!.dispatchEvent(new Event("input", { bubbles: true }));
    });
  };

  const saveIn = (box: HTMLElement, n: number) =>
    (Array.from(box.querySelectorAll("button")).filter(
      (b) => b.textContent === "保存",
    )[n] as HTMLButtonElement | undefined);

  it("sends the window as a pair, and the measurement on its own", async () => {
    api.mockResolvedValue({
      super_admin: true,
      engines: [
        llm({
          has_models: true,
          model_rows: [
            {
              id: "qwen3",
              enabled: true,
              context_tokens: 262144,
              max_output_tokens: 8192,
              vram_need_mib: 33792,
              vram_need_source: "weights_kv",
            },
          ],
        }),
      ],
    });
    await mount();
    const box = windowBox("qwen3");
    expect(box).toBeTruthy();
    // The stored values are what the boxes open with — this is a correction, not a blank form.
    expect(field(box!, "コンテキスト")?.value).toBe("262144");
    expect(field(box!, "最大出力")?.value).toBe("8192");

    apiJSON.mockResolvedValue(llm());
    await type(field(box!, "コンテキスト"), "16384");
    await click(saveIn(box!, 0));
    // 🔴 BOTH halves travel, even though only one was touched: the row answers a cap only
    // alongside a window, so a request that moved one alone leaves a row nothing can explain.
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/llm/models/qwen3", "PUT", {
      context_tokens: 16384,
      max_output_tokens: 8192,
    });

    apiJSON.mockClear();
    apiJSON.mockResolvedValue(llm());
    const box2 = windowBox("qwen3")!;
    await type(field(box2, "実測 VRAM（MiB）"), "19000");
    await click(saveIn(box2, 1));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/llm/models/qwen3", "PUT", {
      vram_mib: 19000,
    });
  });

  // 🔴 The meta line under the row, which is where the figure is read when nobody is editing.
  // `weights_kv` used to fall through to the MEASURED sentence, so a number the CP derived from a
  // GGUF header read as one an operator stood behind — and this is the only source that moves
  // when the window is edited, so it is the one where that claim misleads.
  it("words a weights-plus-KV floor as a floor in the row's meta line", async () => {
    api.mockResolvedValue({
      super_admin: true,
      engines: [
        llm({
          has_models: true,
          model_rows: [
            { id: "qwen3", enabled: true, vram_need_mib: 33792, vram_need_source: "weights_kv" },
            { id: "floored", enabled: true, vram_need_mib: 17408, vram_need_source: "floor" },
            { id: "measured", enabled: true, vram_mib: 8000, vram_need_mib: 8000, vram_need_source: "declared" },
          ],
        }),
      ],
    });
    await mount();
    const meta = (id: string) =>
      Array.from(ui().querySelectorAll(".engines-model"))
        .find((li) => li.querySelector(".engines-model-id")?.textContent === id)!
        .querySelector(".engines-model-meta")!.textContent!;
    expect(meta("qwen3")).toContain("VRAM 少なくとも 33792 MiB（重み＋KV キャッシュ）");
    // The two it must not be confused with. Without both, an implementation that prints one
    // sentence for every derived figure — or one word for every source — still passes.
    expect(meta("floored")).toContain("VRAM 少なくとも 17408 MiB（重みだけの下限）");
    expect(meta("measured")).toContain("VRAM 8000 MiB");
    expect(meta("measured")).not.toContain("少なくとも");
  });

  // 🔴 The demand and its SOURCE, beside the fields that move it — and taken from the server.
  // The KV geometry the figure is derived from is not on the wire, so a panel that recomputed it
  // would be a second formula disagreeing with the CP's on the one number being read.
  it("shows what the row needs now, and says a derived floor is not a measurement", async () => {
    api.mockResolvedValue({
      super_admin: true,
      engines: [
        llm({
          has_models: true,
          model_rows: [
            { id: "qwen3", enabled: true, context_tokens: 262144, vram_need_mib: 33792, vram_need_source: "weights_kv" },
            { id: "small", enabled: true, context_tokens: 4096, vram_need_mib: 8000, vram_need_source: "declared" },
          ],
        }),
      ],
    });
    await mount();
    const need = (id: string) => windowBox(id)!.querySelector(".engines-model-need")!.textContent;
    expect(need("qwen3")).toContain("33792");
    expect(need("qwen3")).toContain("重み＋KV キャッシュの下限");
    // The pair that makes the assertion mean something: the same sentence for a MEASURED row
    // would pass an implementation that prints one word for every source.
    expect(need("small")).toContain("実測");
    expect(need("qwen3")).not.toContain("実測");
  });

  // 🔥 The gap the live run fell into. An already-enabled row's window was checked by nobody, and
  // the panel cannot check it either — so the CP refuses and the panel turns that into the same
  // question the enable button asks.
  it("asks the CP's own question when raising the window is refused", async () => {
    api.mockResolvedValue({
      super_admin: true,
      engines: [
        llm({
          has_models: true,
          model_rows: [
            { id: "qwen3", enabled: true, context_tokens: 16384, max_output_tokens: 2048, vram_need_mib: 18432, vram_need_source: "weights_kv" },
          ],
        }),
      ],
    });
    await mount();
    apiJSON.mockResolvedValue({
      error: {
        code: "engine_vram_confirm",
        message: "qwen3 wants 33792 MiB of VRAM and the l4 class declares 21000 MiB",
      },
    });
    const box = windowBox("qwen3")!;
    await type(field(box, "コンテキスト"), "262144");
    await click(saveIn(box, 0));

    // The CP's sentence, not one rebuilt here: it names the demand of the row as EDITED, which
    // nothing on this row knows — `vram_need_mib` still describes the stored window.
    const ask = ui().querySelector(".engines-model-confirm")!;
    expect(ask.textContent).toContain("33792");

    apiJSON.mockClear();
    apiJSON.mockResolvedValue(llm());
    await click(
      Array.from(ask.querySelectorAll("button")).find(
        (b) => b.textContent === "承知のうえで有効にする",
      ) as HTMLButtonElement,
    );
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/llm/models/qwen3", "PUT", {
      context_tokens: 262144,
      max_output_tokens: 2048,
      confirm_vram: true,
    });
  });

  // A context window is an llm fact. An image checkpoint has a size list and a family instead —
  // but somebody can still have measured what it takes on the card.
  it("offers a context window to an llm row only, and the measurement to both", async () => {
    api.mockResolvedValue({
      super_admin: true,
      engines: [
        withClasses({ has_models: true, model_rows: [{ id: "sdxl-base-1.0", enabled: true }] }),
      ],
    });
    await mount();
    const box = windowBox("sdxl-base-1.0")!;
    expect(field(box, "コンテキスト")).toBeUndefined();
    expect(field(box, "実測 VRAM（MiB）")).toBeTruthy();
  });

  // An adapter is never loaded with a window of its own, and its weights ride on the checkpoint's
  // demand rather than declaring one.
  it("is not offered on a LoRA row", async () => {
    api.mockResolvedValue({
      super_admin: true,
      engines: [
        row({
          provider: "comfy",
          base_models: ["sdxl"],
          has_models: true,
          model_rows: [{ id: "watercolor-v2", kind: "lora", enabled: true, base_model: "sdxl" }],
        }),
      ],
    });
    await mount();
    // Onto the adapter tab first: an assertion made on the model tab would pass because the row
    // is not rendered at all, which is not what is being claimed.
    await click(tab("LoRA"));
    expect(ui().querySelector(".engines-model-id")?.textContent).toBe("watercolor-v2");
    expect(windowBox("watercolor-v2")).toBeFalsy();
  });

  // 🔥 The borrowed row is paired with an EXTERNAL one, not with a managed one. Every write route
  // answers 400 `engine_not_ours` for a borrowed catalogue (ADR 0079 decision 7) and for nothing
  // else — an implementation that dropped the editor from every unmanaged row would pass against
  // a managed control and take ADR 0076's LAN ComfyUI down with it.
  it("is absent on a borrowed row and present on an external one", async () => {
    const rows = (over: Record<string, unknown>) =>
      withClasses({
        has_models: true,
        model_rows: [{ id: "qwen3", enabled: true, context_tokens: 16384 }],
        ...over,
      });
    api.mockResolvedValue({
      super_admin: true,
      engines: [rows({ managed: false, lifecycle: "external", url: "http://10.0.0.5:8188" })],
    });
    await mount();
    expect(windowBox("qwen3")).toBeTruthy();

    act(() => root?.unmount());
    host?.remove();
    api.mockResolvedValue({
      super_admin: true,
      engines: [rows({ managed: false, lifecycle: "remote", url: "https://af.example.invalid" })],
    });
    await mount();
    expect(windowBox("qwen3")).toBeNull();
  });
});

// Whether this panel offers the way in to an ingest at all. What lies behind the press is the
// catalogue pane's (adminEngineCatalog.dom.test.tsx); the question here is which rows get a door.
describe("EngineModelsAdminView / the door to taking a file in", () => {
  const flux = (files: { s3Key: string; flag?: string }[]) =>
    row({
      provider: "comfy",
      base_models: ["sdxl", "sd35", "flux1"],
      file_flags: ["", "--diffusion-model", "--clip_l", "--t5xxl", "--vae"],
      has_models: true,
      model_rows: [{ id: "flux1-dev-fp8", enabled: true, base_model: "flux1", file_rows: files }],
    });

  const mountWith = async (engine: Record<string, unknown>) => {
    api.mockImplementation(async (p: string) =>
      p.endsWith("/ingest") ? { jobs: [] } : { super_admin: true, engines: [engine] },
    );
    await mount();
  };

  // 🔴 The pair that keeps this honest, and it is NOT "managed vs not".
  //
  // A borrowed role (ADR 0079) mirrors another deployment's catalogue and has no bucket on this
  // side, so nothing here may take a file in. An EXTERNAL engine — the LAN ComfyUI of ADR 0076 —
  // is also `managed: false`, and it stages files in this deployment's bucket like any other.
  // Pairing the borrowed row with a MANAGED one would pass for an implementation that hid the
  // form from every unmanaged engine, taking the LAN ComfyUI with it.
  it("offers the form to an external engine and not to a borrowed one", async () => {
    /** The way IN to every act this form offers. Collapsed until pressed, so this — not the
     *  open form — is what "the ingest is offered here" looks like on a freshly loaded panel. */
    const opener = () => ui().querySelector(".engines-open");

    const remount = async (over: Record<string, unknown>) => {
      act(() => root?.unmount());
      host?.remove();
      await mountWith({ ...flux([{ s3Key: "image/checkpoints/flux1-dev-fp8.safetensors" }]), ...over });
    };

    // The control, first: an ordinary row has it.
    await remount({});
    expect(opener()).toBeTruthy();

    // 🔴 The pair. `managed: false` with a lifecycle that is not `remote` is the LAN ComfyUI an
    // operator runs on their own box, and it stages files in THIS deployment's bucket.
    await remount({ managed: false, lifecycle: "external" });
    expect(opener()).toBeTruthy();

    // Borrowed: the catalogue is a mirror of the far fleet's and there is no bucket here.
    await remount({ managed: false, lifecycle: "remote", url: "https://far.invalid" });
    expect(opener()).toBeNull();
  });
});

// Forgetting a row of the ingest history (the delete the table never had).
//
// 🔴 The history is not only a progress display. Until a catalogue row points at the key a
// `done` job wrote, that row is the only place this deployment records that the file is in the
// bucket — the CP cannot list it (ADR 0072 review R3) — so what the panel says before deleting
// depends on whether anything still uses the file, and it never claims the file is there.
describe("EngineModelsAdminView / forgetting an ingest job", () => {
  const jobsAnswer = (jobs: Record<string, unknown>[], engine: Record<string, unknown> = {}) => {
    api.mockImplementation(async (p: string) =>
      p.endsWith("/ingest")
        ? { jobs }
        : {
            super_admin: true,
            engines: [row({ has_models: true, model_rows: [], ...engine })],
          },
    );
  };
  /** One job's <li>, found by the model id it names. By element, never by the panel's whole
   *  text: this screen holds two lists that both print model ids, and an assertion on the page
   *  passes while the wrong one carries the control. */
  const jobLi = (modelID: string) =>
    Array.from(ui().querySelectorAll("ul.engines-ingest-jobs li")).find(
      (li) => li.querySelector(".engines-model-id")?.textContent === modelID,
    ) as HTMLElement;
  const forgetButton = (li: HTMLElement) =>
    li.querySelector(".engines-ingest-job-forget") as HTMLButtonElement | null;

  // 🔴 A running job is not offered the button AND is told why. Deleting the row does not stop
  // the ECS task: it finishes and writes its catalogue row minutes later, with nothing on
  // screen that says where the model came from.
  it("does not offer to forget a job that is still running, and says why", async () => {
    jobsAnswer([
      { id: "j-live", model_id: "sd35-large", state: "running", source: "hf:x/y" },
      {
        id: "j-done",
        model_id: "sd35-medium",
        state: "done",
        source: "hf:x/z",
        s3_key: "image/checkpoints/sd3.5_medium.safetensors",
        key_used_by: "image/sd35-medium",
      },
    ]);
    await mount();

    const live = jobLi("sd35-large");
    expect(forgetButton(live)).toBeNull();
    expect(live.querySelector(".engines-ingest-job-live")?.textContent).toContain(
      "タスクは止まらず",
    );
    // Positive control in the same fixture: a finished job in the SAME list does get it, so the
    // absence above is the state and not a control that was never rendered at all.
    const done = jobLi("sd35-medium");
    expect(forgetButton(done)).toBeTruthy();
    expect(done.querySelector(".engines-ingest-job-live")).toBeNull();
  });

  // The key is shown at the moment it stops being recorded, and the deletion goes to the CP —
  // the panel then draws the SERVER's remaining list rather than one it edited itself, because
  // the CP is the only side that knows whether the delete was allowed.
  it("names the row that still uses the file, and takes the answer from the server", async () => {
    jobsAnswer([
      {
        id: "j-done",
        model_id: "clip-l",
        state: "done",
        source: "hf:comfyanonymous/flux_text_encoders/clip_l.safetensors",
        s3_key: "image/text_encoders/clip_l.safetensors",
        key_used_by: "image/flux1-dev-fp8",
      },
    ]);
    await mount();
    await click(forgetButton(jobLi("clip-l"))!);

    const confirm = jobLi("clip-l").querySelector(".engines-ingest-job-confirm") as HTMLElement;
    expect(confirm.querySelector(".engines-model-keys")?.textContent).toBe(
      "image/text_encoders/clip_l.safetensors",
    );
    // Named, not "still in use": an operator cannot act on an unnamed reference.
    expect(confirm.textContent).toContain("image/flux1-dev-fp8");
    // 🔴 And nothing here claims the FILE is there. The CP cannot look in the bucket, and a
    // purge deletes the bytes while leaving the job `done` for ever.
    expect(confirm.querySelector(".engines-ingest-job-ack")).toBeNull();

    apiJSON.mockResolvedValueOnce({ jobs: [] });
    const go = Array.from(confirm.querySelectorAll("button")).find(
      (b) => b.textContent === "消す",
    ) as HTMLButtonElement;
    expect(go.disabled).toBe(false);
    await click(go);
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/ingest/j-done", "DELETE");
    expect(ui().querySelector("ul.engines-ingest-jobs")).toBeNull();
  });

  // 🔴 The one job that is harder to forget: nothing in the catalogue points at its key, so
  // this row is the last written record of a file that is still being paid for — and the last
  // place that key can be picked from to register it again.
  it("asks a second time before forgetting the only record of a key", async () => {
    jobsAnswer([
      {
        id: "j-orphan",
        model_id: "forgotten-xl",
        state: "done",
        source: "hf:x/forgotten",
        s3_key: "image/checkpoints/forgotten.safetensors",
      },
    ]);
    await mount();
    await click(forgetButton(jobLi("forgotten-xl"))!);

    const confirm = jobLi("forgotten-xl").querySelector(
      ".engines-ingest-job-confirm",
    ) as HTMLElement;
    const go = Array.from(confirm.querySelectorAll("button")).find(
      (b) => b.textContent === "消す",
    ) as HTMLButtonElement;
    expect(go.disabled).toBe(true);
    // What it says is that no ROW points at the key — never that the file is or is not there.
    const warn = confirm.querySelector("p.form-err")!;
    expect(warn.textContent).toContain("カタログ行はありません");
    expect(warn.textContent).toContain("バケットを見られない");

    const ack = confirm.querySelector(".engines-ingest-job-ack input") as HTMLInputElement;
    await act(async () => {
      ack.click();
    });
    apiJSON.mockResolvedValueOnce({ jobs: [] });
    expect(
      (Array.from(
        jobLi("forgotten-xl").querySelectorAll(".engines-ingest-job-confirm button"),
      ).find((b) => b.textContent === "消す") as HTMLButtonElement).disabled,
    ).toBe(false);
  });

  // The borrowed catalogue is a MIRROR and this whole screen is read-only for it (ADR 0079
  // decision 7), so the new control is absent there too.
  //
  // 🔥 The row it is paired with is an EXTERNAL one — `managed: false` with a lifecycle that is
  // not `remote`. Paired with a managed row instead, an implementation that hid the button on
  // every unmanaged row would pass this test and take ADR 0076's LAN ComfyUI down with it.
  it("offers nothing on a borrowed catalogue, and everything on an external engine", async () => {
    const jobs = [
      { id: "j-done", model_id: "sd35-medium", state: "done", source: "hf:x/z",
        s3_key: "image/checkpoints/sd3.5_medium.safetensors" },
    ];
    jobsAnswer(jobs, { managed: false, lifecycle: "remote", url: "https://far.invalid" });
    await mount();
    expect(jobLi("sd35-medium")).toBeTruthy(); // the history itself is still readable
    expect(forgetButton(jobLi("sd35-medium"))).toBeNull();

    act(() => root?.unmount());
    host?.remove();
    // An external engine — this deployment's own ComfyUI on the LAN, not managed by ECS and not
    // borrowed. Its ingest history is its own, so the control is there.
    jobsAnswer(jobs, { managed: false, lifecycle: "external", url: "http://192.168.0.2:8188" });
    await mount();
    expect(forgetButton(jobLi("sd35-medium"))).toBeTruthy();
  });
});

// Registering a key the ingest history still holds.
//
// 🔴 "Take that file in again" is not an ingest. Forgetting a catalogue row leaves the object in
// the bucket unless `?purge=1` was ticked (measured on the dev deployment 2026-09-09: a 491 MB
// file outlived its row), so the bytes are staged and this is `POST /models`. The only thing
// that was ever missing is the list of keys, which lives in this history and was retyped by hand.
describe("EngineModelsAdminView / registering a key from the history", () => {
  const done = (over: Record<string, unknown> = {}) => ({
    id: "j-done",
    model_id: "sd35-medium",
    state: "done",
    source: "hf:stabilityai/stable-diffusion-3.5-medium/sd3.5_medium.safetensors",
    s3_key: "image/checkpoints/sd3.5_medium.safetensors",
    kind: "checkpoint",
    ...over,
  });
  const answer = (jobs: Record<string, unknown>[], engine: Record<string, unknown> = {}) => {
    api.mockImplementation(async (p: string) =>
      p.endsWith("/ingest")
        ? { jobs }
        : {
            super_admin: true,
            engines: [
              row({
                provider: "comfy",
                base_models: ["sdxl", "sd35", "flux1"],
                file_flags: ["", "--diffusion-model", "--clip_l", "--t5xxl", "--vae"],
                has_models: true,
                model_rows: [],
                ...engine,
              }),
            ],
          },
    );
  };
  const reuseButton = () =>
    ui().querySelector(".engines-ingest-job-reuse") as HTMLButtonElement | null;
  /** The input of the add form's row with this label — the form is a column of labelled rows,
   *  and reading it by index breaks the moment a field is added above. */
  const addField = (label: string) =>
    (Array.from(ui().querySelectorAll(".engines-model-add .engines-model-add-row")).find(
      (l) => l.querySelector("span")?.textContent === label,
    ) as HTMLElement)?.querySelector("input, select") as HTMLInputElement | HTMLSelectElement;

  it("fills the registration form from a finished job, and posts the source with it", async () => {
    answer([done()]);
    await mount();
    await click(reuseButton()!);

    // The key and the id are in the form; nothing has been posted.
    expect((addField("キー") as HTMLInputElement).value).toBe(
      "image/checkpoints/sd3.5_medium.safetensors",
    );
    expect((addField("id") as HTMLInputElement).value).toBe("sd35-medium");
    expect(apiJSON).not.toHaveBeenCalled();
    // Where it came from, shown rather than hidden — it is the one thing that will land on the
    // row and has no field of its own.
    expect(ui().querySelector(".engines-model-add-from-job")?.textContent).toContain(
      "hf:stabilityai/stable-diffusion-3.5-medium",
    );
    // 🔴 The FAMILY is not filled in, and the button is off until somebody picks one. A display
    // name from a repository produced rows that looked complete and refused to generate (ADR
    // 0072 P2 実機検証), so it is the one answer this form will not guess from a job.
    const goButton = () =>
      Array.from(ui().querySelectorAll(".engines-model-add-actions button")).find(
        (b) => b.textContent === "登録する",
      ) as HTMLButtonElement;
    expect((addField("ファミリー") as HTMLSelectElement).value).toBe("");
    expect(goButton().disabled).toBe(true);

    await act(async () => {
      const sel = addField("ファミリー") as HTMLSelectElement;
      const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!;
      setter.call(sel, "sd35");
      sel.dispatchEvent(new Event("change", { bubbles: true }));
    });
    apiJSON.mockResolvedValueOnce(row({ has_models: true, model_rows: [] }));
    await click(goButton());

    const [path, method, body] = apiJSON.mock.calls.at(-1)!;
    expect(path).toBe("api/admin/engines/image/models");
    expect(method).toBe("POST");
    expect(body).toMatchObject({
      id: "sd35-medium",
      kind: "checkpoint",
      base_model: "sd35",
      files: [{ flag: "", s3Key: "image/checkpoints/sd3.5_medium.safetensors", bytes: 0 }],
      source: "hf:stabilityai/stable-diffusion-3.5-medium/sd3.5_medium.safetensors",
    });
    // 🔴 No acceptance travels with the key. It is the record of a human act on the row the
    // ingest created (ADR 0072 decision 10), and the person registering it again may be
    // somebody else; the CP leaves the new row's acceptance empty and the panel must not try.
    expect(Object.keys(body as object).some((k) => k.startsWith("license_accepted"))).toBe(false);
  });

  // A part carries its file role, because the same key registered with no flag becomes the
  // CHECKPOINT of its own row — a row that loads a text encoder as a model and fails at the
  // next cold start, with nothing on screen connecting the two.
  it("carries what the file is within the model, and names who else uses the key", async () => {
    answer([
      done({
        model_id: "clip-l",
        s3_key: "image/text_encoders/clip_l.safetensors",
        file_flag: "--clip_l",
        key_used_by: "image/flux1-dev-fp8",
      }),
    ]);
    await mount();
    await click(reuseButton()!);
    expect((addField("役割") as HTMLSelectElement).value).toBe("--clip_l");
    // Not a refusal: SD3.5 and FLUX.1 read the same text encoders, so a shared key is normal
    // and the form says who has it instead of blocking.
    expect(ui().querySelector(".engines-model-add-from-job")?.textContent).toContain(
      "image/flux1-dev-fp8",
    );
    expect(reuseButton()).toBeTruthy();
  });

  // 🔴 The tab moves with the key. A LoRA prefilled into the checkpoint form would be registered
  // as a checkpoint — a row the engine can be told to start with and cannot load.
  it("opens the LoRA form for a job that took a LoRA in", async () => {
    answer([done({ model_id: "watercolor-v2", kind: "lora", s3_key: "image/loras/wc2.safetensors" })]);
    await mount();
    // Starts on the model tab, as the screen always does.
    const active = () =>
      (ui().querySelector(".seg-btn.active + .seg-btn.active, .seg-btn.active") &&
        Array.from(ui().querySelectorAll(".seg-btn.active")).map((b) => b.textContent)) || [];
    expect(active()).toContain("モデル");
    await click(reuseButton()!);
    expect(active()).toContain("LoRA");
    expect((addField("id") as HTMLInputElement).value).toBe("watercolor-v2");

    // And a tab pressed BY HAND drops the prefill: without that, coming back to the model list
    // later reopens the form still holding a key nobody chose there.
    await click(
      Array.from(ui().querySelectorAll(".seg-btn")).find(
        (b) => b.textContent === "モデル",
      ) as HTMLElement,
    );
    expect(ui().querySelector(".engines-model-add")).toBeNull();
  });

  // A failed job left part of a file at best, so registering its key would produce a row that
  // fails at load. It is still forgettable — that is the other half of this list.
  it("offers the key of a finished job only", async () => {
    answer([
      done({ id: "j-fail", model_id: "sd35-large", state: "failed", message: "sha256 mismatch" }),
      done(),
    ]);
    await mount();
    const li = (modelID: string) =>
      Array.from(ui().querySelectorAll("ul.engines-ingest-jobs li")).find(
        (n) => n.querySelector(".engines-model-id")?.textContent === modelID,
      ) as HTMLElement;
    expect(li("sd35-large").querySelector(".engines-ingest-job-reuse")).toBeNull();
    expect(li("sd35-large").querySelector(".engines-ingest-job-forget")).toBeTruthy();
    // Positive control: the finished job beside it has both.
    expect(li("sd35-medium").querySelector(".engines-ingest-job-reuse")).toBeTruthy();
  });

  // The reduced panel a granted tenant_admin gets (ADR 0072 open question 11): they may take a
  // model in and may forget their own history, and the bucket is the OPERATOR's — so the
  // registration form is not theirs and neither is the button that fills it. Pinned together,
  // because a button that only ever 403s is worse than no button.
  it("offers a tenant_admin the forget but not the registration", async () => {
    api.mockImplementation(async (p: string) =>
      p.endsWith("/ingest")
        ? { jobs: [done()] }
        : { super_admin: false, engines: [row({ has_models: true, model_rows: [] })] },
    );
    await mount();
    expect(reuseButton()).toBeNull();
    expect(ui().querySelector(".engines-ingest-job-forget")).toBeTruthy();
    // And the form the button would have filled is not on their screen either. By its own
    // label: `.engines-open` is worn by the INGEST opener too, which a granted tenant_admin
    // does get, so the class alone would have asserted nothing.
    expect(
      Array.from(ui().querySelectorAll("button")).find(
        (b) => b.textContent === "バケットのファイルを登録する",
      ),
    ).toBeUndefined();
  });

  // The borrowed catalogue is a MIRROR — every write route answers 400 there (ADR 0079 decision
  // 7) — so the key of a job in this deployment's history is not registrable into it.
  //
  // 🔥 Paired with an EXTERNAL row (`managed: false`, lifecycle not `remote`): paired with a
  // managed one, an implementation that hid the button on every unmanaged engine would pass and
  // take ADR 0076's LAN ComfyUI with it.
  it("is not offered on a borrowed catalogue, and is on an external engine", async () => {
    answer([done()], { managed: false, lifecycle: "remote", url: "https://far.invalid" });
    await mount();
    expect(reuseButton()).toBeNull();

    act(() => root?.unmount());
    host?.remove();
    answer([done()], { managed: false, lifecycle: "external", url: "http://192.168.0.2:8188" });
    await mount();
    expect(reuseButton()).toBeTruthy();
  });
});

// 🔴 The fault a row cannot state about itself: the checkpoint file carries no VAE, so the
// family's workflow has nothing to decode with. Every field on the row is filled in, the engine
// loads it, `generate_image` offers it by name — and every request dies inside ComfyUI after the
// box has paid a 1-2.5 minute checkpoint switch (measured twice on the real deployment).
//
// What this screen has to do about it is the whole of the feature: say it in a sentence, and
// offer ONE press. By hand it was a search, an ingest under the right file role and a retyped id
// that had to collide with an existing row before the act even appeared — walked once, in anger,
// by the person who owns this deployment.
describe("EngineModelsAdminView / a checkpoint with no VAE", () => {
  const broken = (over: Record<string, unknown> = {}) =>
    row({
      provider: "comfy",
      base_models: ["sdxl", "sd35"],
      file_flags: ["", "--vae"],
      model_rows: [
        {
          id: "waimature_v30",
          enabled: false,
          kind: "checkpoint",
          base_model: "sdxl",
          vae_missing: true,
          vae_fix: "stabilityai/sdxl-vae/sdxl_vae.safetensors",
          file_rows: [{ s3Key: "image/checkpoints/waimature_v30.safetensors", vae_bundled: "no" }],
          ...over,
        },
      ],
    });
  const button = (label: string) =>
    Array.from(ui().querySelectorAll("button")).find((b) => b.textContent === label) as
      | HTMLButtonElement
      | undefined;

  it("says what is wrong and fixes it in one press when the file is already here", async () => {
    api.mockResolvedValue({ super_admin: true, engines: [broken()] });
    await mount();
    expect(ui().textContent).toContain("VAE を同梱していません");

    // The plan first — what the press will do, before it does it.
    apiJSON.mockResolvedValue({
      vae_bundled: "no",
      action: "attach",
      staged: true,
      repo: "stabilityai/sdxl-vae",
      file: "sdxl_vae.safetensors",
      s3Key: "image/vae/sdxl_vae.safetensors",
    });
    await click(button("VAE を足す"));
    expect(apiJSON).toHaveBeenCalledWith(
      "api/admin/engines/image/models/waimature_v30/vae",
      "POST",
      { check: true },
    );
    // 🔴 The cheap case has to SAY it is cheap: the bytes are already this deployment's, so
    // there is no download, no minutes and no second licence to accept. A screen that asked for
    // a licence acceptance here would be asking about something that is not happening.
    expect(ui().textContent).toContain("もう置いてあります");
    expect(button("ライセンスに同意して取り込む")).toBeFalsy();

    apiJSON.mockResolvedValue({ action: "attached", vae_bundled: "no" });
    await click(button("この行に足す"));
    expect(apiJSON).toHaveBeenCalledWith(
      "api/admin/engines/image/models/waimature_v30/vae",
      "POST",
      {},
    );
    expect(ui().textContent).toContain("有効にできます");
  });

  // The other half of the same press: the file is not here, so it is a download under a licence
  // — and the licence and the size are on screen BEFORE the button that accepts them, which is
  // the rule the ingest form already follows.
  it("names the licence and the size before accepting a download", async () => {
    api.mockResolvedValue({ super_admin: true, engines: [broken()] });
    await mount();
    apiJSON.mockResolvedValue({
      vae_bundled: "no",
      action: "ingest",
      repo: "stabilityai/sdxl-vae",
      file: "sdxl_vae.safetensors",
      bytes: 334641162,
      license: "mit",
    });
    await click(button("VAE を足す"));
    expect(ui().textContent).toContain("stabilityai/sdxl-vae/sdxl_vae.safetensors");
    expect(ui().textContent).toContain("335 MB");
    expect(ui().textContent).toContain("mit");

    apiJSON.mockResolvedValue({ action: "job_started", vae_bundled: "no" });
    await click(button("ライセンスに同意して取り込む"));
    expect(apiJSON).toHaveBeenCalledWith(
      "api/admin/engines/image/models/waimature_v30/vae",
      "POST",
      { licenseAccepted: true },
    );
    expect(ui().textContent).toContain("取り込みを開始しました");
  });

  // 🔴 Measured on af-sandbox (2026-09-13): the row's source is a Civitai version, Civitai
  // answered 503, and the whole fix was refused — although what it downloads comes from Hugging
  // Face and never touches that source. The diagnosis must not take the remedy down with it.
  it("goes on when the source will not answer again, and says the plan rests on the old reading", async () => {
    api.mockResolvedValue({ super_admin: true, engines: [broken()] });
    await mount();
    apiJSON.mockResolvedValue({
      vae_bundled: "no",
      action: "attach",
      staged: true,
      repo: "stabilityai/sdxl-vae",
      file: "sdxl_vae.safetensors",
      recheck_failed: "civitai.com answered 503 Service Unavailable",
    });
    await click(button("VAE を足す"));
    expect(ui().textContent).toContain("もう一度読めませんでした");
    expect(ui().textContent).toContain("503");
    // …and the press still goes through, carrying the operator's "I know" so the CP does not
    // refuse the same thing twice.
    apiJSON.mockResolvedValue({ action: "attached", vae_bundled: "no" });
    await click(button("この行に足す"));
    expect(apiJSON).toHaveBeenCalledWith(
      "api/admin/engines/image/models/waimature_v30/vae",
      "POST",
      { force: true },
    );
  });

  // With nothing recorded either, the deployment refuses to spend 335 MB on a guess — and offers
  // the one escape it has, to the person who has watched the row fail in the engine.
  it("offers the escape when nothing is known and the source is down", async () => {
    api.mockResolvedValue({ super_admin: true, engines: [broken()] });
    await mount();
    apiJSON.mockResolvedValue({
      error: { code: "engine_vae_unreadable", message: "civitai.com answered 503 Service Unavailable" },
    });
    await click(button("VAE を足す"));
    expect(ui().textContent).toContain("503");
    const forceBtn = button("上流が答えないので、承知のうえで足す");
    expect(forceBtn).toBeTruthy();

    apiJSON.mockResolvedValue({
      vae_bundled: "", action: "ingest", repo: "stabilityai/sdxl-vae", file: "sdxl_vae.safetensors",
      bytes: 334641162, license: "mit",
    });
    await click(forceBtn);
    expect(apiJSON).toHaveBeenLastCalledWith(
      "api/admin/engines/image/models/waimature_v30/vae",
      "POST",
      { check: true, force: true },
    );
  });

  // 🔴 When the source will not answer, the scan has no verdict — and silence there looks
  // exactly like a healthy row. The panel says what happened and offers the one escape, instead
  // of leaving a checkpoint that cannot generate looking fine (af-sandbox, Civitai 503).
  it("says so when the header could not be checked at all, and does not ask that source again", async () => {
    api.mockResolvedValue({
      super_admin: true,
      engines: [broken({ vae_missing: false, vae_unread: true, vae_fix: undefined })],
    });
    apiJSON.mockResolvedValue({
      read: [{ id: "waimature_v30", vae_bundled: "", unreadable: "civitai.com answered 503 Service Unavailable" }],
    });
    await mount();
    expect(ui().textContent).toContain("確認できませんでした");
    expect(ui().textContent).toContain("503");
    // The plain "add a VAE" press is not offered: it would be one more request to the host that
    // just refused. What is offered is the deliberate one.
    expect(button("VAE を足す")).toBeFalsy();
    expect(button("上流が答えないので、承知のうえで足す")).toBeTruthy();
  });

  // 🔴 A row nobody has READ is not a broken row. Every checkpoint taken in before this existed
  // is in that state, so the panel answers the question itself — one call for the catalogue, not
  // one per row — and draws no mark until there is an answer.
  it("reads the unread headers once and marks nothing until it has an answer", async () => {
    api.mockResolvedValue({
      super_admin: true,
      engines: [
        broken({ vae_missing: false, vae_unread: true, vae_fix: undefined }),
      ],
    });
    apiJSON.mockResolvedValue({ read: [] });
    await mount();
    expect(ui().textContent).not.toContain("VAE を同梱していません");
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/models/vae-scan", "POST", {});
    // Once. The rows are replaced on every load, so a scan that depended on their identity
    // would read the upstream again for ever.
    const scans = () =>
      apiJSON.mock.calls.filter((c) => String(c[0]).endsWith("/models/vae-scan")).length;
    expect(scans()).toBe(1);
    await act(async () => {
      await Promise.resolve();
    });
    expect(scans()).toBe(1);
  });
});
