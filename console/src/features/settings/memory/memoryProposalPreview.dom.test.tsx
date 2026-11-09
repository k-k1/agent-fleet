// The "if you approve" preview of a consolidator proposal (#3720). Pinned here: the one-line summary
// for add-only, change-only, mixed and advice-only proposals; one card per item kind (create with
// the rendered body, patch as before/after with its revisions, advice as not approvable); the
// "topic memory is not changed" note only while it is true; the title is the subject of the
// change, not the clustering topic; and a hostile body never reaches the DOM as live markup.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));

import { ProposalPreview, proposalSubject, parseLineDiff, topicChanged } from "./memoryProposalPreview.tsx";
import { ProposalsPanel } from "./memoryProposals.tsx";
import { ConfirmProvider } from "../../../ui/ConfirmProvider.tsx";
import { ToastProvider } from "../../../ui/ToastProvider.tsx";
import { setLocale, t } from "../../../lib/i18n/index.ts";
import type { ConsolItemView, ConsolProposalView } from "./memoryTypes.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const create = (name: string, extra: Partial<ConsolItemView> = {}): ConsolItemView => ({
  op: "create",
  name,
  description: `about ${name}`,
  type: "reference",
  body: `# ${name}\n\nbody of **${name}**`,
  reason: "seen twice",
  diff: `+# ${name}\n+\n+body of ${name}`,
  ...extra,
});
const patch = (name: string, revision = 2): ConsolItemView => ({
  op: "patch",
  name,
  revision,
  reason: "trunk renamed",
  edits: [{ old: "develop", new: "main" }],
  diff: " keep this\n-branch is develop\n+branch is main\n…\n tail",
});
const advice: ConsolItemView = { op: "note", name: "tests", advice: "merge", targets: ["tests", "ci"], reason: "same rule twice" };

function viewOf(memory: string, itemViews: ConsolItemView[], over: Partial<ConsolProposalView> = {}): ConsolProposalView {
  const targetRevisions: Record<string, number> = {};
  for (const i of itemViews) if (i.op === "patch" && i.revision) targetRevisions[i.name] = i.revision;
  return {
    id: "p".repeat(20),
    topicId: "t",
    scope: "user",
    memory,
    status: "pending",
    created: "2026-11-09T01:00:00Z",
    expires: "2099-11-23T01:00:00Z",
    items: itemViews,
    itemViews,
    observationIds: [],
    targetRevisions,
    evidence: [],
    ...over,
  };
}

async function mount(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>{node}</ConfirmProvider>
      </ToastProvider>,
    );
  });
  await act(async () => {
    for (let i = 0; i < 6; i++) await Promise.resolve();
  });
}
const summary = () => host!.querySelector(".mem-pv-summary")?.textContent ?? "";
const cards = (op: string) => Array.from(host!.querySelectorAll<HTMLElement>(`.mem-pv-card[data-op="${op}"]`));

beforeEach(() => setLocale("en"));
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  api.mockReset();
  apiJSON.mockReset();
  document.body.innerHTML = "";
});

describe("the pure helpers", () => {
  it("titles a proposal by what it creates or patches, never by the topic memory", () => {
    expect(proposalSubject({ memory: "topic", items: [create("fresh")] })).toBe("fresh");
    expect(proposalSubject({ memory: "topic", items: [patch("adr-0094")] })).toBe("adr-0094");
    expect(proposalSubject({ memory: "topic", items: [create("a"), patch("b")] })).toBe("a +1");
    expect(proposalSubject({ memory: "topic", items: [advice] })).toBe("tests");
    expect(proposalSubject({ memory: "topic", items: [] })).toBe("topic");
  });

  it("knows the topic memory is changed only by a patch that names it", () => {
    expect(topicChanged({ memory: "m", items: [create("m2")] })).toBe(false);
    expect(topicChanged({ memory: "m", items: [create("m")] })).toBe(false);
    expect(topicChanged({ memory: "m", items: [patch("m")] })).toBe(true);
  });

  it("parses the Agent's line diff", () => {
    expect(parseLineDiff(" a\n-b\n+c\n…\n d\n")).toEqual([
      { tag: " ", text: "a" },
      { tag: "-", text: "b" },
      { tag: "+", text: "c" },
      { tag: "…", text: "" },
      { tag: " ", text: "d" },
    ]);
    expect(parseLineDiff(undefined)).toEqual([]);
  });
});

