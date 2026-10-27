// Repository Skills modal (#2795). Pinned here: the catalog is asked per Subdir, a missing
// folder is listed with the path to create, a shadowed entry says which copy wins, a same-named
// personal skill links to Settings > Skills, an entry that leaves the working copy is named but
// never fetched, and an opened file is read through the repo-scoped route.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  errText: (e: { message?: string }) => e?.message || "",
  isTransientErr: () => false,
}));
vi.mock("../../core/store/workspace.ts", () => ({
  useWorkspaceStore: (sel: (s: unknown) => unknown) => sel({ state: "running", start: () => {} }),
  wsStartBusy: () => false,
}));
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
vi.mock("../viewer/MarkdownView.tsx", () => ({
  MarkdownView: ({ source }: { source: string }) => <div className="md-text">{source}</div>,
}));
const openFileMode = vi.fn();
vi.mock("../viewer/openFile.ts", () => ({ openFileMode: (...a: unknown[]) => openFileMode(...a) }));

import { RepoSkillsModal } from "./RepoSkillsModal.tsx";
import { useSettingsUI } from "../settings/store.ts";
import { t } from "../../lib/i18n/index.ts";

const H = "/home/dev/repos/demo";
const rootCatalog = {
  repo: "demo",
  subdir: "",
  kinds: [
    {
      kind: "claude",
      roots: [
        { dir: H + "/.claude/skills", rel: ".claude/skills", scope: "project", form: "skills", exists: true },
        { dir: H + "/.claude/commands", rel: ".claude/commands", scope: "project", form: "commands", exists: false },
      ],
      skills: [
        {
          name: "a",
          description: "root a",
          type: "skill",
          scope: "project",
          root: H + "/.claude/skills",
          path: H + "/.claude/skills/a/SKILL.md",
          rel: ".claude/skills/a/SKILL.md",
          userInvocable: true,
          alsoPersonal: true,
        },
        {
          name: "sneaky",
          type: "skill",
          scope: "project",
          path: H + "/.claude/skills/sneaky/SKILL.md",
          rel: ".claude/skills/sneaky/SKILL.md",
          userInvocable: true,
          outside: true,
        },
      ],
    },
    {
      kind: "codex",
      roots: [{ dir: H + "/.codex/skills", rel: ".codex/skills", scope: "project", form: "skills", exists: false }],
      skills: [],
    },
  ],
};
const pkgCatalog = {
  repo: "demo",
  subdir: "pkg",
  kinds: [
    {
      kind: "claude",
      roots: [{ dir: H + "/pkg/.claude/skills", rel: "pkg/.claude/skills", scope: "project", form: "skills", exists: true }],
      skills: [
        {
          name: "a",
          type: "skill",
          scope: "project",
          path: H + "/pkg/.claude/skills/a/SKILL.md",
          rel: "pkg/.claude/skills/a/SKILL.md",
          userInvocable: true,
        },
        {
          name: "a",
          type: "skill",
          scope: "project",
          path: H + "/.claude/skills/a/SKILL.md",
          rel: ".claude/skills/a/SKILL.md",
          userInvocable: true,
          shadowedBy: H + "/pkg/.claude/skills/a/SKILL.md",
        },
      ],
    },
  ],
};

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const flush = () =>
  act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<RepoSkillsModal repo="demo" />);
  });
  await flush();
}

const rows = () => Array.from(document.querySelectorAll<HTMLButtonElement>(".skills-row"));

beforeEach(() => {
  api.mockReset();
  openFileMode.mockReset();
  useSettingsUI.setState({ settingsOpen: false, settingsSection: "display", settingsView: null });
  api.mockImplementation(async (url: string) => {
    if (url === "api/skills/catalog?repo=demo&subdir=") return rootCatalog;
    if (url === "api/skills/catalog?repo=demo&subdir=pkg") return pkgCatalog;
    if (url.startsWith("api/skills/catalog?repo=demo&subdir=gone")) return { error: { code: "no_subdir" } };
    if (url.startsWith("api/skills/file?repo=demo")) return { content: "body of " + decodeURIComponent(url.split("&path=")[1]) };
    return { error: { message: "unexpected " + url } };
  });
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  document.body.innerHTML = "";
});

describe("RepoSkillsModal", () => {
  it("lists the present and the missing folders, with the path to create", async () => {
    await mount();
    const missing = document.querySelector(".skills-roots .skills-missing")!;
    expect(missing.textContent).toContain(".claude/commands");
    expect(missing.textContent).toContain(t("repo_skills.not_present"));
    expect(missing.textContent).toContain(".claude/commands/<name>.md");
    expect(document.querySelector(".skills-roots li:not(.skills-missing)")?.textContent).toContain(".claude/skills");
  });

  it("opens a file through the repo route and offers the viewer and the personal skill", async () => {
    await mount();
    await act(async () => rows().find((r) => r.textContent?.includes("root a"))!.click());
    await flush();
    expect(api).toHaveBeenCalledWith("api/skills/file?repo=demo&subdir=&path=" + encodeURIComponent(".claude/skills/a/SKILL.md"));
    expect(document.querySelector(".md-text")?.textContent).toBe("body of .claude/skills/a/SKILL.md");

    const viewer = Array.from(document.querySelectorAll("button")).find((b) => b.textContent === t("repo_skills.open_in_viewer"))!;
    await act(async () => viewer.click());
    expect(openFileMode).toHaveBeenCalledWith("repos/demo/.claude/skills/a/SKILL.md", "view");

    const personal = Array.from(document.querySelectorAll("button")).find((b) => b.textContent === t("repo_skills.open_personal"))!;
    await act(async () => personal.click());
    const st = useSettingsUI.getState();
    expect([st.settingsOpen, st.settingsSection, st.settingsView]).toEqual([true, "skills", "claude"]);
  });

  it("names an entry that leaves the working copy without fetching it", async () => {
    await mount();
    api.mockClear();
    await act(async () => rows().find((r) => r.textContent?.includes("sneaky"))!.click());
    await flush();
    expect(document.body.textContent).toContain(t("repo_skills.outside"));
    expect(api.mock.calls.filter((c) => String(c[0]).startsWith("api/skills/file"))).toEqual([]);
  });

  it("reloads for a Subdir and shows which copy wins", async () => {
    await mount();
    const input = document.querySelector<HTMLInputElement>(".skills-subdir input")!;
    const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    await act(async () => {
      set.call(input, "pkg");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => {
      document.querySelector<HTMLFormElement>(".skills-subdir")!.requestSubmit();
    });
    await flush();
    expect(api).toHaveBeenCalledWith("api/skills/catalog?repo=demo&subdir=pkg");
    const [nested, shadowed] = rows();
    expect(nested.classList.contains("skills-shadowed")).toBe(false);
    expect(shadowed.classList.contains("skills-shadowed")).toBe(true);
    await act(async () => shadowed.click());
    expect(document.body.textContent).toContain(t("skills.shadowed_by", { by: "pkg/.claude/skills/a/SKILL.md" }));
  });

  it("says so when the Subdir does not exist", async () => {
    await mount();
    const input = document.querySelector<HTMLInputElement>(".skills-subdir input")!;
    const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    await act(async () => {
      set.call(input, "gone");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => {
      document.querySelector<HTMLFormElement>(".skills-subdir")!.requestSubmit();
    });
    await flush();
    expect(document.querySelector(".form-err")?.textContent).toBe(t("repo_skills.no_subdir", { subdir: "gone" }));
  });
});
