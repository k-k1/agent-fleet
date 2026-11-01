// Render test for the item details' session list (#3166): which sessions offer Remove, the
// confirmation it goes through, and what a failed removal leaves on screen.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const { WorkItemDetailModal } = await import("./WorkItemDetailModal.tsx");
const { t } = await import("../../lib/i18n/index.ts");
type WorkItem = import("./read.ts").WorkItem;
type WorkItemSessionRef = import("./read.ts").WorkItemSessionRef;

let root: Root | null = null;
let host: HTMLDivElement;

const item: WorkItem = {
  id: "1",
  queryId: "q1",
  provider: "jira",
  kind: "issue",
  key: "PROJ-1",
  title: "Login",
  state: "open",
  url: "",
  assignee: "",
  labels: [],
  labelColors: {},
  repo: "",
  updatedAt: "2026-11-01T00:00:00Z",
  checks: { state: "", total: 0, failed: 0, pending: 0 },
  mergeable: "",
};

const ref = (name: string, over: Partial<WorkItemSessionRef> = {}): WorkItemSessionRef => ({
  id: `link:${name}`,
  provider: "jira",
  itemKey: "PROJ-1",
  sessionName: name,
  repo: "",
  branch: "",
  createdAt: "",
  ...over,
});

const render = async (started: WorkItemSessionRef[], onUnlink?: (name: string) => Promise<string>) => {
  await act(async () => {
    root!.render(
      <WorkItemDetailModal
        item={item}
        repos={[]}
        defaultRepo=""
        started={started}
        onClose={() => {}}
        onPick={() => {}}
        onStartHub={() => {}}
        sessionRef={() => ({ state: "live", title: "" })}
        onOpenSession={() => {}}
        onReport={() => {}}
        onUnlink={onUnlink}
      />,
    );
  });
};

const rows = () => [...document.querySelectorAll<HTMLLIElement>(".wi-dsrow")];
const removeOf = (li: HTMLLIElement) => li.querySelector<HTMLButtonElement>(".wi-dunlink");
const confirmButton = () =>
  [...document.querySelectorAll<HTMLButtonElement>(".confirm-actions button")].find(
    (b) => b.textContent === t("wi.unlink_confirm"),
  );

beforeEach(() => {
  localStorage.clear();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  host.remove();
  root = null;
});

describe("WorkItemDetailModal session list", () => {
  it("offers Remove for a link but never for a launch, and tags a finished PR with its state", async () => {
    await render(
      [
        ref("sledger", { id: "1" }), // a ledger row: a launch
        ref("sstart", { role: "started" }),
        ref("swork", { role: "working", state: "merged" }),
      ],
      async () => "",
    );
    expect(rows().map((li) => !!removeOf(li))).toEqual([false, false, true]);
    expect(rows()[2].textContent).toContain(t("srow.pr_state.merged"));
  });

  it("draws no Remove button without a handler", async () => {
    await render([ref("swork", { role: "working" })]);
    expect(rows().map((li) => !!removeOf(li))).toEqual([false]);
  });

  it("removes only after the confirmation, and hides the row once the Agent agreed", async () => {
    const onUnlink = vi.fn(async () => "");
    await render([ref("swork", { role: "working" }), ref("swatch", { role: "watching" })], onUnlink);
    await act(async () => removeOf(rows()[0])!.click());
    expect(onUnlink).not.toHaveBeenCalled();
    await act(async () => confirmButton()!.click());
    expect(onUnlink).toHaveBeenCalledWith("swork");
    expect(rows().map((li) => li.textContent?.includes("swatch"))).toEqual([true]);
    expect(confirmButton()).toBeUndefined();
  });

  it("keeps the row and shows the error when the removal fails", async () => {
    await render([ref("swork", { role: "working" })], async () => "nope");
    await act(async () => removeOf(rows()[0])!.click());
    await act(async () => confirmButton()!.click());
    expect(rows()).toHaveLength(1);
    expect(document.querySelector(".confirm [role=alert]")?.textContent).toBe("nope");
  });
});
