// The notes under the family card (ADR 0100 decision 12): where the Files pane reaches the file
// (the Agent sends `files_path`) the memo offers it and only appends; where it does not, the memo
// edits the four sections — starting from the WHOLE file, and keeping the member's text when
// another writer got there first.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const calls: { get: [string, string, boolean][]; put: unknown[] } = { get: [], put: [] };
let docs: Record<string, object> = {};
let putStatus = 200;
vi.mock("./api.ts", () => ({
  imagegenKnowledge: async (scope: string, key: string, full = false) => {
    calls.get.push([scope, key, full]);
    return docs[full ? "full" : "cut"];
  },
  addImagegenKnowledge: async () => ({}),
  editImagegenKnowledge: async (body: unknown) => {
    calls.put.push(body);
    return putStatus === 200
      ? { ...(docs.full as object), status: 200 }
      : { status: putStatus, error: { code: "knowledge_changed" } };
  },
}));
const opened: string[] = [];
vi.mock("../viewer/openFile.ts", () => ({ openFileMode: (p: string) => opened.push(p) }));

import { ToastProvider } from "../../ui/ToastProvider.tsx";
import { KnowledgeMemo } from "./parts/KnowledgeMemo.tsx";

let host: HTMLDivElement;
let root: Root;

const base = {
  scope: "model",
  key: "m",
  path: "/home/u/imagegen-knowledge/models/m.md",
  settings: "cfg 5",
  prompts: "",
  records: "- r1",
};

beforeEach(() => {
  calls.get.length = 0;
  calls.put.length = 0;
  opened.length = 0;
  putStatus = 200;
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
});

async function mountOpen() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () =>
    root.render(
      <ToastProvider>
        <KnowledgeMemo model="m" />
      </ToastProvider>,
    ),
  );
  const details = host.querySelector("details")!;
  await act(async () => {
    details.open = true;
    details.dispatchEvent(new Event("toggle"));
  });
}

const button = (label: string) => [...host.querySelectorAll("button")].find((b) => b.textContent?.includes(label));
const click = async (el: Element | undefined) => act(async () => void (el as HTMLElement).click());

describe("the notes memo", () => {
  it("offers the Files pane when the Agent names the file relative to the browse root, and no editor", async () => {
    docs = {
      cut: { ...base, summary: "s", version: "v1", files_path: "imagegen-knowledge/models/m.md" },
    };
    await mountOpen();
    await click(button("ファイルで開く") || button("Open in Files"));
    expect(opened).toEqual(["imagegen-knowledge/models/m.md"]);
    expect(host.querySelector(".igen-memo-edit")).toBeNull();
    expect(button("編集") || button("Edit")).toBeUndefined();
  });

  it("edits the four sections from the whole file where the Files pane cannot reach it", async () => {
    docs = {
      cut: { ...base, summary: "short…", summary_truncated: true, version: "v1" },
      full: { ...base, summary: "the whole summary", version: "v1" },
    };
    await mountOpen();
    await click(button("編集") || button("Edit"));
    expect(calls.get.at(-1)).toEqual(["model", "m", true]);
    const summary = host.querySelector<HTMLTextAreaElement>('textarea[data-section="summary"]')!;
    expect(summary.value).toBe("the whole summary");
    await click(button("保存") || button("Save"));
    expect(calls.put).toEqual([
      { scope: "model", key: "m", version: "v1", summary: "the whole summary", settings: "cfg 5", prompts: "", records: "- r1" },
    ]);
    expect(host.querySelector(".igen-memo-edit")).toBeNull();
  });

  it("keeps the editor open when another writer got there first", async () => {
    docs = {
      cut: { ...base, summary: "s", version: "v1" },
      full: { ...base, summary: "s", version: "v1" },
    };
    putStatus = 412;
    await mountOpen();
    await click(button("編集") || button("Edit"));
    await click(button("保存") || button("Save"));
    expect(host.querySelector(".igen-memo-edit")).not.toBeNull();
  });
});
