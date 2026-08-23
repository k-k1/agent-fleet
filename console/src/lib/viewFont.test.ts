// キーボードの文字サイズ操作が「どの設定を動かすか」。ここを間違えると、押しても何も
// 変わらない（別の面の設定を動かしている）という、いちばん気づきにくい壊れ方をする。
import { describe, it, expect } from "vitest";
import type { PaneContent } from "../layout/types.ts";
import { fontSettingFor, stepFontSize, FONT_MIN, FONT_MAX } from "./viewFont.ts";

describe("fontSettingFor", () => {
  it("splits the terminal pane by chat: raw PTY is the terminal, the mirror is a conversation", () => {
    expect(fontSettingFor({ kind: "terminal", chat: false })).toBe("termSize");
    expect(fontSettingFor({ kind: "terminal", chat: true })).toBe("chatSize");
  });

  it("maps the conversation surfaces to chatSize", () => {
    expect(fontSettingFor({ kind: "chat", conversationId: null, draftAssistantId: null })).toBe("chatSize");
    expect(fontSettingFor({ kind: "sharedSession", sharedSessionId: "s1" })).toBe("chatSize");
  });

  it("maps the read-aloud view to its own size", () => {
    expect(fontSettingFor({ kind: "read", filePath: "a.md" })).toBe("readerSize");
  });

  it("maps every read-oriented viewer to viewerSize", () => {
    const viewers: PaneContent[] = [
      { kind: "file", filePath: "main.ts" },
      { kind: "diff", docTitle: "d", diffTool: "t", diffEdits: null },
      { kind: "wtdiff", scmRepo: "r", filePath: "a.ts", diffStaged: false },
      { kind: "scm", scmRepo: "r" },
      { kind: "changes", scmRepo: "r" },
      { kind: "commit", scmRepo: "r", commitSha: "abc" },
      { kind: "doc", docTitle: "plan", docContent: "#" },
    ];
    for (const c of viewers) expect(`${c.kind}:${fontSettingFor(c)}`).toBe(`${c.kind}:viewerSize`);
  });

  it("claims nothing where there is no text to resize (key falls through to the terminal)", () => {
    expect(fontSettingFor({ kind: "file", filePath: "shot.png" })).toBeNull(); // 画像
    expect(fontSettingFor({ kind: "browser", port: 5173, path: "/" })).toBeNull();
    expect(fontSettingFor({ kind: "browserAttach", attachmentId: "a1" })).toBeNull();
    expect(fontSettingFor(null)).toBeNull(); // 空セル
  });

  it("keeps drawio (図 ⇄ ソースを行き来する) on viewerSize", () => {
    expect(fontSettingFor({ kind: "file", filePath: "arch.drawio" })).toBe("viewerSize");
  });
});

describe("stepFontSize", () => {
  it("steps by one and stops at the same bounds as the settings stepper", () => {
    expect(stepFontSize(13, 1)).toBe(14);
    expect(stepFontSize(13, -1)).toBe(12);
    expect(stepFontSize(FONT_MIN, -1)).toBe(FONT_MIN);
    expect(stepFontSize(FONT_MAX, 1)).toBe(FONT_MAX);
  });
});
