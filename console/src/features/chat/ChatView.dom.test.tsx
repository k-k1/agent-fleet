// ChatView の DOM ゴールデン（features/chat を parts/ へ割ったときに足した回帰）。
//
// **何を守っているか**: 分割で新しく生まれた「継ぎ目」＝ ChatView が 7 つの部品へ渡す props。
// 分割前は JSX が inline だったので取り違えようが無かったが、いまは同じ型の props が並ぶ
// （`ChatComposerRow` だけで disabled / canSend / canAttach / hasTarget の boolean が 4 本、
// `ChatMessageRow` は assistId / assistVoice / paneId の string が 3 本）。**入れ替えても
// typecheck は緑・既存のテストも緑**なので、DOM を丸ごと撮って突き合わせるのがいちばん安い。
// 実測（レビュワーが変異で確認）: canSend と canAttach を入れ替えるとこのゴールデンが動く。
//
// **DOM に出ない取り違えは、これでも捕まらない**（assistVoice と paneId のように読み上げの
// 引数にしか効かないもの）。この検査は「①連結比較・②バンドル照合を補う 3 番目」であって
// 万能ではない。
//
// **意図して変えたときの撮り直し**:
//     npx vitest run src/features/chat/ChatView.dom.test.tsx -u
// 差分は PR に載せること（routes.golden / wire.golden と同じ運用）。
//
// 🔴 **撮り直しの合図は環境変数にしない。** 以前は `UPDATE_CHATVIEW_GOLDEN=1` を見ていたが、
// **環境変数は一度 export すると以後ずっと効く**ので、その手元では毎回ゴールデンが上書きされ、
// **この検査が永久に赤くならない**（しかも本人には緑にしか見えない）。Go 側の
// `-update-routes-golden` がテストフラグなのと同じ理由で、**引数で渡すものは漏れ込まない。**
// vitest 自身の `-u` に相乗りしているので、独自フラグを増やしてもいない。
//
// 時刻とロケールは固定する — ゴールデンにローカル時刻や既定ロケールが焼かれると、
// **開発機の状態で赤くなるテスト**になる（TZ を beforeAll で入れても遅い）。
import { describe, it, expect, vi } from "vitest";
import { act } from "react";
import { createRoot } from "react-dom/client";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import type { Conversation } from "../../types/chat.ts";

const GOLDEN = path.join(path.dirname(fileURLToPath(import.meta.url)), "testdata", "chatview.golden.html");

// 撮り直しは vitest の `-u`（--update）だけで起きる。**環境変数は読まない**（上記の理由）。
const UPDATE_GOLDEN = process.argv.includes("-u") || process.argv.includes("--update");

// 4 つの role・作業過程（語り＋単発ツール＋連続ツールの束）・計画・コンテキストバー・
// 貼り付け画像・サジェスト（ピン留め込み）が 1 本の会話に全部出るように組んだ fixture。
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
// 時刻の整形はタイムゾーン依存なので固定文字列に落とす（守りたいのは配線であって書式ではない）。
vi.mock("../../lib/intl.ts", async (orig) => ({
  ...((await orig()) as Record<string, unknown>),
  fmtDateTime: () => "01/01 00:00",
}));
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
vi.mock("../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => () => Promise.resolve(true) }));

import { ChatView } from "./ChatView.tsx";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { setSetting } from "../../lib/settings.ts";

describe("ChatView の DOM", () => {
  it("会話 1 本を描いたときの DOM がゴールデンと一致する", async () => {
    useWorkspaceStore.setState({ state: "running" });
    setSetting("locale", "ja"); // 既定ロケールに依存させない
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

    // 空振り防止: ゴールデンが白紙でないこと（fixture の中身が実際に出ていること）を先に見る。
    // 「一致した」だけでは、両側とも何も描けていない場合と区別できない。
    // 🔴 **撮り直しより前に置く。**後ろに置くと、白紙の DOM をそのままゴールデンへ焼けてしまい、
    // 以後この検査は「白紙と白紙が一致する」で永久に緑になる。
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
