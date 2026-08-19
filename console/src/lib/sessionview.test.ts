import { describe, expect, it } from "vitest";
import { resumeClock, stateInfo } from "./sessionview.ts";
import { t } from "./i18n/index.ts";

// 状態チップの写像。ここで固定したいのは「認証切れ（docs/47 §4-8）が独立したチップに
// なること」— 入力待ち に見せると送っても動かないセッションを利用者が延々と叩き、
// 上限で停止 に混ぜると「待てば直る」と読めてしまう（認証切れは待っても直らない）。
describe("stateInfo", () => {
  const claude = { kind: "claude", alive: true };

  it("認証切れは idle とも blocked とも別のチップになる", () => {
    const auth = stateInfo({ ...claude, state: "auth" });
    expect(auth.text).toBe(t("state.auth_expired"));
    expect(auth.text).not.toBe(t("state.idle"));
    expect(auth.text).not.toBe(t("state.blocked"));
    // 注意を引く色（question 系）。on（緑＝正常）ではない。
    expect(auth.cls).toBe("question");
  });

  it("既存の状態は変わらない", () => {
    expect(stateInfo({ ...claude, state: "idle" }).text).toBe(t("state.idle"));
    expect(stateInfo({ ...claude, state: "blocked" }).text).toBe(t("state.blocked"));
    expect(stateInfo({ ...claude, state: "working" }).text).toBe(t("state.working"));
  });

  it("停止中のセッションは state に関係なく 停止中", () => {
    expect(stateInfo({ kind: "claude", alive: false, state: "auth" }).text).toBe(t("state.stopped"));
  });

  // 利用上限のリセット待ち（docs/47 §4-9）。これが 入力待ち に見えていたのが元の苦情で、
  // 「なぜ止まっているのか」「いつ動くのか」の両方が画面から消えていた。
  it("上限のリセット待ちは 入力待ち と別のチップで、予約時刻を出す", () => {
    const bare = stateInfo({ ...claude, state: "limited" });
    expect(bare.text).toBe(t("state.rate_limited"));
    expect(bare.text).not.toBe(t("state.idle"));
    // 対応が要る blocked（＝ペインで選ぶまで動かない）とも別物。
    expect(bare.text).not.toBe(t("state.blocked"));

    const at = new Date();
    at.setHours(at.getHours() + 1, 30, 0, 0);
    const timed = stateInfo({ ...claude, state: "limited", rateLimitResumeAt: at.toISOString() });
    expect(timed.text).toContain(resumeClock(at.toISOString()));
  });

  // 予約が無い上限（自動再開 OFF・モデル別上限）でも、待ちであることは言える。
  it("再開時刻が無い／読めないときは時刻なしのチップに落ちる", () => {
    expect(stateInfo({ ...claude, state: "limited", rateLimitResumeAt: "" }).text).toBe(t("state.rate_limited"));
    expect(stateInfo({ ...claude, state: "limited", rateLimitResumeAt: "not-a-time" }).text).toBe(
      t("state.rate_limited"),
    );
  });

  // 未回答の質問と同じ強さで並ばせない（限定クラスで太字を戻す）。色は question 系のまま。
  it("リセット待ちは question 色を借りつつ limited クラスを足す", () => {
    const cls = stateInfo({ ...claude, state: "limited" }).cls;
    expect(cls).toContain("question");
    expect(cls).toContain("limited");
  });
});

// 日時はすべてローカル時刻のコンストラクタで組む（固定オフセットの文字列にすると、
// 表示は実行環境の TZ で行われるので JST 以外の runner で落ちる）。
describe("resumeClock", () => {
  const now = new Date(2026, 7, 19, 21, 0, 0);

  it("同じ日なら時刻だけ", () => {
    expect(resumeClock(new Date(2026, 7, 19, 23, 50, 0).toISOString(), now)).toBe("23:50");
  });

  // 週次の窓は数日先に落ちうる。時刻だけだと「あと数分」に読めてしまう。
  it("別の日なら日付を足す", () => {
    expect(resumeClock(new Date(2026, 7, 21, 7, 15, 0).toISOString(), now)).toBe("08/21 07:15");
  });

  it("空／壊れた値は空文字", () => {
    expect(resumeClock(undefined, now)).toBe("");
    expect(resumeClock("", now)).toBe("");
    expect(resumeClock("nonsense", now)).toBe("");
  });
});
