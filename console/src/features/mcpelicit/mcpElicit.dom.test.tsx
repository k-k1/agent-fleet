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
    const r = await answerReply;
    if (!(r as { error?: unknown }).error) listed = [];
    return r;
  }),
}));
const toasts: string[] = [];
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => (m: string) => toasts.push(m) }));

const { MirrorElicitCards, McpElicitCard } = await import("./McpElicitCard.tsx");
const { useMcpElicitStore } = await import("./store.ts");
const { t } = await import("../../lib/i18n/index.ts");

const held = {
  id: "e1",
  serverId: "mcp-1",
  server: "gmail",
  serverLabel: "Gmail <0>x</0>",
  origin: "https://gmailmcp.googleapis.com",
  tool: "send_mail",
  session: "s1",
  arguments: `{"to":"a@example.com","n":1.0,"file":"report\u202Efdp.exe"}`,
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
  // Long past on this browser's clock, as on one running ahead of the Agent's: the card goes by
  // the seconds left alone.
  deadline: "2000-01-01T00:00:00Z",
  expiresInSec: 600,
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
    // The label is the member's own text, which a session can change: where the call went shows too.
    expect(text).toContain("(https://gmailmcp.googleapis.com)");
    // A direction control inside a string is written as its escape, so nothing reorders on screen.
    expect(host.querySelector("pre")!.textContent).toBe(
      `{\n  "to": "a@example.com",\n  "n": 1.0,\n  "file": "report\\u202Efdp.exe"\n}`,
    );
    expect(text).toContain("Send **mail** to a@example.com");
    // #5282: two options whose labels read the same show their values.
    expect(radio("Allow… (ALLOW_ONCE)")).toBeTruthy();
    expect(radio("Allow… (ALLOW_TOOL_ALWAYS)")).toBeTruthy();
    expect(radio("Deny")).toBeTruthy();
  });
  // A toast keeps the element it was given; once the call leaves the list it shows nothing.
  it("shows nothing for a call no longer listed", async () => {
    await act(async () => root!.render(<McpElicitCard id="gone" showSession />));
    expect(host.textContent).toBe("");
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
    await click(button(t("mcpelicit.send")));
    expect(posts).toEqual([{ id: "e1", action: "accept", choices: { approval_choice: { approval_choice: "DENY_ONCE" }, extra: { note: "N" } } }]);
  });
  it("drops an optional choice the member took back", async () => {
    await render("s1");
    await click(radio("Deny"));
    await click(radio("Note"));
    await click(button(t("mcpelicit.clear_choice")));
    expect(radio("Note").checked).toBe(false);
    await click(button(t("mcpelicit.send")));
    expect(posts).toEqual([{ id: "e1", action: "accept", choices: { approval_choice: { approval_choice: "DENY_ONCE" }, extra: {} } }]);
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
    // The card stays with the member's choice, to be sent again.
    expect(radio("Deny").checked).toBe(true);
    answerReply = { ok: true };
    await click(button(t("mcpelicit.send")));
    expect(posts[1]).toEqual({ id: "e1", action: "accept", choices: { approval_choice: { approval_choice: "DENY_ONCE" }, extra: {} } });
  });
  it("keeps the form, disabled, while an answer is on its way", async () => {
    let release!: () => void;
    answerReply = new Promise<unknown>((r) => (release = () => r({ error: { code: "http_502" } })));
    await render("s1");
    await click(radio("Deny"));
    await click(radio("Note"));
    await click(button(t("mcpelicit.send")));
    expect(button(t("mcpelicit.send")).disabled).toBe(true);
    expect(radio("Deny").checked).toBe(true);
    // The clear button sits in the legend, which a disabled fieldset does not cover.
    expect(button(t("mcpelicit.clear_choice")).disabled).toBe(true);
    await act(async () => release());
    expect(button(t("mcpelicit.send")).disabled).toBe(false);
  });
  it("is gone once its time is up, even without a new list", async () => {
    listed = [{ ...held, expiresInSec: 0 }];
    await useMcpElicitStore.getState().refresh();
    await render("s1");
    expect(host.textContent).toBe("");
  });
  // The form's keys are the server's: none may be read off an object's prototype.
  it("treats keys named like object members as plain keys", async () => {
    listed = [
      {
        ...held,
        inputs: [
          { key: "constructor", fields: [{ name: "constructor", required: true, options: [{ value: "Y", label: "Yes" }] }] },
          { key: "__proto__", fields: [{ name: "__proto__", required: true, options: [{ value: "Z", label: "Zed" }] }] },
        ],
      },
    ];
    await useMcpElicitStore.getState().refresh();
    await render("s1");
    const send = button(t("mcpelicit.send"));
    expect(send.disabled).toBe(true);
    await click(radio("Yes"));
    expect(send.disabled).toBe(true);
    await click(radio("Zed"));
    expect(send.disabled).toBe(false);
    await click(send);
    expect(JSON.stringify(posts[0])).toBe(
      JSON.stringify({ id: "e1", action: "accept", choices: { constructor: { constructor: "Y" }, ["__proto__"]: { ["__proto__"]: "Z" } } }),
    );
  });
  // Joining the key and the field name with a separator could put two fields in one group.
  it("keeps fields whose names join alike in separate radio groups", async () => {
    listed = [
      {
        ...held,
        inputs: [
          { key: "a:b", fields: [{ name: "c", required: true, options: [{ value: "1", label: "One" }] }] },
          { key: "a", fields: [{ name: "b:c", required: true, options: [{ value: "2", label: "Two" }] }] },
        ],
      },
    ];
    await useMcpElicitStore.getState().refresh();
    await render("s1");
    await click(radio("One"));
    await click(radio("Two"));
    expect(radio("One").checked).toBe(true);
    expect(radio("Two").checked).toBe(true);
  });
});