describe("the summary line", () => {
  it("says an add-only approval changes no existing memory", async () => {
    await mount(<ProposalPreview view={viewOf("topic", [create("one"), create("two")])} />);
    expect(summary()).toBe("Approving adds 2 memories; no existing memory changes.");
  });

  it("names the patched memory and its revisions", async () => {
    await mount(<ProposalPreview view={viewOf("adr-0094", [patch("adr-0094", 2)])} />);
    expect(summary()).toBe("Approving changes 1 memory (adr-0094, rev 2 → 3).");
  });

  it("counts both when a proposal adds and changes", async () => {
    await mount(<ProposalPreview view={viewOf("topic", [create("one"), patch("p", 5)])} />);
    expect(summary()).toBe("Approving adds 1 and changes 1 (p, rev 5 → 6).");
  });

  it("says an advice-only proposal changes nothing", async () => {
    await mount(<ProposalPreview view={viewOf("tests", [advice])} />);
    expect(summary()).toContain("Advice only");
  });

  it("is in Japanese too", async () => {
    setLocale("ja");
    await mount(<ProposalPreview view={viewOf("topic", [create("one"), create("two")])} />);
    expect(summary()).toBe(t("mem.pv_sum_add_many", { n: 2 }));
    expect(summary()).toContain("2 件追加");
  });

  it("is not shown once the proposal is decided", async () => {
    await mount(<ProposalPreview view={viewOf("topic", [create("one")], { status: "approved" })} />);
    expect(host!.querySelector(".mem-pv-summary")).toBeNull();
    expect(cards("create")).toHaveLength(1);
  });
});

describe("the create card", () => {
  it("shows scope, name, type, description and the rendered body", async () => {
    await mount(<ProposalPreview view={viewOf("topic", [create("fresh")], { scope: "project", projectId: "proj-1" })} />);
    const [card] = cards("create");
    expect(card.querySelector(".mem-pv-title")!.textContent).toContain(t("mem.pv_new"));
    expect(card.querySelector("code")!.textContent).toBe("fresh");
    const meta = card.querySelector(".mem-pv-meta")!.textContent!;
    expect(meta).toContain("proj-1");
    expect(meta).toContain("reference");
    expect(meta).toContain("about fresh");
    // Rendered, not the raw markdown source.
    expect(card.querySelector(".mem-pv-body h1")!.textContent).toBe("fresh");
    expect(card.querySelector(".mem-pv-body strong")!.textContent).toBe("fresh");
  });

  it("says the topic memory is not changed when the create is not about it", async () => {
    await mount(<ProposalPreview view={viewOf("unrelated-topic", [create("fresh")])} />);
    expect(cards("create")[0].textContent).toContain("The topic memory “unrelated-topic” is not changed.");
  });

  it("does not claim that when another item of the proposal patches the topic memory", async () => {
    await mount(<ProposalPreview view={viewOf("topic", [create("fresh"), patch("topic")])} />);
    expect(cards("create")[0].querySelector(".mem-pv-untouched")).toBeNull();
  });

  it("never turns a hostile body into live markup", async () => {
    const body = '<script>window.__pwned=1</script><img src=x onerror="window.__pwned=2"><a href="javascript:window.__pwned=3">x</a>\n\n[y](javascript:alert(1))';
    await mount(<ProposalPreview view={viewOf("topic", [create("evil", { body })])} />);
    const el = cards("create")[0].querySelector(".mem-pv-body")!;
    expect(el.querySelector("script")).toBeNull();
    expect(el.innerHTML).not.toMatch(/onerror|javascript:/i);
    expect((window as unknown as { __pwned?: number }).__pwned).toBeUndefined();
  });
});

describe("the patch card", () => {
  it("shows revision n → n+1 as before and after with the changed lines marked", async () => {
    await mount(<ProposalPreview view={viewOf("branches", [patch("branches", 3)])} />);
    const [card] = cards("patch");
    expect(card.querySelector(".mem-pv-title")!.textContent).toContain("rev 3 → 4");
    const sides = card.querySelectorAll(".mem-pv-side");
    expect(sides).toHaveLength(2);
    expect(sides[0].getAttribute("aria-label")).toBe("Before (rev 3)");
    expect(sides[1].getAttribute("aria-label")).toBe("After (rev 4)");
    // The removed line is only before, the added line only after; the context is on both.
    expect(sides[0].querySelector(".mem-pv-del")!.textContent).toContain("branch is develop");
    expect(sides[0].textContent).not.toContain("branch is main");
    expect(sides[1].querySelector(".mem-pv-add")!.textContent).toContain("branch is main");
    expect(sides[1].textContent).not.toContain("branch is develop");
    expect(sides[0].textContent).toContain("keep this");
    expect(sides[1].textContent).toContain("keep this");
  });

  it("says it is published as consolidator and can be reverted from the change list", async () => {
    await mount(<ProposalPreview view={viewOf("branches", [patch("branches", 3)])} />);
    const note = cards("patch")[0].textContent!;
    expect(note).toContain("consolidator");
    expect(note).toContain("revert it from the change list");
  });

  it("shows why a patch no longer applies", async () => {
    await mount(<ProposalPreview view={viewOf("branches", [{ ...patch("branches", 3), stale: "the memory has changed" }])} />);
    expect(cards("patch")[0].querySelector(".mem-warn")!.textContent).toContain("the memory has changed");
  });
});

