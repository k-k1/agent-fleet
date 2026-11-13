// The toast's title names the asking session as a link that opens it the way the left pane does.
// Its text is the session's name; the title the session may have set for itself is only a tooltip.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("../../core/api/client.ts", () => ({ apiJSON: async () => ({}), getTenant: () => "", api: vi.fn(async () => ({})) }));
const toasts: string[] = [];
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => (m: string) => toasts.push(m) }));
const opened: unknown[][] = [];
let openResult = true;
vi.mock("../sessions/open.ts", () => ({
  openSessionFromList: (...args: unknown[]) => {
    opened.push(args);
    return openResult;
  },
}));

const { AwsConfirmCard } = await import("./AwsConfirmCard.tsx");
const { useSessionsStore } = await import("../sessions/store.ts");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const { t } = await import("../../lib/i18n/index.ts");

const request = {
  id: "req1",
  session: "s1",
  targets: [{ profile: "prod-admin", label: "", accountId: "123456789012", roleName: "Admin" }],
  commands: ["aws ec2 describe-instances"],
  firstAt: "2026-10-31T00:00:00Z",
  lastAt: "2026-10-31T00:00:00Z",
  expiresInSec: 900,
};

let root: Root | null = null;
let host: HTMLDivElement;

beforeEach(() => {
  opened.length = 0;
  toasts.length = 0;
  openResult = true;
  useWorkspaceStore.setState({ state: "running" });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root?.unmount());
  host.remove();
});

async function render() {
  await act(async () => root!.render(<AwsConfirmCard r={request} showSession />));
}

describe("the session in a confirmation toast", () => {
  it("is a link that opens the session, titled by its name", async () => {
    const s1 = { name: "s1", kind: "claude", alive: true, title: "Session Other" };
    useSessionsStore.setState({ sessions: [s1] as never });
    await render();
    const link = host.querySelector(".aws-confirm-head button") as HTMLButtonElement;
    expect(link.textContent).toBe("s1");
    expect(link.title).toBe("Session Other");
    expect(host.textContent).toContain(t("awsconfirm.commands", { commands: "aws ec2 describe-instances" }));
    await act(async () => link.click());
    expect(opened).toEqual([[s1, false, true]]);
    expect(toasts).toEqual([]);

    openResult = false;
    await act(async () => link.click());
    expect(toasts).toEqual([t("awsconfirm.open_failed")]);
  });

  it("is plain text when the session is not in the list", async () => {
    useSessionsStore.setState({ sessions: [] });
    await render();
    expect(host.querySelector(".aws-confirm-head button")).toBeNull();
    expect(host.querySelector(".aws-confirm-head")!.textContent).toContain("s1");
    expect(host.querySelector(".aws-confirm-head")!.textContent).not.toContain("<0>");
  });
});
