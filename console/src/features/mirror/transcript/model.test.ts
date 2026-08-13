import { describe, expect, it } from "vitest";
import {
  coalesceUserActions,
  foldParts,
  groupTurns,
  isNoise,
  latestContext,
  parseCommand,
  peerSenderOf,
} from "./model.ts";
import type { Turn } from "./types.ts";

// このパイプラインはミラーと共有セッションビューの両方が通る唯一の描画経路なので、
// ここが崩れると「所有者と受信者で会話の見え方が違う」という形で表面化する。
// MirrorView.tsx に埋もれていた頃はテストが1本も無かった。

const user = (text: string, extra: Partial<Turn> = {}): Turn => ({ role: "user", text, ...extra });
const asst = (text: string, extra: Partial<Turn> = {}): Turn => ({ role: "assistant", text, ...extra });

describe("isNoise", () => {
  it("システムが差し込んだ user 行を落とし、本物の発話は残す", () => {
    expect(isNoise(user("<system-reminder>foo"))).toBe(true);
    expect(isNoise(user("  <bash-input>ls</bash-input>"))).toBe(true); // 先頭空白は無視
    expect(isNoise(user("[Request interrupted by user for tool use]"))).toBe(true);
    expect(isNoise(user("これは普通の指示です"))).toBe(false);
  });

  it("assistant 側は何であれノイズ扱いしない", () => {
    expect(isNoise(asst("<system-reminder>これは本文の一部"))).toBe(false);
  });
});

describe("coalesceUserActions", () => {
  it("`!`実行を bash ブロックにし、対になる出力ターンを吸収する", () => {
    const out = coalesceUserActions([
      user("<bash-input>ls -la</bash-input>"),
      user("<bash-stdout>a.txt\nb.txt</bash-stdout><bash-stderr>warn</bash-stderr>"),
      asst("見ました"),
    ]);
    expect(out).toHaveLength(2); // 出力ターンは消費されている
    expect(out[0].bash).toBe(true);
    expect(out[0].text).toBe("$ ls -la"); // コピー用の平文
    expect(out[0].parts).toEqual([{ kind: "bash", text: "ls -la", output: "a.txt\nb.txt", stderr: "warn" }]);
  });

  it("出力ターンが無い `!`実行も bash ブロックとして残す", () => {
    const out = coalesceUserActions([user("<bash-input>pwd</bash-input>")]);
    expect(out).toHaveLength(1);
    expect(out[0].parts?.[0]).toEqual({ kind: "bash", text: "pwd", output: "", stderr: "" });
  });

  it("`/`実行を cmd チップにする", () => {
    const out = coalesceUserActions([user("<command-name>/scout</command-name><command-args>--deep</command-args>")]);
    expect(out[0].cmd).toBe(true);
    expect(out[0].parts).toEqual([{ kind: "cmd", text: "/scout", info: "--deep" }]);
  });

  it("スキル起動のようにタグ順が逆でも名前を拾う（順序を固定しない）", () => {
    // 名前が先頭に来ることを要求していた頃、この形は解析できずノイズとして丸ごと
    // 消え、user ターンの境目が失われて以降の返信が1ブロックに融合していた。
    const t = user("<command-message>scout is running…</command-message><command-name>/scout</command-name>");
    expect(parseCommand(t)).toEqual({ name: "/scout", args: "" });
    expect(coalesceUserActions([t])[0].cmd).toBe(true);
  });
});

