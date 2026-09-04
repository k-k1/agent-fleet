import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { ja } from "../../../lib/i18n/locales/ja.ts";
import { en } from "../../../lib/i18n/locales/en.ts";

// Pins the wiring of the agent memory tab (docs/log/39 P2) from the source, without a DOM.
// The target is not the rendering but the seams that turn into a 404, an empty tab or an
// untranslated label when they break:
//   1. rail registration and the render branch in the settings modal (one without the other
//      gives a tab you can click that stays blank)
//   2. every REST path it calls is on the CP allow list (a missing entry is a 404 from the FE)
//   3. every i18n key it uses exists in both ja and en (including the dynamic mem.trigger_*)
const read = (rel: string) => readFileSync(fileURLToPath(new URL(rel, import.meta.url)), "utf8");
// Because this check reads source text, it depends on how many files the tab is split into.
// The memory tab is MemoryTab (shell, root overview, history) / memoryRestore (restore) /
// memoryTransfer (export and import), so the family is concatenated and read as one tab body.
// Add any new file here: forget one and the check that is supposed to watch for unregistered
// REST paths silently stops covering it.
const tab = ["./MemoryTab.tsx", "./memoryTypes.ts", "./memoryRestore.tsx", "./memoryTransfer.tsx"]
  .map(read)
  .join("\n");
const dialog = read("../SettingsDialog.tsx");
const cpRoutes = read("../../../../../control-plane/routes.go");
const agentRoutes = read("../../../../../workspace/agent/routes.go");

describe("agent memory tab in the settings modal", () => {
  it("is registered in both the workspace-group rail and the render branch", () => {
    expect(dialog).toContain('["memory", "set.tab_memory"]');
    expect(dialog).toContain('{section === "memory" && <MemoryTab />}');
    // The rail entry belongs to the workspace group, not personal settings or connections.
    const workspaceGroup = dialog.slice(dialog.indexOf('key: "workspace"'), dialog.indexOf("];"));
    expect(workspaceGroup).toContain('["memory", "set.tab_memory"]');
  });

  it("registers every REST path it calls in both CP and Agent (a gap is a 404 from the FE)", () => {
    const paths = [...tab.matchAll(/"(api\/agents\/memory\/[a-z]+)/g)].map((m) => m[1]);
    expect(new Set(paths)).toEqual(
      new Set([
        "api/agents/memory/roots",
        "api/agents/memory/snapshots",
        "api/agents/memory/diff",
        "api/agents/memory/tree",
        "api/agents/memory/restore",
        "api/agents/memory/settings",
        // P3. The regex stops before the last segment, so import/apply is represented by
        // "import" (the CP entry is checked below by containing /api/agents/memory/import).
        "api/agents/memory/export",
        "api/agents/memory/import",
      ]),
    );
    for (const p of new Set(paths)) {
      expect(cpRoutes, `${p} is not registered in control-plane/routes.go`).toContain("/" + p);
      expect(agentRoutes, `${p} is not registered in workspace/agent/routes.go`).toContain(
        p.replace(/^api\//, "/"),
      );
    }
  });

  it("has every i18n key it uses in both ja and en", () => {
    const used = [...tab.matchAll(/\btr\("([^"]+)"/g)].map((m) => m[1]);
    expect(used.length).toBeGreaterThan(10);
    for (const key of new Set(used)) {
      expect(ja, `ja is missing ${key}`).toHaveProperty(key);
      expect(en, `en is missing ${key}`).toHaveProperty(key);
    }
  });

  it("registers import/apply on both sides too (it is the one path that goes missing alone)", () => {
    expect(tab).toContain("api/agents/memory/import/apply");
    expect(cpRoutes).toContain("/api/agents/memory/import/apply");
    expect(agentRoutes).toContain("/agents/memory/import/apply");
  });

  it("shows the cause (the server's message) when export or import fails", () => {
    // i18n folds a generic code such as `memory_import_failed` into boilerplate, so plain
    // errText prints only "import failed" and the cause disappears — that is how an ENOENT
    // (the live root was never created) became invisible on screen and in investigation.
    const failures = [...tab.matchAll(/toast\((?:body\?\.error \? )?errDetail\(/g)];
    expect(failures.length).toBeGreaterThanOrEqual(4);
    expect(tab).toContain('body?.error ? errDetail(body.error) : tr("mem.import_failed")');
    expect(tab).toContain('body?.error ? errDetail(body.error) : tr("mem.export_failed")');
  });

  it("uses mode values for migration (import with history) that match the Agent constants", () => {
    // apply branches on the single mode key rather than adding a REST path, because a new
    // path risks the known trap of never reaching the CP allow list. A spelling drift turns
    // into a 400, so both sides are compared.
    const importGo = read("../../../../../workspace/agent/internal/memoryx/memory_import.go");
    expect(importGo).toContain('memoryImportModeMigrate = "migrate"');
    expect(importGo).toContain('memoryImportModeReplace = "replace"');
    expect(tab).toContain('useState<"replace" | "migrate">("replace")');
    expect(tab).toMatch(/mode,\s*\n\s*scope:/); // the apply body carries mode
    // Migration is offered only for a bundle: a tar holds one generation, so choosing it
    // would merely throw the history away.
    expect(tab).toContain('preview.format === "bundle"');
  });

  it("covers every Agent AF-Trigger value with a trigger-badge key", () => {
    // One to one with the Agent constants (memory_snapshot.go); "-" becomes "_" in the key.
    for (const trigger of ["auto", "manual", "pre-restore", "restore", "import"]) {
      const key = "mem.trigger_" + trigger.replace(/-/g, "_");
      expect(ja, `ja is missing ${key}`).toHaveProperty(key);
      expect(en, `en is missing ${key}`).toHaveProperty(key);
    }
  });
});
