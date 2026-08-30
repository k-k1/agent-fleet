import { describe, expect, it } from "vitest";
import { remainingShort, resumeClock, stateInfo } from "./sessionview.ts";
import { t } from "./i18n/index.ts";

// 状態チップの写像。ここで固定したいのは「認証切れ（docs/log/47 §4-8）が独立したチップに
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

  // 利用上限のリセット待ち（docs/log/47 §4-9）。これが 入力待ち に見えていたのが元の苦情で、
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

// 支出・残高の上限（docs/log/47 §4-10）。同じ 429 で届くので、ここを取り違えると利用者は
// 来ないリセットを待ち続ける。
describe("stateInfo（残高・支出の上限）", () => {
  const claude = { kind: "claude", alive: true };

  it("制限解除待ち とも 入力待ち とも別のチップになる", () => {
    const spend = stateInfo({ ...claude, state: "spend_limit" });
    expect(spend.text).toBe(t("state.spend_limit"));
    expect(spend.text).not.toBe(t("state.rate_limited"));
    expect(spend.text).not.toBe(t("state.idle"));
    // 人が今やる側（増枠）なので、limited の落ち着いた見え方ではなく質問系の注意色。
    expect(spend.cls).toBe("question");
  });

  it("再開時刻は出さない（予約は存在しない）", () => {
    const at = new Date(Date.now() + 3600_000).toISOString();
    expect(stateInfo({ ...claude, state: "spend_limit", rateLimitResumeAt: at }).text).toBe(t("state.spend_limit"));
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

// 停止中でも「答えを待っている対話がある」ことは出す（docs/log/75 §75.6.5）。人待ちも
// 畳めるようにした以上、停止中 の 1 語に丸めると未回答の質問が静かに消える。
describe("stateInfo（停止中の持ち越し）", () => {
  const dead = { kind: "claude", alive: false };

  it("持ち越しの種類ごとにバッジが変わる", () => {
    expect(stateInfo({ ...dead, carried: "question" }).text).toBe(t("state.stopped_question"));
    expect(stateInfo({ ...dead, carried: "plan" }).text).toBe(t("state.stopped_plan"));
    expect(stateInfo({ ...dead, carried: "permission" }).text).toBe(t("state.stopped_permission"));
  });

  it("停止中であることは崩さない（off を保ったまま注意色を足す）", () => {
    const chip = stateInfo({ ...dead, carried: "question" });
    expect(chip.cls).toContain("off");
    expect(chip.cls).toContain("question");
  });

  it("持ち越しが無ければ従来どおり 停止中", () => {
    expect(stateInfo(dead).text).toBe(t("state.stopped"));
    expect(stateInfo({ ...dead, carried: "" }).text).toBe(t("state.stopped"));
  });

  // 異常終了は「なぜ死んだか」の方が先。持ち越しでその警告を隠さない。
  it("クラッシュ表示は持ち越しより優先する", () => {
    expect(stateInfo({ ...dead, carried: "question", exitReason: "oom" }).text).toBe(t("exit.oom.text"));
  });

  // 稼働中の行は state が今出ているモーダルを語る。二重に見せない。
  it("生きている行には持ち越しを出さない", () => {
    expect(stateInfo({ kind: "claude", alive: true, state: "idle", carried: "question" }).text).toBe(t("state.idle"));
  });
});

// 停止しないピンの残り時間（docs/log/75）。**切れたピンをバッジに残さない**のが要点 —
// 残すと利用者は「守られているつもり」で放置し、実際には次のスイープで畳まれる。
describe("remainingShort", () => {
  const now = new Date(2026, 7, 24, 12, 0, 0);

  it("残りを人が読める形にする", () => {
    expect(remainingShort(new Date(2026, 7, 24, 12, 30, 0).toISOString(), now)).toBe("30m");
    expect(remainingShort(new Date(2026, 7, 24, 16, 0, 0).toISOString(), now)).toBe("4h");
    expect(remainingShort(new Date(2026, 7, 24, 14, 15, 0).toISOString(), now)).toBe("2h15m");
  });

  it("切れている・掛かっていない・壊れている は空", () => {
    expect(remainingShort(new Date(2026, 7, 24, 11, 59, 0).toISOString(), now)).toBe("");
    expect(remainingShort(undefined, now)).toBe("");
    expect(remainingShort("", now)).toBe("");
    expect(remainingShort("いつまでも", now)).toBe("");
  });
});
