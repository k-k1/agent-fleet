// Where the run rows appear (ADR 0126 decision 2): between the mirror's turns by time, and in
// Settings' "Recent runs" with their session.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

let runsReply: unknown = { runs: [] };
vi.mock("../../core/api/client.ts", () => ({
  getTenant: () => "",
  api: vi.fn(async (path: string) => (path === "api/aws-runs" ? runsReply : {})),
}));
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

const { RecentCloudRuns, cloudRunCards } = await import("./cloudRuns.tsx");
const { TranscriptView } = await import("../mirror/transcript/TranscriptView.tsx");
const { t } = await import("../../lib/i18n/index.ts");

let root: Root | null = null;
let host: HTMLDivElement;

async function render(node: React.ReactNode): Promise<void> {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(node);
  });
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0));
  });
}

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host?.remove();
});

const run = (id: string, startedAt: string, extra: Record<string, unknown> = {}) => ({
  id,
  provider: "aws",
  startedAt,
  profile: "prod",
  account: "123456789012",
  role: "Dev",
  exitCode: 0,
  durationMs: 10,
  ...extra,
});

describe("RecentCloudRuns", () => {
  it("lists the runs with their session, and a run without one", async () => {
    runsReply = { runs: [run("b", "2026-10-30T12:00:02Z", { session: "deployer" }), run("a", "2026-10-30T12:00:01Z")] };
    await render(<RecentCloudRuns />);
    const items = [...host.querySelectorAll(".cloud-runs-list li")];
    expect(items).toHaveLength(2);
    expect(items[0].textContent).toContain("deployer");
    expect(items[0].textContent).toContain("123456789012 / Dev");
    expect(items[1].textContent).toContain(t("cloudruns.no_session"));
  });
  it("says so when there are none, and when the list cannot be read", async () => {
    runsReply = { runs: [] };
    await render(<RecentCloudRuns />);
    expect(host.textContent).toContain(t("cloudruns.recent_empty"));
    act(() => root?.unmount());
    host.remove();
    runsReply = { error: { message: "agent down" } };
    await render(<RecentCloudRuns />);
    expect(host.textContent).toContain("agent down");
  });
});

describe("cloudRunCards in the mirror", () => {
  it("places a run between the turns it happened between", async () => {
    const groups = [
      { role: "user", text: "first", ts: "2026-10-30T12:00:00Z", idx: 0 },
      { role: "user", text: "second", ts: "2026-10-30T12:10:00Z", idx: 1 },
    ];
    const caps = { session: "s1" } as never;
    await render(
      <TranscriptView groups={groups as never} caps={caps} inlineCards={cloudRunCards([run("r1", "2026-10-30T12:05:00Z") as never])} />,
    );
    const text = host.textContent ?? "";
    const at = text.indexOf("123456789012 / Dev");
    expect(at).toBeGreaterThan(text.indexOf("first"));
    expect(at).toBeLessThan(text.indexOf("second"));
  });
});
