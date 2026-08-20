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

// The Chromium attachment opener is a spy: the pane commit has its own suite.
const attachOpened: string[] = [];
vi.mock("../browser/attachmentAction.ts", () => ({
  openBrowserAttachment: async (id: string) => {
    attachOpened.push(id);
    return true;
  },
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
  attachOpened.length = 0;
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

// The link attach_chromium tells the agent to post. It looks like a repo-root
// path, and the file resolver used to swallow it: the click answered
// "repos/<repo>/open/browser-attachment/ba_… not found" instead of opening the
// pane, which killed the whole hand-off (docs/53 §53.7).
describe("Chromium attachment action link", () => {
  const openFiles: string[] = [];
  const renderLink = async (source: string) => {
    await act(async () => {
      root.render(
        <MarkdownView
          source={source}
          baseDir="/home/dev/repos/novel-idea@wip-sv57pon/02-noir"
          onOpenFile={(path: string) => openFiles.push(path)}
        />,
      );
    });
  };

  beforeEach(() => {
    openFiles.length = 0;
    useChatStore.setState({ convs: [] });
  });

  it("opens the attachment pane instead of resolving a repo file", async () => {
    const id = "ba_2463e5bc214dda83010f9c232a78a88e";
    await renderLink(`[ブラウザを開いて操作する](/open/browser-attachment/${id})`);

    const [a] = links("action-link");
    expect(a).toBeTruthy();
    expect(a.classList.contains("repo-link")).toBe(false);
    await act(async () => a.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true })));

    expect(attachOpened).toEqual([id]);
    expect(openFiles).toHaveLength(0);
    expect(toasts).toHaveLength(0);
  });

  it("accepts the same link written as an absolute same-origin URL", async () => {
    const id = "ba_2463e5bc214dda83010f9c232a78a88e";
    await renderLink(`[開く](${location.origin}/open/browser-attachment/${id})`);
    const [a] = links("action-link");
    await act(async () => a.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true })));
    expect(attachOpened).toEqual([id]);
  });

  it("leaves a foreign origin as an ordinary external link", async () => {
    await renderLink("[罠](https://evil.invalid/open/browser-attachment/ba_x)");
    expect(links("action-link")).toHaveLength(0);
    expect(links("ext-link")).toHaveLength(1);
    expect(attachOpened).toHaveLength(0);
  });

  it("still classifies an ordinary repo path as a file link", async () => {
    await renderLink("[docs](/docs/53-chromium-attach-view.md)");
    expect(links("action-link")).toHaveLength(0);
    const [a] = links("repo-link");
    expect(a?.title).toContain("repos/novel-idea@wip-sv57pon/docs/53-chromium-attach-view.md");
    await act(async () => a.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true })));
    expect(attachOpened).toHaveLength(0);
  });
});

describe("fenced-code controls", () => {
  it("toggles line wrapping for only the selected code block", async () => {
    useChatStore.getState().setConvs([]);
    await render("```ts\nconst veryLongLine = 'value';\n```");

    const pre = host.querySelector("pre");
    const button = host.querySelector<HTMLButtonElement>(".md-code-wrap-toggle");
    // markdownCodeWrap defaults to true, so a fresh block starts wrapped.
    expect(pre?.classList.contains("md-code-wrap")).toBe(true);
    expect(button?.getAttribute("aria-pressed")).toBe("true");

    await act(async () => button?.click());
    expect(pre?.classList.contains("md-code-wrap")).toBe(false);
    expect(button?.getAttribute("aria-pressed")).toBe("false");

    await act(async () => button?.click());
    expect(pre?.classList.contains("md-code-wrap")).toBe(true);
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

// `[label]: destination` is a link reference definition and renders as nothing. Japanese
// prose has no ASCII space, so a whole note line matched that shape and the list item came
// out empty — while every later `[保留]` in the document turned into a link to the sentence.
describe("link reference definition vs Japanese prose", () => {
  it("renders a bracketed label followed by Japanese prose as written", async () => {
    useChatStore.getState().setConvs([]);
    await render("- [棄却＝記録]: 中イキ未達を意図化する案（既定設計と逆）。\n- [保留]: 幕間の再配置／MED語彙拡張。");

    const items = [...host.querySelectorAll("li")].map((li) => li.textContent);
    expect(items).toEqual(["[棄却＝記録]: 中イキ未達を意図化する案（既定設計と逆）。", "[保留]: 幕間の再配置／MED語彙拡張。"]);
    expect(host.querySelectorAll("a")).toHaveLength(0);
  });

  it("still consumes a real definition and resolves references to it", async () => {
    useChatStore.getState().setConvs([]);
    await render("[foo]: https://example.com/x\n\nsee [foo] and [docs]\n\n[docs]: /docs/68-session-changed-files.md");

    expect(host.textContent).not.toContain("https://example.com/x");
    const hrefs = [...host.querySelectorAll("a")].map((a) => a.getAttribute("href"));
    expect(hrefs).toEqual(["https://example.com/x", "/docs/68-session-changed-files.md"]);
  });

  it("keeps a definition whose destination is a non-ASCII URL or path", async () => {
    useChatStore.getState().setConvs([]);
    await render("[w]: https://ja.wikipedia.org/wiki/日本語\n\nsee [w]");

    expect(host.querySelector("a")?.getAttribute("href")).toContain("wiki/");
    expect(host.querySelector("a")?.textContent).toBe("w");
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

// A front matter block YAML rejects (here: a value opening with a backtick, which
// YAML reserves) used to land in the body as one run-on paragraph. It now fills the
// property panel like any other, with a notice — the file is still broken elsewhere.
describe("front matter", () => {
  it("shows an invalid block in the property panel, with a notice", async () => {
    useChatStore.getState().setConvs([]);
    await render("---\n用途: 商業化可能性評価\n備考: `レビュー.md` とは役割が違う\n---\n\n# 本文");

    const panel = host.querySelector(".md-frontmatter");
    expect([...(panel?.querySelectorAll("dt") ?? [])].map((d) => d.textContent)).toEqual(["用途", "備考"]);
    expect(panel?.querySelectorAll("dd")[1]?.textContent).toBe("`レビュー.md` とは役割が違う");
    // Only the heading is left in the body — no stray paragraph, no <hr>.
    expect(host.querySelector("hr")).toBeNull();
    expect(host.querySelector("h1")?.textContent).toBe("本文");

    const note = host.querySelector(".md-frontmatter-note");
    expect(note?.nextElementSibling).toBe(panel);
  });

  it("leaves a valid block unmarked", async () => {
    useChatStore.getState().setConvs([]);
    await render("---\n用途: 評価\n---\n\n# 本文");

    expect(host.querySelector(".md-frontmatter dd")?.textContent).toBe("評価");
    expect(host.querySelector(".md-frontmatter-note")).toBeNull();
  });
});
