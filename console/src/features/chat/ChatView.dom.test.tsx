// DOM golden for ChatView, the regression added when features/chat was split into parts/.
//
// What it protects: the seams the split created, i.e. the props ChatView passes to its seven
// parts. Before the split the JSX was inline and could not be mixed up; now same-typed props
// sit side by side (`ChatComposerRow` alone takes four booleans — disabled / canSend /
// canAttach / hasTarget — and `ChatMessageRow` three strings — assistId / assistVoice /
// paneId). Swapping two of them keeps typecheck AND the existing tests green, so capturing the
// whole DOM and comparing it is the cheapest guard. Measured (a reviewer mutated it): swapping
// canSend and canAttach moves this golden.
//
// A mix-up that never reaches the DOM is still not caught (assistVoice vs paneId, which only
// feed the speech arguments). This check is the third one, complementing the concatenation
// comparison and the bundle comparison; it is not exhaustive.
//
// To re-capture after an intentional change:
//     npx vitest run src/features/chat/ChatView.dom.test.tsx -u
// Put the diff in the PR, as with routes.golden / wire.golden.
//
// Never signal a re-capture with an environment variable. This used to read
// `UPDATE_CHATVIEW_GOLDEN=1`, and because an exported variable stays in effect, that shell
// overwrote the golden on every run and the check could never go red — while looking green to
// its owner. Same reason `-update-routes-golden` is a test flag on the Go side: what is passed
// as an argument cannot leak. Riding on vitest's own `-u` also avoids adding a flag.
//
// The clock and the locale are pinned: baking local time or the default locale into the golden
// would make the test go red on the state of a developer's machine (setting TZ in beforeAll is
// already too late).
import { describe, it, expect, vi } from "vitest";
import { act } from "react";
import { createRoot } from "react-dom/client";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import type { Conversation } from "../../types/chat.ts";

const GOLDEN = path.join(path.dirname(fileURLToPath(import.meta.url)), "testdata", "chatview.golden.html");

// A re-capture happens only via vitest's `-u` (--update); no environment variable is read.
const UPDATE_GOLDEN = process.argv.includes("-u") || process.argv.includes("--update");

// A fixture built so that one conversation exercises all four roles, the work steps
// (narration, a single tool, and a run of consecutive tools), the plan, the context bar, a
// pasted image and the reply suggestions (pinned ones included).
const CONV: Conversation = {
  id: "c1",
  agent: "claude",
  active_agent: "claude",
  assistant_id: "a1",
  title: "テスト会話",
  model: "claude-opus-5",
  created_at: 1_700_000_000_000,
  updated_at: 1_700_000_100_000,
  tools: "af_write",
  plan: "- 計画の1行目\n- 2行目",
  plan_updated_at: 1_700_000_050_000,
  context: { tokens: 1000, read: 400, create: 300, fresh: 300, window: 200000, model: "claude-opus-5" },
  messages: [
    { role: "user", content: "こんにちは\n\n添付: ![](pasted-1.png)", ts: 1_700_000_001_000 },
    {
      role: "assistant",
      content: "はい、**了解**しました。\n\n- 一つ目\n- 二つ目",
      ts: 1_700_000_002_000,
      agent: "claude",
      model: "claude-opus-5",
      steps: [
        { text: "まず読みます", tools: ["Read"] },
        { tools: ["Grep", "Grep", "Bash"] },
        { text: "次に書きます", tools: ["Edit"] },
      ],
    },
    { role: "report", content: "報告本文", ts: 1_700_000_003_000, session: "sxxxxxx" },
    { role: "notice", content: "システム通知の本文", ts: 1_700_000_004_000 },
    { role: "user", content: "もう一度お願いします", ts: 1_700_000_005_000 },
    { role: "assistant", content: "できました。", ts: 1_700_000_006_000, agent: "claude" },
  ],
};

vi.mock("../../core/api/client.ts", async (orig) => ({
  ...((await orig()) as Record<string, unknown>),
  api: () => Promise.resolve({}),
  apiJSON: () => Promise.resolve({}),
  chatGet: () => Promise.resolve(CONV),
  chatList: () => Promise.resolve([]),
  assistantGet: () => Promise.resolve({ id: "a1", name: "アシスタント", agent: "claude", icon: "beaker", voice: "" }),
  chatSuggestReplies: () => Promise.resolve({ suggestions: [] }),
  raw: () => Promise.resolve(new Response("")),
  rel: (p: string) => p,
  errText: (e: { message?: string }) => e?.message || "",
  isTransientErr: () => false,
}));
// Time formatting is timezone-dependent, so pin it to a fixed string: the wiring is what
// this protects, not the format.
vi.mock("../../lib/intl.ts", async (orig) => ({
  ...((await orig()) as Record<string, unknown>),
  fmtDateTime: () => "01/01 00:00",
}));
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
vi.mock("../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => () => Promise.resolve(true) }));

import { ChatView } from "./ChatView.tsx";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { setSetting } from "../../lib/settings.ts";

describe("ChatView DOM", () => {
  it("renders one conversation to a DOM matching the golden", async () => {
    useWorkspaceStore.setState({ state: "running" });
    setSetting("locale", "ja"); // do not depend on the default locale
    setSetting("quickRepliesEnabled", true);
    setSetting("replySuggestEnabled", true);
    setSetting("quickReplies", {
      a: { text: "ありがとう", count: 5, at: 5 },
      b: { text: "続けて", count: 3, at: 3 },
    });
    setSetting("quickRepliesPinned", ["続けて"]);
    setSetting("ttsEnabled", true);

    const host = document.createElement("div");
    document.body.appendChild(host);
    const root = createRoot(host);
    await act(async () => {
      root.render(<ChatView conversationId="c1" paneId="p0" active />);
    });
    await act(async () => {
      await new Promise((r) => setTimeout(r, 120));
    });
    const dom = host.innerHTML.replace(/></g, ">\n<") + "\n";
    await act(async () => root.unmount());
    host.remove();

    // Positive control: check the golden is not blank, i.e. the fixture's content really
    // rendered. "They matched" alone cannot be told apart from both sides rendering nothing.
    // This must stay BEFORE the re-capture: after it, a blank DOM would be written into the
    // golden and the check would then be permanently green on blank == blank.
    for (const needle of ["chat-msg role-report", "chat-msg role-notice", "chat-suggest-chip", "テスト会話", "報告本文"]) {
      expect(dom).toContain(needle);
    }
    if (UPDATE_GOLDEN) {
      fs.mkdirSync(path.dirname(GOLDEN), { recursive: true });
      fs.writeFileSync(GOLDEN, dom);
    }
    expect(dom).toBe(fs.readFileSync(GOLDEN, "utf8"));
  });
});
