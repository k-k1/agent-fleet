import { describe, expect, it } from "vitest";
import { echoLanded, echoNeedsResync, ECHO_RESYNC_MS } from "./pendingEcho.ts";

const notNoise = () => false;

describe("echoLanded", () => {
  it("通常の送信は送信後の実ターンで解消する", () => {
    expect(echoLanded({ text: "確認して", sinceIdx: 10 }, [{ role: "user", text: "確認して", idx: 11 }], notNoise)).toBe(true);
  });

  it("managed Codex の画像マーカーは添付パスで解消する", () => {
    const path = "/home/dev/.cache/agent-fleet/pasted/sid/paste-1.png";
    const actual = `確認して <image name=[Image #1] path="${path}">`;
    // app-server が別 response_item に画像を具体化した場合でも、実ターンが既に
    // 見えていれば「反映待ち」を残さない。
    expect(echoLanded({ text: "確認して", sinceIdx: 99, attachmentPaths: [path] }, [{ role: "user", text: actual, idx: 42 }], notNoise)).toBe(true);
  });

  it("別の添付や以前の同文ターンでは解消しない", () => {
    expect(
      echoLanded(
        { text: "確認して", sinceIdx: 99, attachmentPaths: ["/pasted/sid/paste-new.png"] },
        [{ role: "user", text: "確認して <image path=\"/pasted/sid/paste-old.png\">", idx: 42 }],
        notNoise,
      ),
    ).toBe(false);
  });

  // スラッシュコマンドは生の "/model opus" ではなく <command-name>…</command-name> として
  // 記録され isNoise で隠れる。テキスト一致では永久に「反映待ち」→ commandTurnName で解消する。
  const cmdNoise = (t: { text?: string }) => (t.text || "").replace(/^\s+/, "").startsWith("<command-name>");

  it("スラッシュコマンドは <command-name> ターンで解消する", () => {
    expect(
      echoLanded(
        { text: "/model opus", sinceIdx: 10 },
        [{ role: "user", text: "<command-name>/model</command-name><command-args>opus</command-args>", idx: 11 }],
        cmdNoise,
      ),
    ).toBe(true);
  });

  it("スラッシュコマンド行に続く文が付いても command 名一致で解消する", () => {
    // 実バグの再現: "/model opus\n続けて" を送信、実ターンは <command-name> のみ。
    expect(
      echoLanded(
        { text: "/model opus\n続けて", sinceIdx: 10 },
        [{ role: "user", text: "<command-name>/model</command-name><command-args>opus</command-args>", idx: 11 }],
        cmdNoise,
      ),
    ).toBe(true);
  });

  it("スキル形（<command-message> 先頭）の command ターンでも解消する", () => {
    // 実バグの再現（定時 /scout・2.1.215）: スキル起動はタグ順が逆で
    // <command-message> が先頭に来る。name-first 前提だと永久に「反映待ち」。
    expect(
      echoLanded(
        { text: "/scout", sinceIdx: 10 },
        [{ role: "user", text: "<command-message>scout</command-message>\n<command-name>/scout</command-name>", idx: 11 }],
        (t) => (t.text || "").replace(/^\s+/, "").startsWith("<command-message>"),
      ),
    ).toBe(true);
  });

  it("送信より前の command ターンでは解消しない", () => {
    expect(
      echoLanded(
        { text: "/model opus", sinceIdx: 20 },
        [{ role: "user", text: "<command-name>/model</command-name>", idx: 11 }],
        cmdNoise,
      ),
    ).toBe(false);
  });

  it("別コマンドのターンでは解消しない", () => {
    expect(
      echoLanded(
        { text: "/model opus", sinceIdx: 10 },
        [{ role: "user", text: "<command-name>/clear</command-name>", idx: 11 }],
        cmdNoise,
      ),
    ).toBe(false);
  });

  it("先頭が '/' でも実 command ターンが無ければ通常のテキスト一致に委ねる", () => {
    // "/" 始まりの素のテキスト（コマンドではない）は誤って隠さない。
    expect(echoLanded({ text: "/etc/hosts を見て", sinceIdx: 10 }, [], notNoise)).toBe(false);
  });

  // codex managed が turn/start をエラーで拒否した場合（利用上限を使い切った実測）、
  // turn すら作られずユーザー発言も rollout に一切書かれない。合成された error ターン
  // （driver.go managedEnrich）が assistant 側でも解消の対象になる。
  it("送信後の error ターン（assistant）でも解消する（turn/start 拒否で user ターンが無い場合）", () => {
    expect(
      echoLanded(
        { text: "続けて", sinceIdx: 10 },
        [{ role: "assistant", idx: 11, parts: [{ kind: "error" }] }],
        notNoise,
      ),
    ).toBe(true);
  });

  it("送信より前の error ターンでは解消しない", () => {
    expect(
      echoLanded({ text: "続けて", sinceIdx: 20 }, [{ role: "assistant", idx: 11, parts: [{ kind: "error" }] }], notNoise),
    ).toBe(false);
  });

  it("error 以外の kind の assistant ターンでは解消しない", () => {
    expect(
      echoLanded({ text: "続けて", sinceIdx: 10 }, [{ role: "assistant", idx: 11, parts: [{ kind: "text" }] }], notNoise),
    ).toBe(false);
  });
});

