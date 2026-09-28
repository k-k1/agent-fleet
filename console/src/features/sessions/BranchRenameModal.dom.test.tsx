// The rename dialog asks the branch-name resolver (ADR 0103 decision 8): its chips are the
// repository's resolved prefixes, a chip still swaps only the prefix, and the AI suggestion's
// kind and slug go back to the resolver with the session, which composes the whole name. An
// Agent without the resolver keeps the fixed chips and the bare slug.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

type Json = Record<string, unknown>;
let rule: Json = {};
let suggestion: Json = {};
let composed: Json = {};
let checked: Json = { warnings: [] };
const api = vi.fn(async (url: string): Promise<Json> => (url.includes("/branch-rule") ? rule : {}));
const apiJSON = vi.fn(async (url: string, _m: string, _body: Json): Promise<Json> => {
  if (url.endsWith("/suggest-branch")) return suggestion;
  if (url.endsWith("/branch-name")) return composed;
  if (url.endsWith("/branch-name/check")) return checked;
  return {};
});
vi.mock("../../core/api/client.ts", async (orig) => {
  const real = (await orig()) as Record<string, unknown>;
  return {
    ...real,
    api: (...a: unknown[]) => api(...(a as [string])),
    apiJSON: (...a: unknown[]) => apiJSON(...(a as [string, string, Json])),
  };
});

const { BranchRenameModal, chipPrefixes, stripKnownPrefix } = await import("./BranchRenameModal.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { setSetting } = await import("../../lib/settings.ts");

let root: Root | null = null;
let host: HTMLDivElement;

async function settle(): Promise<void> {
  for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
async function click(el: Element): Promise<void> {
  await act(async () => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  });
  await settle();
}
const chips = () => [...document.querySelectorAll(".sm-prefix-chip")].map((c) => c.textContent);
const chip = (p: string) => [...document.querySelectorAll(".sm-prefix-chip")].find((c) => c.textContent === p)!;
const input = () => document.querySelector<HTMLInputElement>("input[type=text]")!;
const byText = (t: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.includes(t))!;

async function render(branch: string, repo = "web@wip-abc"): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <BranchRenameModal name="s1" branch={branch} repo={repo} onClose={() => {}} onSaved={() => {}} />
      </ToastProvider>,
    );
  });
  await settle();
}

beforeEach(() => {
  setSetting("branchSuggestEnabled", true);
  rule = {
    kinds: [
      { kind: "feature", prefix: "feature/", base: "" },
      { kind: "bugfix", prefix: "bugfix/", base: "" },
      { kind: "hotfix", prefix: "hotfix/", base: "" },
    ],
  };
  suggestion = { branch: "login-redirect", kind: "bugfix", slug: "login-redirect" };
  composed = { name: "bugfix/PROJ-12-login-redirect", name_empty: false, warnings: [], sources: {} };
  checked = { warnings: [] };
  api.mockClear();
  apiJSON.mockClear();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  host.remove();
  root = null;
});

describe("BranchRenameModal with the resolver", () => {
  it("offers the resolved prefixes as chips, and a chip swaps only the prefix", async () => {
    await render("temp/abc123");
    expect(api).toHaveBeenCalledWith("api/repos/web%40wip-abc/branch-rule");
    expect(chips()).toEqual(["feature/", "bugfix/", "hotfix/"]);
    await click(chip("bugfix/"));
    expect(input().value).toBe("bugfix/abc123");
    await click(chip("feature/"));
    expect(input().value).toBe("feature/abc123");
    // A chip press asks the Agent for nothing.
    expect(apiJSON.mock.calls.some((c) => String(c[0]).endsWith("/branch-name"))).toBe(false);
  });

  it("composes the AI suggestion through the resolver with the session", async () => {
    await render("temp/abc123");
    // A chip already picked does not end up in front of a name the resolver composed whole.
    await click(chip("feature/"));
    await click(byText("Ask AI to suggest"));
    const call = apiJSON.mock.calls.find((c) => c[0] === "api/repos/web%40wip-abc/branch-name");
    expect(call?.[2]).toEqual({ session: "s1", kind: "bugfix", slug: "login-redirect" });
    expect(document.querySelector(".sm-proposal-text")?.textContent).toBe("bugfix/PROJ-12-login-redirect");
    await click(byText("Use this"));
    expect(input().value).toBe("bugfix/PROJ-12-login-redirect");
  });

  it("warns about a name outside the resolved prefixes without disabling Save", async () => {
    checked = { warnings: [{ code: "prefix_mismatch", message: "none of the resolved prefixes" }] };
    await render("feat/old-name");
    await act(async () => void (await new Promise((r) => setTimeout(r, 450))));
    // The name the dialog opened with is not checked: nobody chose it here.
    expect(document.querySelectorAll(".launch-branch-warns li")).toHaveLength(0);
    await click(chip("feature/"));
    await act(async () => void (await new Promise((r) => setTimeout(r, 450))));
    await settle();
    expect(document.querySelectorAll(".launch-branch-warns li")).toHaveLength(1);
    expect(byText("Save").disabled).toBe(false);
  });
});

describe("BranchRenameModal without the resolver", () => {
  it("keeps the fixed chips and puts the bare slug behind the current chip", async () => {
    rule = { error: { code: "http_404" } };
    suggestion = { branch: "login-redirect" };
    await render("fix/something");
    expect(chips()).toEqual(["feat/", "fix/", "refactor/", "chore/", "docs/"]);
    await click(byText("Ask AI to suggest"));
    expect(apiJSON.mock.calls.some((c) => String(c[0]).endsWith("/branch-name"))).toBe(false);
    await click(byText("Use this"));
    expect(input().value).toBe("fix/login-redirect");
  });
});

describe("chip helpers", () => {
  it("lists each non-empty prefix once, and the fixed set without kinds", () => {
    expect(chipPrefixes([{ prefix: "feature/" }, { prefix: "" }, { prefix: "feature/" }, { prefix: "fix/" }])).toEqual(["feature/", "fix/"]);
    expect(chipPrefixes(null)).toEqual(["feat/", "fix/", "refactor/", "chore/", "docs/"]);
  });

  it("strips the longest known prefix, including temp/ and the old fixed chips", () => {
    expect(stripKnownPrefix("fixup/x", ["fix/", "fixup/"])).toBe("x");
    expect(stripKnownPrefix("temp/abc", ["feature/"])).toBe("abc");
    expect(stripKnownPrefix("feat/abc", ["feature/"])).toBe("abc");
    expect(stripKnownPrefix("other/abc", ["feature/"])).toBe("other/abc");
  });
});
