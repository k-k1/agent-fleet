// The wait that stops the real-browser checks (pdf:check / doc:check) from failing on a
// browser that is merely slow to open its first tab.
//
// Chromium writes DevToolsActivePort — and answers /json/list — before the tab exists, so the
// list can come back with no `type === "page"` in it. The harness used to read that list once
// and index straight into `.find(...)`, which threw
// `Cannot read properties of undefined (reading 'webSocketDebuggerUrl')` and reddened the
// console job on PRs that touched no console file at all (measured on CI 2026-09-20 and
// 2026-09-22, green on a re-run both times).
//
// This is the one part of startBrowser that can be checked without a browser, which is why
// pickPageTarget takes its fetch and its sleep as arguments.
import { describe, it, expect } from "vitest";
// The harness is plain ESM JavaScript; its JSDoc types are what this file is checked against
// (tsconfig: allowJs, checkJs false).
import { pickPageTarget } from "../../scripts/lib/headless.mjs";

type Target = { type: string; webSocketDebuggerUrl?: string };

const PAGE: Target = { type: "page", webSocketDebuggerUrl: "ws://127.0.0.1:1/devtools/page/A" };
const nap = async () => {};

describe("pickPageTarget", () => {
  it("returns the page target when it is there straight away", async () => {
    const got = await pickPageTarget(0, { fetchList: async () => [PAGE], pause: nap });
    expect(got.webSocketDebuggerUrl).toBe(PAGE.webSocketDebuggerUrl);
  });

  it("waits through the window where the port answers but the tab does not exist yet", async () => {
    // Exactly the CI failure: the endpoint is up and lists the browser target only. Three
    // rounds of that, then the tab appears.
    let calls = 0;
    const got = await pickPageTarget(0, {
      fetchList: async () => (++calls < 4 ? [{ type: "browser" } as Target] : [PAGE]),
      pause: nap,
    });
    expect(got.webSocketDebuggerUrl).toBe(PAGE.webSocketDebuggerUrl);
    expect(calls).toBe(4);
  });

  it("waits through /json/list not answering at all", async () => {
    let calls = 0;
    const got = await pickPageTarget(0, {
      fetchList: async () => {
        if (++calls < 3) throw new Error("fetch failed");
        return [PAGE];
      },
      pause: nap,
    });
    expect(got.webSocketDebuggerUrl).toBe(PAGE.webSocketDebuggerUrl);
  });

  it("never returns a target with no debugger url", async () => {
    // A page entry without webSocketDebuggerUrl is the shape that produced the original
    // TypeError one line later; treating it as "not ready" is the whole point.
    let calls = 0;
    const got = await pickPageTarget(0, {
      fetchList: async () => (++calls < 2 ? [{ type: "page" } as Target] : [PAGE]),
      pause: nap,
    });
    expect(got.webSocketDebuggerUrl).toBe(PAGE.webSocketDebuggerUrl);
  });

  it("gives up with a message that says what it did see", async () => {
    // The negative control: the wait must end, and the error has to name the state it was
    // stuck in — "never a page target" with no detail is what sent the last two
    // investigations looking at the wrong flake.
    await expect(
      pickPageTarget(0, { tries: 3, fetchList: async () => [{ type: "browser" } as Target], pause: nap }),
    ).rejects.toThrow(/never a page target.*1 target\(s\).*browser/s);

    await expect(
      pickPageTarget(0, {
        tries: 2,
        fetchList: async () => {
          throw new Error("connect ECONNREFUSED");
        },
        pause: nap,
      }),
    ).rejects.toThrow(/did not answer.*ECONNREFUSED/s);
  });
});
