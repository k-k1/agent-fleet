// エージェントへの指示タブ（docs/60）。ここが守るべきは「利用者が画面から誤解しないこと」
// の 3 点で、それだけを jsdom で押さえる:
//   ① 未対応の kind が**行として残り**、理由が読めること（黙って消えると対応漏れに見える）
//   ② 「書いた」と「効いている」が別に出ること（保存できても効かない場合がある）
//   ③ 上限超過では保存させないこと（Agent 側も 400 で弾くが、画面で先に止める）
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  errText: (e: { message?: string }) => e?.message || "",
  isTransientErr: () => false,
  raw: () => Promise.resolve(new Response("")),
}));
vi.mock("../../core/store/workspace.ts", () => ({
  useWorkspaceStore: (sel: (s: unknown) => unknown) =>
    sel({ state: "running", start: () => {} }),
  wsStartBusy: () => false,
}));
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

import { InstructionsTab } from "./InstructionsTab.tsx";

const payload = {
  text: "always speak Japanese\n",
  bytes: 21,
  max_bytes: 64,
  enabled: true,
  path: "/home/dev/.config/agent-fleet/user-notes.md",
  fleet_bytes: 29521,
  targets: [
    {
      kind: "claude",
      supported: true,
      on: true,
      applied: true,
      delivery: "file",
      path: "/var/lib/af/claude/CLAUDE.md",
    },
    {
      kind: "opencode",
      supported: true,
      on: true,
      applied: false,
      delivery: "config",
      path: "/home/dev/.config/agent-fleet/instructions/opencode.md",
      error: "config_unreadable",
    },
    {
      kind: "cursor",
      supported: false,
      on: false,
      applied: false,
      reason: "no_user_scope",
    },
  ],
};

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<InstructionsTab />);
  });
  // useRetryLoad の解決を1回流す。
  await act(async () => {
    await Promise.resolve();
  });
}

beforeEach(() => {
  api.mockReset();
  apiJSON.mockReset();
  api.mockResolvedValue(payload);
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

const rows = () =>
  Array.from(
    document.querySelectorAll<HTMLTableRowElement>(".instr-targets tr"),
  );

describe("InstructionsTab", () => {
  it("未対応の kind を消さずに理由付きで出す", async () => {
    await mount();
    expect(rows()).toHaveLength(3);
    const unsupported = document.querySelector(".instr-unsupported");
    expect(unsupported).not.toBeNull();
    // 理由が文言として出ていること（コードそのままでは意味が伝わらない）。
    expect(
      unsupported?.querySelector(".instr-badge")?.textContent,
    ).toBeTruthy();
    expect(unsupported?.textContent).not.toContain("no_user_scope");
    // 未対応の行にトグルを出さない（押せる顔をしていると「効くはず」に見える）。
    expect(unsupported?.querySelector(".choice-seg")).toBeNull();
  });

  it("書いた／効いているを行ごとに出し分ける", async () => {
    await mount();
    expect(document.querySelectorAll(".instr-ok")).toHaveLength(1); // claude
    const fail = document.querySelector(".instr-fail");
    expect(fail).not.toBeNull();
    expect(fail?.textContent).toBeTruthy();
    expect(fail?.textContent).not.toContain("config_unreadable"); // 生コードを見せない
  });

  it("上限を超えたら保存を止める", async () => {
    await mount();
    const ta = document.querySelector<HTMLTextAreaElement>("#instr-body")!;
    const setter = Object.getOwnPropertyDescriptor(
      HTMLTextAreaElement.prototype,
      "value",
    )!.set!;
    await act(async () => {
      setter.call(ta, "x".repeat(payload.max_bytes + 1));
      ta.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const save = Array.from(
      document.querySelectorAll<HTMLButtonElement>(".instr-actions .ui-btn"),
    )[0];
    expect(save.disabled).toBe(true);
    expect(document.querySelector(".instr-over")).not.toBeNull();
    expect(apiJSON).not.toHaveBeenCalled();
  });

  it("保存すると本文を PUT する", async () => {
    apiJSON.mockResolvedValue({ ...payload, text: "short\n" });
    await mount();
    const ta = document.querySelector<HTMLTextAreaElement>("#instr-body")!;
    const setter = Object.getOwnPropertyDescriptor(
      HTMLTextAreaElement.prototype,
      "value",
    )!.set!;
    await act(async () => {
      setter.call(ta, "short\n");
      ta.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const save = Array.from(
      document.querySelectorAll<HTMLButtonElement>(".instr-actions .ui-btn"),
    )[0];
    await act(async () => {
      save.click();
    });
    expect(apiJSON).toHaveBeenCalledWith("api/user-notes", "PUT", {
      text: "short\n",
    });
  });
});
