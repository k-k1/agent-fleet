// テナントの既定のマシン種別（docs/log/70 §70.4.3 / §70.10）。
//
// 押さえるのは 3 点。
//   ① クラスが無いデプロイでは面ごと出ない。「選択肢の無い選択肢」を置かない。
//   ② 並べるのはサーバが返した集合そのもの。ここで全クラスから絞り直すと、
//      super_admin の許可一覧を画面が二重実装することになる。
//   ③ 「テナントの既定を消す」＝ デプロイ既定へ戻す、が空文字の PUT で表現される。
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
}));
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

import { TenantMachineView } from "./tenantMachine.tsx";

const CLASSES = [
  {
    id: "standard",
    label: "標準（Intel）",
    arch: "x86_64",
    slots: [
      { instance_type: "m7i.large", mem_mib: 8192, vcpu: 2 },
      { instance_type: "m7i.2xlarge", mem_mib: 32768, vcpu: 8 },
    ],
  },
  {
    id: "arm",
    label: "省コスト（Arm）",
    arch: "arm64",
    slots: [{ instance_type: "m7g.large", mem_mib: 8192, vcpu: 2 }],
  },
];

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<TenantMachineView slug="acme" />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const chips = () =>
  Array.from(document.querySelectorAll<HTMLButtonElement>(".machine-picker .le-presets .chip"));
const chipWith = (t: string) => chips().find((b) => (b.textContent || "").trim() === t);

beforeEach(() => {
  apiJSON.mockResolvedValue({});
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.clearAllMocks();
});

describe("テナントの既定のマシン種別", () => {
  it("クラスの無いデプロイでは面ごと描かない", async () => {
    api.mockResolvedValue({ tenant: "acme", editable: false, classes: [] });
    await mount();
    expect(document.querySelector(".machine-picker")).toBeNull();
  });

  it("サーバが返した集合をそのまま並べ、いまの既定に印を付ける", async () => {
    api.mockResolvedValue({
      tenant: "acme",
      slot_class: "arm",
      classes: CLASSES,
      default_slot_class: "standard",
      editable: true,
    });
    await mount();
    expect(chips().map((b) => (b.textContent || "").trim())).toEqual([
      "デプロイの既定",
      "標準（Intel）",
      "省コスト（Arm）",
    ]);
    expect(chipWith("省コスト（Arm）")!.className).toContain("on");
    expect(chipWith("デプロイの既定")!.className).not.toContain("on");
    // それぞれが何を買うのかを 1 行で出す（「載る箱」の話は運用者の語彙で書けない）。
    const specs = Array.from(document.querySelectorAll(".machine-specs li")).map((e) => e.textContent || "");
    expect(specs[0]).toContain("m7i.large–m7i.2xlarge");
    expect(specs[1]).toContain("arm64");
  });

  it("「デプロイの既定」は空文字で保存する（テナント既定を消す）", async () => {
    api.mockResolvedValue({
      tenant: "acme",
      slot_class: "arm",
      classes: CLASSES,
      default_slot_class: "standard",
      editable: true,
    });
    apiJSON.mockResolvedValue({ tenant: "acme", slot_class: "" });
    await mount();
    await act(async () => chipWith("デプロイの既定")!.click());
    expect(apiJSON).toHaveBeenCalledWith("api/admin/tenants/acme/slot-class", "PUT", { slot_class: "" });
  });
});
