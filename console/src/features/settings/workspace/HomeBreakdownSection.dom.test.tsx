// The home breakdown (#3083). What matters:
//   1. opening the tab never starts a walk: it reads the Agent's last result, and the walk
//      waits for the press
//   2. while the walk runs the button says how far it got, and the result appears without
//      another press
//   3. each category says what can be freed today and where; a working copy that git cannot
//      read says nothing of it is counted as build output
//   4. an Agent without the endpoint hides the section rather than showing an error
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { HomeBreakdownSection, type HomeUsage } from "./HomeBreakdownSection.tsx";

const answers = vi.hoisted(() => ({
  byPath: {} as Record<string, unknown>,
  calls: [] as string[],
}));
vi.mock("../../../core/api/client.ts", () => ({
  api: (path: string, opts?: RequestInit) => {
    answers.calls.push((opts?.method ?? "GET") + " " + path);
    return Promise.resolve(answers.byPath[path] ?? {});
  },
  apiJSON: () => Promise.resolve({}),
  getTenant: () => "default",
}));

const GiB = 2 ** 30;
const result: HomeUsage = {
  total: 66 * GiB,
  files: 3_000_000,
  categories: [
    { key: "source", bytes: 9 * GiB, files: 1, reclaimable: 0 },
    {
      key: "regenerable",
      bytes: 13 * GiB,
      files: 1,
      reclaimable: 0,
      parts: [{ name: "node_modules", bytes: 11 * GiB }],
    },
    {
      key: "tool_cache",
      bytes: 16 * GiB,
      files: 1,
      reclaimable: 10 * GiB,
      parts: [
        {
          name: ".gradle",
          bytes: 6 * GiB,
          path: "~/.gradle",
          browse: ".gradle",
        },
        { name: "uv", bytes: 5 * GiB, reclaimable: true, path: "~/.cache/uv" },
      ],
    },
    { key: "af_work", bytes: 4 * GiB, files: 1, reclaimable: 2 * GiB },
    { key: "trash", bytes: 2 * GiB, files: 1, reclaimable: 2 * GiB },
  ],
  repos: [
    {
      name: "web@feature-1",
      bytes: 2 * GiB,
      regenerable: 1800 * 2 ** 20,
      path: "~/repos/web@feature-1",
    },
    {
      name: "notes",
      bytes: GiB,
      regenerable: 0,
      unjudged: true,
      path: "~/repos/notes",
    },
  ],
  af_work: [
    {
      name: "sgone01",
      bytes: GiB,
      modified: "2026-09-01T00:00:00Z",
      orphan: true,
      marked: true,
    },
    { name: "my-stuff", bytes: GiB / 2, modified: "2026-10-30T00:00:00Z" },
  ],
  af_work_judged: true,
  measured_at: "2026-10-31T04:00:00Z",
  elapsed_ms: 95_000,
};

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount(own = true) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<HomeBreakdownSection own={own} />);
  });
  return host.textContent || "";
}

const buttons = () => [...document.body.querySelectorAll("button")];

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
beforeEach(() => {
  g.IS_REACT_ACT_ENVIRONMENT = true;
  answers.byPath = {};
  answers.calls = [];
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.useRealTimers();
});

describe("HomeBreakdownSection", () => {
  it("reads the last result on opening and walks only on a press, polling until it ends", async () => {
    vi.useFakeTimers();
    answers.byPath["api/cleanup/home"] = {
      running: false,
      scanned: 0,
      result: null,
    };
    let text = await mount();
    expect(text).toContain("home の内訳");
    expect(text).toContain("まだ計測していません");
    expect(answers.calls).toEqual(["GET api/cleanup/home"]);

    answers.byPath["api/cleanup/home/measure"] = {
      running: true,
      scanned: 120,
      result: null,
    };
    answers.byPath["api/cleanup/home"] = {
      running: true,
      scanned: 5000,
      result: null,
    };
    await act(async () => {
      buttons()
        .find((b) => b.textContent === "測る")!
        .click();
    });
    expect(answers.calls).toContain("POST api/cleanup/home/measure");
    expect(host!.textContent).toContain("計測中… 120 件");

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000);
    });
    expect(host!.textContent).toContain("計測中… 5000 件");

    answers.byPath["api/cleanup/home"] = {
      running: false,
      scanned: 9000,
      result,
    };
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000);
    });
    text = host!.textContent || "";
    expect(text).toContain("もう一度計測");
    expect(text).toContain("作業コピー: ビルド成果物");
    const polls = answers.calls.filter((c) => c === "GET api/cleanup/home").length;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    expect(answers.calls.filter((c) => c === "GET api/cleanup/home").length).toBe(polls); // stopped polling
  });

  it("says per category what can be freed today, and lists working copies and af-work folders", async () => {
    answers.byPath["api/cleanup/home"] = { running: false, scanned: 0, result };
    const text = await mount();
    // 10 + 2 + 2 GiB from the cleanups that exist.
    expect(text).toContain("今ある掃除で約 14 GB 空けられます");
    expect(text).toContain("今 10 GB 空けられます。");
    expect(text).toContain("これだけを消す機能はまだありません");
    expect(text).toContain(".gradle");
    expect(text).toContain("ビルド成果物 1.8 GB");
    expect(text).toContain("git リポジトリとして読めないため");
    expect(text).toContain("セッションなし");
    // Only my-stuff lacks the Agent's marker.
    expect(text.match(/Agent が作ったものではない/g)).toHaveLength(1);
    // The share bar of the biggest category is drawn to scale.
    const bar = host!.querySelector('[data-cat="tool_cache"] .hb-bar > span') as HTMLElement;
    expect(bar.style.width).toBe("24.2%");
  });

  it("hides itself on an Agent without the endpoint", async () => {
    const text = await mount();
    expect(text).toBe("");
  });

  it("tells a shared home that freeing it moves the filesystem figure less", async () => {
    answers.byPath["api/cleanup/home"] = { running: false, scanned: 0, result };
    expect(await mount(false)).toContain("ほかの人と共有のファイルシステム全体");
  });
});