describe("the advice card", () => {
  it("says what would happen and that it cannot be approved yet", async () => {
    await mount(<ProposalPreview view={viewOf("tests", [advice])} />);
    const [card] = cards("note");
    expect(card.textContent).toContain("Suggests merging the memories (tests, ci) into one.");
    expect(card.textContent).toContain("not approvable yet");
    expect(card.textContent).toContain("same rule twice");
  });
});

describe("review findings (round 1)", () => {
  it("shows no stale warning on a decided proposal's cards", async () => {
    const staleCreate = create("fresh", { stale: "name exists" });
    await mount(<ProposalPreview view={viewOf("t", [staleCreate, { ...patch("p"), stale: "the memory has changed since" }], { status: "approved" })} />);
    expect(host!.querySelector(".mem-warn")).toBeNull();
  });

  it("says only the advice writes nothing when a patch rides along", async () => {
    await mount(<ProposalPreview view={viewOf("t", [patch("p"), advice])} />);
    const text = cards("note")[0].textContent!;
    expect(text).toContain("This suggestion writes nothing");
    expect(text).not.toContain("Nothing is written");
  });

  it("marks the items that were applied", async () => {
    await mount(<ProposalPreview view={viewOf("t", [create("a", { commit: "c1" }), patch("p"), { ...patch("q"), commit: "c2" }], { status: "partial" })} />);
    const applied = (el: HTMLElement) => el.textContent!.includes(t("mem.cs_published"));
    expect(cards("create").map(applied)).toEqual([true]);
    expect(cards("patch").map(applied)).toEqual([false, true]);
  });

  it("counts a name once in the summary", async () => {
    await mount(<ProposalPreview view={viewOf("t", [create("same"), create("same")])} />);
    expect(summary()).toBe("Approving adds 1 memory; no existing memory changes.");
  });

  it("drops the revision from the labels of a stale patch", async () => {
    await mount(<ProposalPreview view={viewOf("t", [{ ...patch("p", 2), stale: "the memory has changed since" }])} />);
    const labels = Array.from(cards("patch")[0].querySelectorAll(".mem-pv-side")).map((e) => e.getAttribute("aria-label"));
    expect(labels).toEqual(["Before", "After"]);
  });

  it("prefers the item's own revision over targetRevisions", async () => {
    await mount(<ProposalPreview view={viewOf("t", [patch("p", 4)], { targetRevisions: { p: 9 } })} />);
    expect(summary()).toContain("rev 4 → 5");
  });

  it("says the topic memory is untouched once for several creates", async () => {
    await mount(<ProposalPreview view={viewOf("topic", [create("a"), create("b")])} />);
    expect(host!.querySelectorAll(".mem-pv-untouched")).toHaveLength(1);
  });
});

describe("in the proposals panel", () => {
  const proposal = viewOf("unrelated-topic", [create("fresh")]);
  const route = (path: string) => {
    if (path === "api/agents/memory-consolidation/proposals") return Promise.resolve({ proposals: [proposal] });
    if (path.includes("proposal?id=")) return Promise.resolve(proposal);
    return Promise.resolve({});
  };

  it("titles the list row and the detail by the created name, and puts the preview before Approve", async () => {
    api.mockImplementation(route);
    await mount(<ProposalsPanel reload={0} onChanged={() => {}} />);
    expect(host!.querySelector(".mem-list .mem-af-name")!.textContent).toBe("fresh");
    expect(host!.querySelector(".mem-cs-subject code")!.textContent).toBe("fresh");
    const approve = Array.from(host!.querySelectorAll("button")).find((b) => b.textContent === t("mem.cs_approve"))!;
    const preview = host!.querySelector(".mem-pv")!;
    expect(preview.compareDocumentPosition(approve) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("offers no Approve for an advice-only proposal", async () => {
    const adv = viewOf("tests", [advice]);
    api.mockImplementation((path: string) =>
      path.endsWith("/proposals") ? Promise.resolve({ proposals: [adv] }) : path.includes("proposal?id=") ? Promise.resolve(adv) : Promise.resolve({}),
    );
    await mount(<ProposalsPanel reload={0} onChanged={() => {}} />);
    expect(Array.from(host!.querySelectorAll("button")).some((b) => b.textContent === t("mem.cs_approve"))).toBe(false);
    expect(cards("note")).toHaveLength(1);
  });
});
