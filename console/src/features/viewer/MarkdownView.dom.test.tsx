// Render tests for MarkdownView's reference auto-linking (linkifyRefs): bare commit
// hashes, session slugs ("s…") and assistant-conversation slugs ("a…") in prose.
// These pin the existence gating and the conv-vs-commit classification order — the
// parts a reader can't tell from the regexes alone.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Session } from "../../types/session.ts";
import type { ConversationMeta } from "../../types/chat.ts";

// Toasts require a provider; the tests only need the calls observable.
const toasts: unknown[] = [];
vi.mock("../../ui/ToastProvider.tsx", () => ({
  useToast: () => (m: unknown) => toasts.push(m),
}));

// Pane-open helpers become spies — asserting on layout-store internals would couple
// the test to pane mechanics that have their own suites.
const opened: { kind: string; ref: string; openInNew?: boolean }[] = [];
vi.mock("../chat/open.ts", () => ({
  openChat: (id: string) => opened.push({ kind: "conv", ref: id }),
  openChatSplit: (id: string) => opened.push({ kind: "conv", ref: id, openInNew: true }),
}));
vi.mock("../sessions/open.ts", () => ({
  openSessionChat: (name: string) => opened.push({ kind: "session", ref: name }),
  openSessionChatSplit: (name: string) => opened.push({ kind: "session", ref: name, openInNew: true }),
}));
vi.mock("../scm/open.ts", () => ({
  openCommit: (repo: string, sha: string) => opened.push({ kind: "commit", ref: `${repo}:${sha}` }),
}));

// chatList feeds ensureConvs (the "list not loaded yet" path).
let listedConvs: ConversationMeta[] = [];
vi.mock("../chat/api.ts", () => ({
  chatList: vi.fn(async () => ({ conversations: listedConvs })),
}));

const { MarkdownView } = await import("./MarkdownView.tsx");
const { useChatStore } = await import("../chat/store.ts");
const { useSessionsStore } = await import("../sessions/store.ts");

const conv = (slug: string, id = `id-${slug}`): ConversationMeta =>
  ({ id, slug, agent: "claude", title: slug, created_at: 0, updated_at: 0, message_count: 1 }) as ConversationMeta;
const session = (name: string): Session => ({ name, kind: "claude" }) as Session;

let host: HTMLDivElement;
let root: Root;

const render = async (source: string, repo: string | null = null) => {
  await act(async () => {
    root.render(<MarkdownView source={source} repo={repo} />);
  });
};
const links = (cls: string) => [...host.querySelectorAll<HTMLAnchorElement>(`a.${cls}`)];

beforeEach(() => {
  toasts.length = 0;
  opened.length = 0;
  listedConvs = [];
  useChatStore.setState({ convs: null });
  useSessionsStore.setState({ sessions: [] });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
});

describe("conversation-slug linkify", () => {
  it("links a slug whose conversation exists and opens the chat by id on click", async () => {
    useChatStore.getState().setConvs([conv("azw7wys")]);
    await render("報告は azw7wys を参照。");
    const [a] = links("md-conv-link");
    expect(a?.textContent).toBe("azw7wys");
    await act(async () => a.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true })));
    expect(opened).toEqual([{ kind: "conv", ref: "id-azw7wys" }]);
  });

  it("leaves a slug-shaped English word unlinked when no such conversation exists", async () => {
    useChatStore.getState().setConvs([conv("azw7wys")]);
    await render("checked against the list");
    expect(links("md-ref-link")).toHaveLength(0);
  });

  it("loads the conversation list on demand and linkifies once it arrives", async () => {
    listedConvs = [conv("azw7wys")];
    await render("see azw7wys"); // convs = null at render time → ensureConvs kicks in
    expect(links("md-conv-link")).toHaveLength(1);
    expect(useChatStore.getState().convs).toEqual(listedConvs);
  });

  it("prefers a live conversation over the commit shape for an all-hex slug", async () => {
    useChatStore.getState().setConvs([conv("abcdef2")]);
    await render("token abcdef2 here", "r1");
    expect(links("md-conv-link")).toHaveLength(1);
    expect(links("md-commit-link")).toHaveLength(0);
  });

  it("falls back to a commit link for an all-hex token with no matching conversation", async () => {
    useChatStore.getState().setConvs([]);
    await render("token abcdef2 here", "r1");
    expect(links("md-commit-link")).toHaveLength(1);
    expect(links("md-conv-link")).toHaveLength(0);
  });

  it("toasts instead of opening when the conversation vanished before the click", async () => {
    useChatStore.getState().setConvs([conv("azw7wys")]);
    await render("see azw7wys");
    const [a] = links("md-conv-link");
    useChatStore.getState().setConvs([]); // deleted meanwhile
    await act(async () => a.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true })));
    expect(opened).toHaveLength(0);
    expect(toasts).toHaveLength(1);
  });
});

describe("session-slug linkify (existing behavior guarded)", () => {
  it("still links a live session slug", async () => {
    useChatStore.getState().setConvs([]);
    useSessionsStore.setState({ sessions: [session("sukbq4s")] });
    await render("mirror of sukbq4s");
    const [a] = links("md-session-link");
    expect(a?.textContent).toBe("sukbq4s");
    await act(async () => a.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true })));
    expect(opened).toEqual([{ kind: "session", ref: "sukbq4s" }]);
  });
});

describe("fenced-code controls", () => {
  it("toggles line wrapping for only the selected code block", async () => {
    useChatStore.getState().setConvs([]);
    await render("```ts\nconst veryLongLine = 'value';\n```");

    const pre = host.querySelector("pre");
    const button = host.querySelector<HTMLButtonElement>(".md-code-wrap-toggle");
    expect(pre?.classList.contains("md-code-wrap")).toBe(false);
    expect(button?.getAttribute("aria-pressed")).toBe("false");

    await act(async () => button?.click());
    expect(pre?.classList.contains("md-code-wrap")).toBe(true);
    expect(button?.getAttribute("aria-pressed")).toBe("true");

    await act(async () => button?.click());
    expect(pre?.classList.contains("md-code-wrap")).toBe(false);
  });
});

describe("fullwidth-pipe table repair", () => {
  it("renders a fullwidth-pipe table as a table and marks it as repaired", async () => {
    useChatStore.getState().setConvs([]);
    await render("｜章｜点｜\n｜---｜---｜\n｜A1C01｜6.5｜");

    expect(host.querySelectorAll("table")).toHaveLength(1);
    expect(host.querySelectorAll("tbody tr")).toHaveLength(1);
    const notice = host.querySelector(".md-table-repaired");
    expect(notice?.nextElementSibling?.tagName).toBe("TABLE");
  });

  it("leaves a well-formed table unmarked", async () => {
    useChatStore.getState().setConvs([]);
    await render("| 章 | 点 |\n|---|---|\n| A1C01 | 6.5 |");

    expect(host.querySelectorAll("table")).toHaveLength(1);
    expect(host.querySelector(".md-table-repaired")).toBeNull();
  });
});

describe("quote controls", () => {
  it("adds a copy control to a rendered quote", async () => {
    useChatStore.getState().setConvs([]);
    await render("> 引用本文");

    const quote = host.querySelector("blockquote");
    const button = host.querySelector<HTMLButtonElement>(".md-quote-copy-button");
    expect(quote?.classList.contains("md-quote-copy")).toBe(true);
    expect(button?.getAttribute("aria-label")).toBeTruthy();
  });
});
