// ADR 0102 on the Console side: the toast shows what the Agent lists (Settings' account and
// role), nothing starts until the member presses "Log in" in the modal, the modal shows only
// the code of the attempt its own press created, and the toast goes once the request is
// settled.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

type Json = Record<string, unknown>;
const calls: { path: string; method: string }[] = [];
let listed: Json[] = [];
let attemptReplies: Json[] = [];

vi.mock("../../core/api/client.ts", () => ({
  api: vi.fn(async (path: string) => {
    calls.push({ path, method: "GET" });
    if (path === "api/aws-login") return { requests: listed };
    if (path.includes("/attempts/")) return attemptReplies.length > 1 ? attemptReplies.shift() : attemptReplies[0];
    return {};
  }),
  apiJSON: vi.fn(async (path: string, method: string) => {
    calls.push({ path, method });
    if (path.includes("/start")) return { attempt: "att1" };
    return { ok: true };
  }),
}));

const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { AwsLoginHost } = await import("./AwsLoginHost.tsx");
const { useAwsLoginStore } = await import("./store.ts");

const prod: Json = {
  id: "0123456789abcdef01234567",
  profile: "prod",
  label: "Production",
  accountId: "123456789012",
  roleName: "Dev",
  waiters: [{ session: "s1", command: "terraform" }],
  firstAt: "2026-09-27T00:00:00Z",
  lastAt: "2026-09-27T00:00:00Z",
};

let root: Root | null = null;
let host: HTMLDivElement;

async function mount(): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <AwsLoginHost />
      </ToastProvider>,
    );
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
}

async function tick(ms: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

function button(label: string, scope: ParentNode = document.body): HTMLButtonElement {
  const b = Array.from(scope.querySelectorAll("button")).find((x) => x.textContent?.trim() === label);
  if (!b) throw new Error(`no button "${label}" in: ${document.body.textContent}`);
  return b as HTMLButtonElement;
}

beforeEach(() => {
  vi.useFakeTimers();
  calls.length = 0;
  listed = [prod];
  attemptReplies = [{ phase: "starting" }];
  useAwsLoginStore.setState({ requests: [], hidden: {}, modal: null });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host.remove();
  root = null;
  vi.useRealTimers();
});

describe("AWS login toast and modal", () => {
  it("shows Settings' account and role in a sticky toast, and starts nothing on open", async () => {
    await mount();
    const toast = document.querySelector(".ui-toast");
    expect(toast?.textContent).toContain("Production");
    expect(toast?.textContent).toContain("123456789012");
    expect(toast?.textContent).toContain("Dev");
    expect(toast?.textContent).toContain("s1 · terraform");

    await act(async () => button("Log in", toast!).click());
    expect(document.body.textContent).toContain("AWS login (Production)");
    // Closing is the answer for "not on this device": the modal must say the request stays.
    expect(document.body.textContent).toContain("Closing keeps the request");
    await tick(5000);
    expect(calls.some((c) => c.path.includes("/start"))).toBe(false);
  });

  it("shows the code of its own attempt only, and drops it when the attempt is replaced", async () => {
    await mount();
    await act(async () => useAwsLoginStore.getState().open(prod.id as string));
    const modal = document.querySelector(".ui-modal-foot")!.parentElement!;
    attemptReplies = [
      { phase: "authorize", url: "https://device.sso.ap-northeast-1.amazonaws.com/?user_code=ABCD-EFGH", code: "ABCD-EFGH" },
      { phase: "replaced" },
    ];
    await act(async () => button("Log in", modal).click());
    expect(calls).toContainEqual({ path: `api/aws-login/${prod.id}/start?profile=prod`, method: "POST" });
    await tick(400);
    expect(document.body.textContent).toContain("ABCD-EFGH");
    expect(calls.some((c) => c.path === `api/aws-login/${prod.id}/attempts/att1`)).toBe(true);
    await tick(1600);
    expect(document.body.textContent).not.toContain("ABCD-EFGH");
    expect(document.body.textContent).toContain("Another sign-in was started");
    expect(() => button("Start again")).not.toThrow();
  });

  it("says why an unexpected sign-in URL was refused", async () => {
    await mount();
    await act(async () => useAwsLoginStore.getState().open(prod.id as string));
    attemptReplies = [{ phase: "failed", message: "unexpected sign-in URL" }];
    await act(async () => button("Log in", document.querySelector(".ui-modal-foot")!).click());
    await tick(400);
    expect(document.body.textContent).toContain("was not the one this profile uses");
  });

  it("still shows the toast when the store already held the request as the host mounted", async () => {
    // The host's effect runs before ToastProvider registers its sink, so the first toast()
    // goes nowhere; the next poll must issue it rather than trust that it was shown.
    useAwsLoginStore.setState({ requests: [prod as never] });
    await mount();
    await tick(4100);
    expect(document.querySelector(".ui-toast")?.textContent).toContain("Production");
  });

  it("keeps the same toast across polls, so focus and a press in progress survive", async () => {
    await mount();
    const first = document.querySelector(".ui-toast");
    const btn = first!.querySelector(".update-toast-btn") as HTMLButtonElement;
    btn.focus();
    listed = [{ ...prod }]; // a new object with the same content, as every poll returns
    await tick(8100);
    expect(document.querySelector(".ui-toast")).toBe(first);
    expect(document.activeElement).toBe(btn);
  });

  it("withdraws the toast once the Agent no longer lists the request", async () => {
    await mount();
    expect(document.querySelector(".ui-toast")).not.toBeNull();
    listed = [];
    await tick(4100);
    expect(document.querySelector(".ui-toast")).toBeNull();
  });

  it("keeps a toast the member closed hidden in this tab, and stops polling for it", async () => {
    await mount();
    await act(async () => (document.querySelector(".ui-toast-x") as HTMLButtonElement).click());
    const before = calls.filter((c) => c.path === "api/aws-login").length;
    await tick(10000);
    expect(document.querySelector(".ui-toast")).toBeNull();
    expect(calls.filter((c) => c.path === "api/aws-login").length).toBe(before);
  });
});
