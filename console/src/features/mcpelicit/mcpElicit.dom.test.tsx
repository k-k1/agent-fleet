// The confirmation card of a held MCP tool call (ADR 0146 decision 6): it shows what the Agent
// lists, answers only with the member's own choice, and goes away once answered.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const posts: unknown[] = [];
let listed: unknown[] = [];
let answerReply: unknown = { ok: true };
vi.mock("../../core/api/client.ts", async (orig) => ({
  ...(await orig<typeof import("../../core/api/client.ts")>()),
  getTenant: () => "",
  api: vi.fn(async (path: string) => (path === "api/mcp-elicitations" ? { elicitations: listed } : {})),
  apiJSON: vi.fn(async (path: string, _method: string, body: unknown) => {
    if (path !== "api/mcp-elicitations/answer") return {};
    posts.push(body);
    const r = answerReply;
    if (!(r as { error?: unknown }).error) listed = [];
    return r;
  }),
}));
const toasts: string[] = [];
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => (m: string) => toasts.push(m) }));

const { MirrorElicitCards } = await import("./McpElicitCard.tsx");
const { useMcpElicitStore } = await import("./store.ts");
const { t } = await import("../../lib/i18n/index.ts");

const held = {
  id: "e1",
  serverId: "mcp-1",
  server: "gmail",
  serverLabel: "Gmail <0>x</0>",
  tool: "send_mail",
  session: "s1",
  arguments: `{"to":"a@example.com","n":1.0}`,
  inputs: [
    {
      key: "approval_choice",
      message: "Send **mail** to a@example.com",
      fields: [
        {
          name: "approval_choice",
          title: "Allow?",
          required: true,
          options: [
            { value: "ALLOW_ONCE", label: "Allow…" },
            { value: "ALLOW_TOOL_ALWAYS", label: "Allow…" },
            { value: "DENY_ONCE", label: "Deny" },
          ],
        },
      ],
    },
    { key: "extra", fields: [{ name: "note", options: [{ value: "N", label: "Note" }] }] },
  ],
  createdAt: "2026-11-27T00:00:00Z",
  deadline: "2026-11-27T00:10:00Z",
};

let root: Root | null = null;
let host: HTMLDivElement;

beforeEach(async () => {
  posts.length = 0;
  toasts.length = 0;
  answerReply = { ok: true };
  listed = [held];
  useMcpElicitStore.getState().reset();
  await useMcpElicitStore.getState().refresh();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root?.unmount());
  host.remove();
});

async function render(session: string) {
  await act(async () => root!.render(<MirrorElicitCards session={session} />));
}
const button = (label: string) => [...host.querySelectorAll("button")].find((b) => b.textContent === label)!;
const radio = (label: string) =>
  [...host.querySelectorAll("label")].find((l) => l.textContent === label)!.querySelector("input")!;
const click = async (el: HTMLElement) => act(async () => el.click());

describe("the MCP confirmation card", () => {
  it("shows the call as the Agent lists it", async () => {
    await render("s1");
    const text = host.textContent!;
    expect(text).toContain("send_mail");
    // A name is an element, never part of the catalogue line: its markup shows as written.
    expect(text).toContain("Gmail <0>x</0>");
    expect(host.querySelector("pre")!.textContent).toBe(`{\n  "to": "a@example.com",\n  "n": 1.0\n}`);
    expect(text).toContain("Send **mail** to a@example.com");
    // #5282: two options whose labels read the same show their values.
    expect(radio("Allow… (ALLOW_ONCE)")).toBeTruthy();
    expect(radio("Allow… (ALLOW_TOOL_ALWAYS)")).toBeTruthy();
    expect(radio("Deny")).toBeTruthy();
  });
  it("is not shown in another session's mirror", async () => {
    await render("s2");
    expect(host.textContent).toBe("");
  });
  it("sends only the member's choice, every input named", async () => {
    await render("s1");
    const send = button(t("mcpelicit.send"));
    expect(send.disabled).toBe(true);
    await click(radio("Allow… (ALLOW_ONCE)"));
    expect(send.disabled).toBe(false);
    await click(send);
    expect(posts).toEqual([{ id: "e1", action: "accept", choices: { approval_choice: { approval_choice: "ALLOW_ONCE" }, extra: {} } }]);
    expect(host.textContent).toBe("");
  });
  it("sends an optional choice the member made, and drops it when taken back", async () => {
    await render("s1");
    await click(radio("Deny"));
    await click(radio("Note"));
    await click(radio(t("mcpelicit.no_choice")));
    await click(radio("Note"));
    await click(button(t("mcpelicit.send")));
    expect(posts).toEqual([{ id: "e1", action: "accept", choices: { approval_choice: { approval_choice: "DENY_ONCE" }, extra: { note: "N" } } }]);
  });
  it("declines with nothing else", async () => {
    await render("s1");
    await click(radio("Allow… (ALLOW_ONCE)"));
    await click(button(t("mcpelicit.decline")));
    expect(posts).toEqual([{ id: "e1", action: "decline" }]);
    expect(host.textContent).toBe("");
  });
  it("says why an answer failed and keeps the card", async () => {
    answerReply = { error: { code: "mcp_elicitation_answer", message: "x" } };
    await render("s1");
    await click(radio("Deny"));
    await click(button(t("mcpelicit.send")));
    expect(toasts).toEqual([t("mcpelicit.answer_failed", { reason: t("err.mcp_elicitation_answer") })]);
    // The card comes back, to be answered again.
    expect(button(t("mcpelicit.decline")).disabled).toBe(false);
  });
});
