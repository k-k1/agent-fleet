// errDetail の契約。`*_failed` のような汎用コードは i18n の定型文しか出さないため、
// サーバが返した原因（message）が落ちると、利用者の環境でしか再現しない失敗を
// 追う手がかりが画面から消える（実例: エージェントメモリの取り込み適用が
// 「取り込みに失敗しました」だけを出して原因を隠した）。
import { beforeAll, describe, expect, it, vi } from "vitest";

// client.ts は window.fetch を束縛し document.baseURI を読むので、先に stub する。
const values = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
});
vi.stubGlobal("sessionStorage", {
  getItem: () => null,
  setItem: () => {},
  removeItem: () => {},
});
vi.stubGlobal("document", { baseURI: "http://localhost/" });
vi.stubGlobal("window", { fetch: vi.fn(), addEventListener: () => {}, removeEventListener: () => {} });

let client: typeof import("./client.ts");

beforeAll(async () => {
  client = await import("./client.ts");
});

describe("errDetail", () => {
  it("翻訳のあるコードでも、サーバの message を併記する", () => {
    const head = client.errText({ code: "memory_import_failed" });
    const got = client.errDetail({
      code: "memory_import_failed",
      message: "apply claude/projects/-x: mkdir /var/lib/af/claude/projects/-x: no such file or directory",
    });
    expect(got.startsWith(head + ": ")).toBe(true);
    expect(got).toContain("no such file or directory");
  });

  it("message が無ければ定型文だけ（区切りを付けない）", () => {
    const got = client.errDetail({ code: "memory_import_failed" });
    expect(got).toBe(client.errText({ code: "memory_import_failed" }));
    expect(got.endsWith(":")).toBe(false);
  });

  it("未訳コードは errText と同じ（message をそのまま出すので重ねない）", () => {
    const e = { code: "no_such_code_zzz", message: "boom" };
    expect(client.errDetail(e)).toBe(client.errText(e));
  });

  it("文字列はそのまま", () => {
    expect(client.errDetail("plain")).toBe("plain");
  });
});
