import { describe, expect, it } from "vitest";
import { composerHistory } from "./composerHistory.ts";
import type { ChatMessage } from "../../types/chat.ts";

const msg = (role: ChatMessage["role"], content: string, extra: Partial<ChatMessage> = {}): ChatMessage => ({
  role,
  content,
  ts: 0,
  ...extra,
});

describe("composerHistory", () => {
  it("keeps only what the member typed in the composer, oldest first, repeats folded", () => {
    expect(
      composerHistory([
        msg("user", "状況は?"),
        msg("assistant", "2 件稼働中です"),
        msg("user", "Discord からの問い", { source: "discord" }),
        msg("user", "Slack からの問い", { source: "slack" }),
        msg("user", "朝の点検", { source: "schedule" }),
        msg("user", "セッション「sabc」の会話を引き継いで…", { source: "handoff" }),
        msg("report", "sabc が完了しました", { session: "sabc" }),
        msg("notice", "自動ターンの上限に達しました"),
        msg("user", "  次は?  "),
        msg("user", "次は?"),
      ]),
    ).toEqual(["状況は?", "次は?"]);
  });
});
