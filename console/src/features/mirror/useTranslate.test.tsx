// useTranslate: the module-level stash that lets a shown translation survive a remount or a
// reused MirrorView swapping its `session` prop (Pane.tsx's same-cell tab reuse, and the
// chat-to-terminal unmount case) without one — the bug being that `shown` had nowhere to live
// but component state, so looking at another tab and back silently flipped an open translation
// back to the original. Also covers the chunked request for an answer too long for one
// translate request part (translate.ts's splitForTranslate), reassembled here into the same
// whole-answer cache entry the rest of the mirror reads by.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, create, type ReactTestRenderer } from "react-test-renderer";

interface MockCall {
  path: string;
  opts?: RequestInit;
}

const calls: MockCall[] = [];
const apiMock = vi.fn((path: string, opts?: RequestInit) => {
  calls.push({ path, opts });
  if (opts?.method === "POST") {
    const body = JSON.parse(String(opts.body)) as { to: string; parts: { text: string }[] };
    return Promise.resolve({
      lang: body.to,
      parts: body.parts.map((p) => ({ hash: translateHash(p.text), text: "訳:" + p.text, cached: false })),
    });
  }
  return Promise.resolve({ lang: "ja", entries: {} });
});

vi.mock("../../core/api/client.ts", () => ({
  api: (path: string, opts?: RequestInit) => apiMock(path, opts),
  errText: (e: unknown) => (e && typeof e === "object" && "message" in e ? String((e as { message?: string }).message) : "err"),
}));

import { useTranslate } from "./useTranslate.ts";
import type { TranscriptTranslateWiring } from "./useTranslate.ts";
import { splitForTranslate, translateHash } from "./translate.ts";

let seen: (TranscriptTranslateWiring | undefined)[] = [];

function Harness({ session }: { session: string }) {
  seen.push(useTranslate({ session, lang: "ja", enabled: true }));
  return null;
}

let renderer: ReactTestRenderer | null = null;

function mount(session: string): void {
  act(() => {
    renderer = create(<Harness session={session} />);
  });
}

function switchTo(session: string): void {
  act(() => {
    renderer!.update(<Harness session={session} />);
  });
}

function unmount(): void {
  act(() => renderer!.unmount());
  renderer = null;
}

const latest = (): TranscriptTranslateWiring | undefined => seen[seen.length - 1];

/** Lets the resolved api() promises (and their chained .then()s) reach React state. */
async function flush(): Promise<void> {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
}

beforeEach(() => {
  seen = [];
  calls.length = 0;
  apiMock.mockClear();
});

afterEach(() => {
  if (renderer) unmount();
});

const TEXT = "Done — the tests are green and the branch is pushed.";
const KEY = translateHash(TEXT);

// stateStore is module-level (that is the whole point), so each test uses its own session name
// to stay isolated from what earlier tests left behind — there is no reset hook between tests,
// same as the real store never being cleared between one session and the next.

describe("useTranslate — surviving a tab switch", () => {
  it("keeps a shown translation when the pane is reused for another session and back", async () => {
    mount("tabswitch-a");
    await flush();
    act(() => latest()!.toggle(KEY, [TEXT]));
    await flush();
    expect(latest()!.shown(KEY)).toBe(true);
    const postCallsAfterFirstPress = calls.filter((c) => c.opts?.method === "POST").length;
    expect(postCallsAfterFirstPress).toBe(1);

    // The pane's session prop changes without unmounting (Pane.tsx reuses one MirrorView across
    // same-cell tabs) — a different session must not see the other one's translation.
    switchTo("tabswitch-b");
    await flush();
    expect(latest()!.shown(KEY)).toBe(false);

    // Switching back must restore exactly what was left open, with no new network round trip:
    // the translation is still held from the first press.
    switchTo("tabswitch-a");
    await flush();
    expect(latest()!.shown(KEY)).toBe(true);
    expect(latest()!.get(TEXT)).toBe("訳:" + TEXT);
    expect(calls.filter((c) => c.opts?.method === "POST")).toHaveLength(postCallsAfterFirstPress);
  });

  it("keeps a shown translation across a full remount", async () => {
    mount("remount-a");
    await flush();
    act(() => latest()!.toggle(KEY, [TEXT]));
    await flush();
    expect(latest()!.shown(KEY)).toBe(true);

    // A chat-to-terminal tab switch unmounts MirrorView entirely (Pane.tsx's showMirror branch).
    unmount();
    mount("remount-a");
    // Restored synchronously from the module stash by the lazy initializer — true even before
    // the GET /translations re-fetch (which is asserted below to still fire and not clobber it).
    expect(latest()!.shown(KEY)).toBe(true);
    expect(latest()!.get(TEXT)).toBe("訳:" + TEXT);
    await flush();
    expect(latest()!.shown(KEY)).toBe(true);
    expect(latest()!.get(TEXT)).toBe("訳:" + TEXT);
  });
});

describe("useTranslate — splitting a too-long answer", () => {
  it("sends a long answer as several request parts in one call and reassembles the translation", async () => {
    const paragraph = "This paragraph repeats itself so the whole answer is long enough to split. ";
    const longText = Array.from({ length: 600 }, (_, i) => paragraph + i).join("\n\n"); // well over 32 KiB
    const expectedChunks = splitForTranslate(longText);
    expect(expectedChunks.length).toBeGreaterThan(1); // sanity: this test exercises the split path

    mount("split-a");
    await flush();
    const key = translateHash(longText);
    act(() => latest()!.toggle(key, [longText]));
    await flush();

    const postCalls = calls.filter((c) => c.opts?.method === "POST");
    expect(postCalls).toHaveLength(1); // one request, not one per chunk
    const sentParts = (JSON.parse(String(postCalls[0].opts!.body)) as { parts: { text: string }[] }).parts;
    expect(sentParts.map((p) => p.text)).toEqual(expectedChunks);

    expect(latest()!.shown(key)).toBe(true);
    expect(latest()!.get(longText)).toBe(expectedChunks.map((c) => "訳:" + c).join(""));
  });
});
