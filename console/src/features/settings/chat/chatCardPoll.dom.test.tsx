// The Slack and Discord cards poll the bot's channels every 3 s between "verify" and the first
// channel showing up. The period does not restart with each answer, so reads overlap: an older
// answer must not fill the picker over a newer one, and a read still out when the poll stops is
// dropped.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const inspectSlack = vi.fn();
const listSlackChannels = vi.fn();
const inspectDiscord = vi.fn();
const listDiscordGuilds = vi.fn();
vi.mock("./chatApi.ts", () => ({
  inspectSlack: (...a: unknown[]) => inspectSlack(...a),
  listSlackChannels: (...a: unknown[]) => listSlackChannels(...a),
  putSlackConn: vi.fn(),
  inspectDiscord: (...a: unknown[]) => inspectDiscord(...a),
  listDiscordGuilds: (...a: unknown[]) => listDiscordGuilds(...a),
  putDiscordConn: vi.fn(),
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

const { SlackCard } = await import("./SlackCard.tsx");
const { DiscordCard } = await import("./DiscordCard.tsx");

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
g.IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | null = null;
let host: HTMLDivElement | null = null;

interface Case {
  name: string;
  render: () => React.ReactElement;
  /** Make the inspect call answer, and the list call wait for answer(i, …). */
  setup: () => void;
  list: ReturnType<typeof vi.fn>;
  /** A list answer holding the given channel names. */
  reply: (names: string[]) => unknown;
}

const cases: Case[] = [
  {
    name: "SlackCard",
    render: () => <SlackCard st={undefined} reload={() => {}} />,
    setup: () => inspectSlack.mockResolvedValue({ botName: "bot", teamName: "team" }),
    list: listSlackChannels,
    reply: (names) => ({ channels: names.map((n) => ({ id: "C-" + n, name: n })) }),
  },
  {
    name: "DiscordCard",
    render: () => <DiscordCard st={undefined} reload={() => {}} />,
    setup: () => inspectDiscord.mockResolvedValue({ botName: "bot", inviteUrl: "https://example.invalid/invite" }),
    list: listDiscordGuilds,
    reply: (names) => ({ guilds: [{ name: "g", channels: names.map((n) => ({ id: "C-" + n, name: n })) }] }),
  },
];

const pending: Array<(v: unknown) => void> = [];

async function flush() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

async function verify(c: Case) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(c.render()));
  const input = host.querySelector('input[type="password"]') as HTMLInputElement;
  const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  await act(async () => {
    setValue.call(input, "token");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  const button = Array.from(host.querySelectorAll("button")).find((b) => !b.disabled && !b.className.includes("linklike"))!;
  await act(async () => button.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  await flush();
}

async function answer(i: number, v: unknown) {
  await act(async () => pending[i](v));
  await flush();
}

async function tick() {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(3000);
  });
}

const picker = () => host!.querySelector("select");
const toggleDm = async () => {
  const link = host!.querySelector("button.linklike") as HTMLButtonElement;
  await act(async () => link.dispatchEvent(new MouseEvent("click", { bubbles: true })));
};

beforeEach(() => {
  vi.useFakeTimers();
  for (const c of cases) {
    c.setup();
    c.list.mockImplementation(() => new Promise((resolve) => pending.push(resolve)));
  }
});
afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  host?.remove();
  root = host = null;
  pending.length = 0;
  vi.clearAllMocks();
  vi.useRealTimers();
});

describe.each(cases)("$name channel poll", (c) => {
  it("reads at once and every 3 s, and stops once a channel arrives", async () => {
    await verify(c);
    expect(c.list).toHaveBeenCalledTimes(1);
    await answer(0, c.reply([]));
    expect(picker()).toBeNull();
    await tick();
    expect(c.list).toHaveBeenCalledTimes(2);
    await answer(1, c.reply(["general"]));
    expect(picker()).not.toBeNull();
    await tick();
    await tick();
    expect(c.list).toHaveBeenCalledTimes(2);
  });

  it("does not fill the picker from an answer older than one already shown", async () => {
    await verify(c);
    await tick();
    expect(pending.length).toBe(2);
    await answer(1, c.reply([]));
    await answer(0, c.reply(["general"]));
    expect(picker()).toBeNull();
  });

  it("drops a read still out when DM is chosen", async () => {
    await verify(c);
    await toggleDm();
    await answer(0, c.reply(["general"]));
    await toggleDm();
    // Back in channel mode the poll reads again at once; the dropped answer filled nothing.
    expect(c.list).toHaveBeenCalledTimes(2);
    expect(picker()).toBeNull();
    await answer(1, c.reply(["general"]));
    expect(picker()).not.toBeNull();
  });
});
