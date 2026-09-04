// セッション報告の行き先は**会話**（docs/log/30）で、通知は Control Plane に 7 日残るのに
// 会話は消えうる —— 利用者が消したときも、テストが実 HOME へ幽霊通知を落としたときも
// （2026-09-04 に実際に起きた: workspace/agent/internal/chatx/chat_report.go の
// interimDeliveries）。消えていたら報告元セッションへ落として理由を言う、が新しい約束。
//
// ★ 芯は「**取れなかった＝消えた ではない**」。WS 起動直後の 5xx や通信断まで「消えた」と
// 読むと、起動待ちの一瞬に押した人が毎回セッションへ飛ばされる（会話は生きているのに）。
//
// dom プロジェクトに置くのは store.ts が core/api/client.ts を引き、そこが読み込み時に
// localStorage を触るから（node 環境では import すら通らない）。
import { beforeEach, describe, expect, it, vi } from "vitest";
import { conversationReachable, openNotificationTarget, type FleetNotification } from "./store.ts";
import { chatGet } from "../../core/api/client.ts";
import { openChat } from "../chat/open.ts";
import { openSessionChat } from "../sessions/open.ts";
import { useSessionsStore } from "../sessions/store.ts";

vi.mock("../../core/api/client.ts", async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  chatGet: vi.fn(),
}));
vi.mock("../chat/open.ts", () => ({ openChat: vi.fn(), openChatSplit: vi.fn(), openAssistantDraft: vi.fn() }));
vi.mock("../sessions/open.ts", () => ({
  openSessionChat: vi.fn(), openSessionChatSplit: vi.fn(),
  openSessionTerminal: vi.fn(), openSessionTerminalSplit: vi.fn(),
}));

describe("報告先の会話が生きているかの判定", () => {
  it("会話が取れたら開く", () => {
    expect(conversationReachable({ id: "c1", messages: [] })).toBe(true);
  });

  it("一時障害（WS 起動中の 5xx・通信断）は「消えた」と読まない", () => {
    expect(conversationReachable({ error: { code: "http_502" } })).toBe(true);
    expect(conversationReachable({ error: { code: "internal", status: 500 } })).toBe(true);
    expect(conversationReachable(null)).toBe(true); // fetch が throw した
  });

  it("404（chat_conversation_not_found）だけが「消えた」", () => {
    expect(conversationReachable({ error: { code: "chat_conversation_not_found", status: 404 } })).toBe(false);
  });
});

describe("セッション報告のクリック先", () => {
  const report = (): FleetNotification => ({
    seq: 1, id: "e1", kind: "session-report",
    target: { type: "session", id: "s1" },
    displayName: "プラン検証",
    payload: { conversation_id: "c1", conversationTitle: "運用オペレーター" },
    createdAt: "2026-09-04T00:43:00Z", seen: false,
  });

  beforeEach(() => {
    vi.clearAllMocks();
    useSessionsStore.setState({ sessions: [{ name: "s1", kind: "claude", alive: true }] });
  });

  it("会話が生きていれば会話を開く（従来どおり）", async () => {
    vi.mocked(chatGet).mockResolvedValue({ id: "c1" } as never);
    expect(await openNotificationTarget(report(), false)).toEqual({ opened: true });
    expect(openChat).toHaveBeenCalledWith("c1");
    expect(openSessionChat).not.toHaveBeenCalled();
  });

  it("会話が消えていたら報告元セッションへ落とし、会話名を返す", async () => {
    vi.mocked(chatGet).mockResolvedValue({ error: { code: "chat_conversation_not_found", status: 404 } } as never);
    expect(await openNotificationTarget(report(), false)).toEqual({ opened: true, missingConversation: "運用オペレーター" });
    // ★ 行き止まりにしない: 「会話が見つかりません」の赤字ペインを開かない。
    expect(openChat).not.toHaveBeenCalled();
    expect(openSessionChat).toHaveBeenCalledWith("s1");
  });

  it("会話もセッションも無ければ、会話が消えたことだけは伝える", async () => {
    useSessionsStore.setState({ sessions: [] });
    vi.mocked(chatGet).mockResolvedValue({ error: { code: "chat_conversation_not_found", status: 404 } } as never);
    const r = await openNotificationTarget(report(), false);
    expect(r.opened).toBe(false);
    expect(r.missingConversation).toBe("運用オペレーター");
  });

  it("WS 起動中の一時失敗では会話を開く（セッションへ飛ばさない）", async () => {
    vi.mocked(chatGet).mockResolvedValue({ error: { code: "http_502" } } as never);
    expect(await openNotificationTarget(report(), false)).toEqual({ opened: true });
    expect(openChat).toHaveBeenCalledWith("c1");
  });
});
