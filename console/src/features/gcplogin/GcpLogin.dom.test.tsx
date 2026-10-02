// ADR 0107 decision 3 on the Console side: the toast shows what the Agent lists, nothing starts
// until the member presses "Log in", the sign-in link and the single code field appear only for
// the attempt this modal's own press created (never one named by a notification or a list), and
// the code travels in exactly one request: the submit to that attempt.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

type Json = Record<string, unknown>;
const calls: { path: string; method: string; body: string }[] = [];
let listed: Json[] = [];
let attemptReplies: Json[] = [];
let codeReply: Json = { ok: true };

vi.mock("../../core/api/client.ts", () => ({
  api: vi.fn(async (path: string, opts?: RequestInit) => {
    calls.push({ path, method: opts?.method || "GET", body: String(opts?.body ?? "") });
    if (path === "api/gcp-login") return { requests: listed };
    if (path.includes("/attempts/")) return attemptReplies.length > 1 ? attemptReplies.shift() : attemptReplies[0];
    return {};
  }),
  apiJSON: vi.fn(async (path: string, method: string, body?: unknown) => {
    calls.push({ path, method, body: body === undefined ? "" : JSON.stringify(body) });
    if (path.endsWith("/code")) return codeReply;
    if (path.includes("/start")) return { attempt: "att1" };
    return { ok: true };
  }),
}));

const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { GcpLoginHost } = await import("./GcpLoginHost.tsx");
const { GcpProfileLoginModal } = await import("./GcpProfileLoginModal.tsx");
const { useGcpLoginStore } = await import("./store.ts");

// Built at run time: nothing secret-shaped is a literal in the tree.
const rnd = () => Math.random().toString(36).slice(2) + Math.random().toString(36).slice(2);
let CODE = "";
let URL_ = "";

const prod: Json = {
  id: "0123456789abcdef01234567",
  profile: "prod",
  label: "Production",
  project: "prod-project",
  account: "dev@example.com",
  waiters: [{ session: "s1", command: "terraform" }],
  firstAt: "2026-10-02T00:00:00Z",
  lastAt: "2026-10-02T00:00:00Z",
};

let root: Root | null = null;
let host: HTMLDivElement;

