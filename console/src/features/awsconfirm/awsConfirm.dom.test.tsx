// "Confirm before use" (ADR 0126 decision 5): the card shows what the Agent lists (never the
// notification's payload), answers through the relay with the profile and session as audit
// hints, and goes away once answered.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const calls: { path: string; method: string }[] = [];
let listed: unknown[] = [];
vi.mock("../../core/api/client.ts", () => ({
  getTenant: () => "",
  api: vi.fn(async (path: string, opts?: RequestInit) => {
    calls.push({ path, method: opts?.method ?? "GET" });
    if (path === "api/aws-confirm") return { requests: listed };
    if (path.includes("/approve") || path.includes("/deny")) {
      listed = [];
      return { ok: true };
    }
    return {};
  }),
}));
const toasts: string[] = [];
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => (m: string) => toasts.push(m) }));

const { MirrorConfirmCards } = await import("./AwsConfirmCard.tsx");
const { useAwsConfirmStore } = await import("./store.ts");
const { t } = await import("../../lib/i18n/index.ts");

const request = {
  id: "req1",
  session: "s1",
  targets: [{ profile: "prod-admin", label: "prod admin", accountId: "123456789012", roleName: "AdministratorAccess", region: "us-east-1" }],
  commands: ["terraform"],
  firstAt: "2026-10-31T00:00:00Z",
  lastAt: "2026-10-31T00:00:00Z",
  expiresInSec: 900,
};

let root: Root | null = null;
let host: HTMLDivElement;

beforeEach(async () => {
  calls.length = 0;
  toasts.length = 0;
  listed = [request];
  useAwsConfirmStore.getState().reset();
  await useAwsConfirmStore.getState().refresh();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  host.remove();
});

async function render(session: string) {
  await act(async () => root!.render(<MirrorConfirmCards session={session} />));
}

describe("the confirmation card in a session's mirror", () => {
  it("shows the profile as the Agent lists it, only in its own session", async () => {
    await render("other");
    expect(host.textContent).toBe("");
    await render("s1");
    expect(host.textContent).toContain("prod admin");
    expect(host.textContent).toContain("123456789012 / AdministratorAccess");
    expect(host.textContent).toContain("terraform");
  });

  for (const [verb, label] of [
    ["approve", "awsconfirm.approve"],
    ["deny", "awsconfirm.deny"],
  ] as const) {
    it(`${verb} answers through the relay with audit hints and removes the card`, async () => {
      await render("s1");
      const btn = [...host.querySelectorAll("button")].find((b) => b.textContent === t(label))!;
      await act(async () => btn.click());
      const post = calls.find((c) => c.method === "POST")!;
      expect(post.path).toBe(`api/aws-confirm/req1/${verb}?profile=prod-admin&session=s1`);
      expect(host.textContent).toBe("");
      expect(toasts).toEqual([]);
    });
  }
});
