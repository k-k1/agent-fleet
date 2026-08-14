import { describe, expect, it } from "vitest";
import { stateInfo } from "./sessionview.ts";
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
});