async function render(node: React.ReactNode): Promise<void> {
  await act(async () => {
    root!.render(<ToastProvider>{node}</ToastProvider>);
  });
  await tick(0);
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

const modalFoot = () => document.querySelector(".ui-modal-foot") as HTMLElement;
const codeField = () => document.querySelector<HTMLInputElement>("#gcp-login-code");

async function typeCode(value: string): Promise<void> {
  const input = codeField()!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

beforeEach(() => {
  vi.useFakeTimers();
  calls.length = 0;
  CODE = "4/0" + rnd();
  URL_ = "https://accounts.google.com/o/oauth2/auth?client_id=" + rnd() + "&redirect_uri=x";
  listed = [prod];
  attemptReplies = [{ phase: "starting" }];
  codeReply = { ok: true };
  useGcpLoginStore.setState({ requests: [], hidden: {}, modal: null });
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

describe("Google Cloud login toast and modal", () => {
  it("shows the Agent's profile and project in a toast, and starts nothing on open", async () => {
    await render(<GcpLoginHost />);
    const toast = document.querySelector(".ui-toast");
    expect(toast?.textContent).toContain("Production");
    expect(toast?.textContent).toContain("prod-project");
    expect(toast?.textContent).toContain("s1 · terraform");
    await act(async () => button("Log in", toast!).click());
    expect(document.body.textContent).toContain("Google Cloud login (Production)");
    await tick(5000);
    expect(calls.some((c) => c.path.includes("/start") || c.path.includes("/attempts/"))).toBe(false);
    expect(codeField()).toBeNull();
  });

  it("never adopts an attempt or a URL named anywhere but its own press", async () => {
    // A list entry (or a forged notification) carrying an attempt id and a URL.
    listed = [{ ...prod, attempt: "att9", url: URL_ }];
    await render(<GcpLoginHost />);
    await act(async () => useGcpLoginStore.getState().open(prod.id as string));
    await tick(5000);
    expect(codeField()).toBeNull();
    expect(document.body.textContent).not.toContain(URL_);
    expect(calls.some((c) => c.path.includes("/attempts/"))).toBe(false);
  });

  it("shows the link and one code field for its own attempt, and sends the code only in the submit", async () => {
    await render(<GcpLoginHost />);
    await act(async () => useGcpLoginStore.getState().open(prod.id as string));
    attemptReplies = [{ phase: "authorize", url: URL_ }];
    await act(async () => button("Log in", modalFoot()).click());
    expect(calls).toContainEqual({ path: `api/gcp-login/${prod.id}/start?profile=prod`, method: "POST", body: "" });
    await tick(400);
    expect(calls.some((c) => c.path === "api/gcp-login/profiles/prod/attempts/att1")).toBe(true);
    expect(document.body.textContent).toContain(URL_);
    expect(document.querySelectorAll("input").length).toBe(1);
    expect(document.body.textContent).toContain("Paste a code only into a login you started yourself");

    await typeCode(CODE);
    await act(async () => button("Submit code").click());
    const withCode = calls.filter((c) => c.path.includes(CODE) || c.body.includes(CODE));
    expect(withCode).toEqual([
      { path: "api/gcp-login/profiles/prod/attempts/att1/code", method: "POST", body: JSON.stringify({ code: CODE }) },
    ]);
    expect(codeField()!.disabled).toBe(true);
    // A second press sends nothing more.
    await act(async () => button("Submit code").click());
    expect(calls.filter((c) => c.path.endsWith("/code")).length).toBe(1);
    attemptReplies = [{ phase: "done" }];
    await tick(1600);
    expect(document.body.textContent).toContain("Logged in.");
    expect(codeField()).toBeNull();
    expect(calls.filter((c) => c.path.includes(CODE) || c.body.includes(CODE)).length).toBe(1);
  });

  it("drops the link and the field when its attempt is replaced", async () => {
    await render(<GcpLoginHost />);
    await act(async () => useGcpLoginStore.getState().open(prod.id as string));
    attemptReplies = [{ phase: "authorize", url: URL_ }, { phase: "replaced" }];
    await act(async () => button("Log in", modalFoot()).click());
    await tick(400);
    expect(codeField()).not.toBeNull();
    await tick(1600);
    expect(codeField()).toBeNull();
    expect(document.body.textContent).not.toContain(URL_);
    expect(document.body.textContent).toContain("Another sign-in was started");
  });

  it("never carries a code typed for one attempt over to the next", async () => {
    await render(<GcpLoginHost />);
    await act(async () => useGcpLoginStore.getState().open(prod.id as string));
    attemptReplies = [{ phase: "authorize", url: URL_ }, { phase: "failed", message: "x" }];
    await act(async () => button("Log in", modalFoot()).click());
    await tick(400);
    await typeCode(CODE);
    await tick(1600);
    expect(codeField()).toBeNull();
    attemptReplies = [{ phase: "authorize", url: URL_ + "2" }];
    await act(async () => button("Start again", modalFoot()).click());
    await tick(400);
    expect(codeField()!.value).toBe("");
    expect(button("Submit code").disabled).toBe(true);
    await act(async () => button("Submit code").click());
    expect(calls.some((c) => c.path.includes(CODE) || c.body.includes(CODE))).toBe(false);
  });

  it("says why a code was refused and lets the member try again", async () => {
    await render(<GcpLoginHost />);
    await act(async () => useGcpLoginStore.getState().open(prod.id as string));
    attemptReplies = [{ phase: "authorize", url: URL_ }];
    codeReply = { error: { code: "bad_code", message: "no" } };
    await act(async () => button("Log in", modalFoot()).click());
    await tick(400);
    await typeCode("not a code");
    await act(async () => button("Submit code").click());
    expect(document.body.textContent).toContain("does not look like a verification code");
    expect(codeField()!.disabled).toBe(false);
  });

  it("names the Compute Engine prompt and an unexpected URL", async () => {
    await render(<GcpLoginHost />);
    await act(async () => useGcpLoginStore.getState().open(prod.id as string));
    attemptReplies = [{ phase: "failed", message: "unexpected sign-in URL" }];
    await act(async () => button("Log in", modalFoot()).click());
    await tick(400);
    expect(document.body.textContent).toContain("is not Google's");
    attemptReplies = [
      {
        phase: "failed",
        message:
          "gcloud asked a question the Console login does not answer (on a Compute Engine VM: whether to use a personal account); log in from a terminal",
      },
    ];
    await act(async () => button("Start again", modalFoot()).click());
    await tick(400);
    expect(document.body.textContent).toContain("af-gcloud-exec --login");
  });

  it("starts Settings' Log in again with force, through the profile's routes", async () => {
    const closed = vi.fn();
    await render(
      <GcpProfileLoginModal
        profile={{ name: "prod", label: "Production", project: "prod-project", account: "" }}
        force
        onClose={closed}
      />,
    );
    expect(codeField()).toBeNull();
    attemptReplies = [{ phase: "authorize", url: URL_ }];
    await act(async () => button("Log in again", modalFoot()).click());
    expect(calls).toContainEqual({ path: "api/gcp-login/profiles/prod/start?force=1", method: "POST", body: "" });
    await tick(400);
    expect(codeField()).not.toBeNull();
  });
});
