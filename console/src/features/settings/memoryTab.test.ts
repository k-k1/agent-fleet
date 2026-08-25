import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { ja } from "../../lib/i18n/locales/ja.ts";
import { en } from "../../lib/i18n/locales/en.ts";

// エージェントメモリタブ（docs/39 P2）の配線を、DOM を起こさずソース側から固定する。
// 本文の描画そのものより、**壊れると 404 / 空タブ / 未訳になる継ぎ目**を見るのが目的:
//   ① 設定モーダルのレール登録と描画分岐（片方だけだと「押せるが真っ白」になる）
//   ② 叩く REST が CP の許可リストに載っていること（載せ忘れ = FE から 404。既知の罠）
//   ③ 使う i18n キーが ja/en 双方にあること（動的な mem.trigger_* を含む）
const read = (rel: string) => readFileSync(fileURLToPath(new URL(rel, import.meta.url)), "utf8");
const tab = read("./MemoryTab.tsx");
const dialog = read("./SettingsDialog.tsx");
const cpRoutes = read("../../../../control-plane/routes.go");
const agentRoutes = read("../../../../workspace/agent/routes.go");

describe("設定モーダルのエージェントメモリタブ", () => {
  it("ワークスペース群のレールと描画分岐の両方に載っている", () => {
    expect(dialog).toContain('["memory", "set.tab_memory"]');
    expect(dialog).toContain('{section === "memory" && <MemoryTab />}');
    // レール登録はワークスペース群の中（個人設定・接続ではない）。
    const workspaceGroup = dialog.slice(dialog.indexOf('key: "workspace"'), dialog.indexOf("];"));
    expect(workspaceGroup).toContain('["memory", "set.tab_memory"]');
  });

  it("叩く REST は CP と Agent の両方に登録されている（登録漏れ = FE から 404）", () => {
    const paths = [...tab.matchAll(/"(api\/agents\/memory\/[a-z]+)/g)].map((m) => m[1]);
    expect(new Set(paths)).toEqual(
      new Set([
        "api/agents/memory/roots",
        "api/agents/memory/snapshots",
        "api/agents/memory/diff",
        "api/agents/memory/tree",
        "api/agents/memory/restore",
        "api/agents/memory/settings",
        // P3。import/apply は正規表現が最終セグメント前で切るので "import" で代表される
        // （CP 側の登録は下の contains で /api/agents/memory/import を含むことで見る）。
        "api/agents/memory/export",
        "api/agents/memory/import",
      ]),
    );
    for (const p of new Set(paths)) {
      expect(cpRoutes, `${p} は control-plane/routes.go に未登録`).toContain("/" + p);
      expect(agentRoutes, `${p} は workspace/agent/routes.go に未登録`).toContain(
        p.replace(/^api\//, "/"),
      );
    }
  });

  it("使う i18n キーが ja/en 双方に揃っている", () => {
    const used = [...tab.matchAll(/\btr\("([^"]+)"/g)].map((m) => m[1]);
    expect(used.length).toBeGreaterThan(10);
    for (const key of new Set(used)) {
      expect(ja, `ja に ${key} がない`).toHaveProperty(key);
      expect(en, `en に ${key} がない`).toHaveProperty(key);
    }
  });

  it("import/apply も両側に登録されている（1 本だけ抜ける漏れ方をする）", () => {
    expect(tab).toContain("api/agents/memory/import/apply");
    expect(cpRoutes).toContain("/api/agents/memory/import/apply");
    expect(agentRoutes).toContain("/agents/memory/import/apply");
  });

  it("書き出し / 取り込みの失敗は原因（サーバの message）まで出す", () => {
    // `memory_import_failed` のような汎用コードは i18n が定型文へ畳むので、errText の
    // ままだと「取り込みに失敗しました」だけが出て原因が消える。実際にこれで
    // 「ENOENT（live のルート未作成）」が画面からも調査からも見えなくなった。
    const failures = [...tab.matchAll(/toast\((?:body\?\.error \? )?errDetail\(/g)];
    expect(failures.length).toBeGreaterThanOrEqual(4);
    expect(tab).toContain('body?.error ? errDetail(body.error) : tr("mem.import_failed")');
    expect(tab).toContain('body?.error ? errDetail(body.error) : tr("mem.export_failed")');
  });

  it("移設（履歴ごと取り込み）の mode 値が Agent 側の定数と一致する", () => {
    // apply は REST を増やさず mode 1 キーで分岐する（新 REST は CP の許可リスト登録漏れ
    // という既知の罠を踏むため）。綴りがずれると 400 になるので両側を突き合わせる。
    const importGo = read("../../../../workspace/agent/memory_import.go");
    expect(importGo).toContain('memoryImportModeMigrate = "migrate"');
    expect(importGo).toContain('memoryImportModeReplace = "replace"');
    expect(tab).toContain('useState<"replace" | "migrate">("replace")');
    expect(tab).toMatch(/mode,\s*\n\s*scope:/); // apply 本文に mode を載せている
    // 移設は bundle のときだけ出す（tar は 1 世代しか無く、選ぶと履歴を捨てるだけになる）。
    expect(tab).toContain('preview.format === "bundle"');
  });

  it("契機バッジのキーは Agent の AF-Trigger 値を網羅する", () => {
    // Agent 側の定数（memory_snapshot.go）と 1:1。"-" は "_" に置換して引く。
    for (const trigger of ["auto", "manual", "pre-restore", "restore", "import"]) {
      const key = "mem.trigger_" + trigger.replace(/-/g, "_");
      expect(ja, `ja に ${key} がない`).toHaveProperty(key);
      expect(en, `en に ${key} がない`).toHaveProperty(key);
    }
  });
});