describe("echoNeedsResync", () => {
  // 実ターンがクライアントに届かないまま（Agent が書きかけの行を1行と数えて cursor を
  // その行の先へ進めた等）だと、テキスト一致のしようがなく「反映待ち」が永久に残る。
  // 一定時間で1回だけ全体読み直しを促し、穴を埋めてから解消させる。
  const now = 1_000_000;

  it("送信直後は読み直さない", () => {
    expect(echoNeedsResync({ text: "x", sinceIdx: 0, at: now - 1000 }, now)).toBe(false);
  });

  it("一定時間解消しなければ読み直す", () => {
    expect(echoNeedsResync({ text: "x", sinceIdx: 0, at: now - ECHO_RESYNC_MS }, now)).toBe(true);
  });

  it("読み直しは1回だけ（毎ポール読み直さない）", () => {
    expect(echoNeedsResync({ text: "x", sinceIdx: 0, at: now - 60000, resyncedAt: now - 30000 }, now)).toBe(false);
  });

  it("送信時刻を持たない旧エコーは対象外", () => {
    expect(echoNeedsResync({ text: "x", sinceIdx: 0 }, now)).toBe(false);
  });
});

describe("起動時の最初の指示（launchSeed の表示用エコー）", () => {
  // 配達は Agent 側（create の initial_prompt / when_ready）で、ミラーは「送った文面」を
  // 見せるだけ。だからエコーが載る時点で実ターンが既に転写にあることがある（配達が速い、
  // あるいはペインを後から開いた）。アンカーを newestIdx() から取るとその場合に
  // idx > sinceIdx が成立せず、リロードするまで「反映待ち」が残り続ける — 2026-08-20 に
  // 報告された「同じ指示が2つ出て、片方が反映待ちのまま」の正体がこれ（ペインは
  // セルでキーされるので、前のセッションの転写を握ったまま session だけ差し替わり、
  // アンカーが他所のセッションの最終 idx になっていた）。sinceIdx=-1 で必ず解消する。
  it("エコーより前に載っていた実ターンでも解消する", () => {
    expect(echoLanded({ text: "検討して", sinceIdx: -1 }, [{ role: "user", text: "検討して", idx: 7 }], notNoise)).toBe(true);
  });

  it("前のセッション由来のアンカーだと解消できない（回帰の形）", () => {
    expect(echoLanded({ text: "検討して", sinceIdx: 500 }, [{ role: "user", text: "検討して", idx: 7 }], notNoise)).toBe(false);
  });
});