describe("groupTurns", () => {
  it("同じ role の連続ターンを1ブロックに畳み、本文とトークンをまとめる", () => {
    const groups = groupTurns([
      asst("前半", { ts: "2026-08-13T10:00:00Z", outTok: 10, idx: 1 }),
      asst("後半", { ts: "2026-08-13T10:05:00Z", outTok: 5, inTok: 100, cacheRead: 20 }),
    ]);
    expect(groups).toHaveLength(1);
    expect(groups[0].text).toBe("前半\n\n後半");
    expect(groups[0].outTok).toBe(15); // 出力は合算
    expect(groups[0].inTok).toBe(100); // 入力/キャッシュは最後の値
    expect(groups[0].ts).toBe("2026-08-13T10:00:00Z"); // 並び順は開始時刻
    expect(groups[0].endTs).toBe("2026-08-13T10:05:00Z"); // フッタは終了時刻
  });

  it("role が変われば別ブロックになる", () => {
    expect(groupTurns([user("お願い"), asst("はい"), user("もう一度")])).toHaveLength(3);
  });

  it("compact/bash/cmd は単独ブロックで、隣とマージしない", () => {
    for (const flag of ["compact", "bash", "cmd"] as const) {
      const groups = groupTurns([
        asst("前", { parts: [{ kind: "text", text: "前" }] }),
        asst("特殊", { [flag]: true, parts: [{ kind: "text", text: "特殊" }] }),
        asst("後", { parts: [{ kind: "text", text: "後" }] }),
      ]);
      expect(groups.map((g) => g.text)).toEqual(["前", "特殊", "後"]);
    }
  });

  it("ノイズと sidechain は落とす（委譲はカードで表現するため）", () => {
    const groups = groupTurns([user("<system-reminder>x"), asst("子の作業", { sidechain: true }), asst("答え")]);
    expect(groups).toHaveLength(1);
    expect(groups[0].text).toBe("答え");
  });

  it("parts を持たない旧 Agent のターンは text から1つ合成する", () => {
    expect(groupTurns([asst("本文だけ")])[0].parts).toEqual([{ kind: "text", text: "本文だけ" }]);
  });

  it("operator 由来などの source は同 role マージを跨いで残る", () => {
    const groups = groupTurns([user("一言目"), user("二言目", { source: "operator" })]);
    expect(groups[0].source).toBe("operator");
  });
});

describe("foldParts", () => {
  it("連続するツールを1つの toolrun にまとめ、他は素通しする", () => {
    const items = foldParts([
      { kind: "text", text: "始めます" },
      { kind: "tool", tool: "Edit" },
      { kind: "tool", tool: "Edit" },
      { kind: "text", text: "できました" },
      { kind: "tool", tool: "Bash" },
    ]);
    expect(items.map((i) => i.kind)).toEqual(["part", "toolrun", "part", "toolrun"]);
    expect(items[1].kind === "toolrun" && items[1].tools).toHaveLength(2);
    // 単独のツールも toolrun になる（呼び出し側の分岐を2通りに保つため）
    expect(items[3].kind === "toolrun" && items[3].tools).toHaveLength(1);
  });

  it("元の並び順を index で保持する", () => {
    const items = foldParts([{ kind: "tool" }, { kind: "text", text: "x" }]);
    expect(items[0].kind === "toolrun" && items[0].tools[0].i).toBe(0);
    expect(items[1].kind === "part" && items[1].i).toBe(1);
  });
});

describe("latestContext", () => {
  it("使用量を持つ最新の assistant ブロックを返す", () => {
    const groups = groupTurns([
      asst("古い", { inTok: 10, cacheRead: 1, ctxWindow: 200000, model: "old" }),
      user("次"),
      asst("新しい", { inTok: 50, cacheRead: 5, cacheCreate: 2, ctxWindow: 200000, model: "new" }),
    ]);
    expect(latestContext(groups)).toMatchObject({ fresh: 50, read: 5, create: 2, model: "new" });
  });

  it("使用量の記録が無ければ null", () => {
    expect(latestContext(groupTurns([asst("記録なし")]))).toBeNull();
  });
});

describe("peerSenderOf", () => {
  it("peer 封筒から送信元セッション名を読む", () => {
    expect(peerSenderOf("[agent-fleet:peer from=build-api] お願い")).toBe("build-api");
    expect(peerSenderOf("ふつうの発話")).toBeNull();
  });
});
