// Settings > Skills (#2792). Pinned here: the entries the session's picker drops stay visible
// and say why (shadowed, not user-invocable, Fleet-managed), a kind with no skills still shows
// the folders it reads, and opening an entry fetches exactly its own file.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  errText: (e: { message?: string }) => e?.message || "",
  isTransientErr: () => false,
}));
vi.mock("../../../core/store/workspace.ts", () => ({
  useWorkspaceStore: (sel: (s: unknown) => unknown) => sel({ state: "running", start: () => {} }),
  wsStartBusy: () => false,
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
// The real viewer pulls in half the app's stores; what matters here is which text reaches it.
vi.mock("../../viewer/MarkdownView.tsx", () => ({
  MarkdownView: ({ source }: { source: string }) => <div className="md-stub">{source}</div>,
}));

import { SkillsTab } from "./SkillsTab.tsx";
import { t } from "../../../lib/i18n/index.ts";

const C = "/var/lib/af/claude";
const catalog = {
  kinds: [
    {
      kind: "claude",
      roots: [
        { dir: C + "/skills", scope: "user", form: "skills", exists: true },
        { dir: C + "/commands", scope: "user", form: "commands", exists: false },
      ],
      skills: [
        {
          name: "handoff",
          description: "hand over",
          type: "skill",
          scope: "user",
          root: C + "/skills",
          path: C + "/skills/handoff/SKILL.md",
          userInvocable: true,
          files: ["refs/notes.md"],
        },
        {
          name: "handoff",
          type: "command",
          scope: "user",
          root: C + "/commands",
          path: C + "/commands/handoff.md",
          userInvocable: true,
          shadowedBy: C + "/skills/handoff/SKILL.md",
        },
        { name: "quiet", type: "skill", scope: "user", path: C + "/skills/quiet/SKILL.md", userInvocable: false },
        { name: "af-build", type: "skill", scope: "fleet", path: C + "/skills/af-build/SKILL.md", userInvocable: true },
        { name: "simplify", type: "skill", scope: "cli", userInvocable: true },
      ],
    },
    {
      kind: "muse",
      roots: [{ dir: "/home/dev/.config/muse/skills", scope: "user", form: "skills", exists: false }],
      skills: [],
    },
  ],
};

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function flush() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<SkillsTab />);
  });
  await flush();
}

const rowNamed = (name: string, type = "skill") =>
  Array.from(document.querySelectorAll<HTMLButtonElement>(".skills-row")).find(
    (b) =>
      b.querySelector(".skills-name")?.textContent === name &&
      (type === "command") === (b.textContent ?? "").includes(t("skills.form_commands")),
  )!;

beforeEach(() => {
  localStorage.clear();
  api.mockReset();
  api.mockImplementation(async (url: string) => {
    if (url === "api/skills/catalog") return catalog;
    if (url.startsWith("api/skills/file?path=")) {
      const p = decodeURIComponent(url.slice("api/skills/file?path=".length));
      return { path: p, content: "content of " + p };
    }
    return { error: { message: "unexpected " + url } };
  });
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("SkillsTab", () => {
  it("lists every entry with the reason it would not run", async () => {
    await mount();
    expect(api).toHaveBeenCalledWith("api/skills/catalog");
    const groups = Array.from(document.querySelectorAll(".skills-group h4")).map((h) => h.firstChild?.textContent?.trim());
    expect(groups).toEqual([t("skills.scope_user"), t("skills.scope_fleet"), t("skills.scope_cli")]);
    const cmd = rowNamed("handoff", "command");
    expect(cmd.classList.contains("skills-shadowed")).toBe(true);
    expect(cmd.textContent).toContain(t("skills.shadowed"));
    expect(rowNamed("handoff").classList.contains("skills-shadowed")).toBe(false);
    expect(rowNamed("quiet").textContent).toContain(t("skills.not_invocable"));
    expect(document.body.textContent).toContain(t("skills.fleet_hint"));
    // A missing root stays listed, dimmed and labelled, so the member sees where to create it.
    const missing = document.querySelector(".skills-roots .skills-missing");
    expect(missing?.textContent).toContain(C + "/commands");
    expect(missing?.textContent).toContain(t("skills.root_missing"));
  });

  it("opens an entry's own file, then a sibling file of its folder", async () => {
    await mount();
    await act(async () => rowNamed("handoff").click());
    await flush();
    expect(api).toHaveBeenCalledWith("api/skills/file?path=" + encodeURIComponent(C + "/skills/handoff/SKILL.md"));
    expect(document.querySelector(".md-stub")?.textContent).toBe("content of " + C + "/skills/handoff/SKILL.md");
    expect(document.querySelector(".skills-where code")?.textContent).toBe(C + "/skills/handoff/SKILL.md");

    const sibling = Array.from(document.querySelectorAll<HTMLButtonElement>(".skills-files .ui-btn")).find(
      (b) => b.textContent === "refs/notes.md",
    )!;
    await act(async () => sibling.click());
    await flush();
    expect(document.querySelector(".md-stub")?.textContent).toBe("content of " + C + "/skills/handoff/refs/notes.md");
  });

  it("shows a bundled skill without fetching a file, and the shadowing winner by path", async () => {
    await mount();
    await act(async () => rowNamed("simplify").click());
    await flush();
    expect(document.body.textContent).toContain(t("skills.bundled_hint"));
    expect(api.mock.calls.filter(([u]) => String(u).startsWith("api/skills/file"))).toHaveLength(0);

    await act(async () => rowNamed("handoff", "command").click());
    await flush();
    expect(document.body.textContent).toContain(t("skills.shadowed_by", { by: C + "/skills/handoff/SKILL.md" }));
  });

  it("still shows the folders of a kind that has no skills", async () => {
    await mount();
    const tab = Array.from(document.querySelectorAll<HTMLButtonElement>("[role=tab]")).find((b) =>
      (b.textContent ?? "").startsWith("Muse"),
    )!;
    await act(async () => tab.click());
    await flush();
    expect(document.body.textContent).toContain(t("skills.none"));
    expect(document.querySelector(".skills-roots code")?.textContent).toBe("/home/dev/.config/muse/skills");
  });
});
