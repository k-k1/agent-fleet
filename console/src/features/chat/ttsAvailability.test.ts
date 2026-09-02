import { describe, it, expect } from "vitest";
import { voicevoxAvailable, pollyAvailable, type TtsStatus } from "./ttsAvailability.ts";

const st = (voicevox: TtsStatus["voicevox"]): TtsStatus => ({ voicevox, polly: { ready: true } });

describe("voicevoxAvailable", () => {
  it("未取得は判断しない（null）", () => {
    // 取得前・取得失敗で「無い」と決めつけると、一瞬だけ設定が消えてから戻る。
    expect(voicevoxAvailable(null)).toBe(null);
  });

  it("到達できるなら在る", () => {
    expect(voicevoxAvailable(st({ ready: true, enabled: true }))).toBe(true);
  });

  it("ECS 管理下なら、いま停止していても在る", () => {
    // 管理者がトグルで起動できる＝このデプロイにずんだもんは存在する。
    expect(voicevoxAvailable(st({ ready: false, enabled: false, managed: true, state: "stopped" }))).toBe(true);
  });

  it("管理外で到達できないなら無い", () => {
    // ECS の既定構成（AF_TTS_ECS_SERVICE 未設定）がこれ。auto は日本語も Polly へ落ちる。
    expect(voicevoxAvailable(st({ ready: false, enabled: true }))).toBe(false);
  });

  it("管理者トグルが off でも「無い」とは言わない", () => {
    // enabled は「今は使わない」であって「存在しない」ではない（設定は隠さない）。
    expect(voicevoxAvailable(st({ ready: true, enabled: false }))).toBe(true);
  });
});

describe("pollyAvailable", () => {
  const withPolly = (ready: boolean): TtsStatus => ({ voicevox: { ready: true }, polly: { ready } });

  it("未取得は判断しない（null）", () => {
    expect(pollyAvailable(null)).toBe(null);
  });

  it("CP にリージョン設定があれば在る", () => {
    expect(pollyAvailable(withPolly(true))).toBe(true);
  });

  // ★ 無い配備で English を選んでも、CP の chooseTTSProvider が voicevox へ落とす。
  // UI 側はこれを見て、選択肢から Polly を外し、読み上げ言語の注記も切り替える
  // （見ないと「Polly の声で読む」と書いてあるのにずんだもんが鳴る）。
  it("無ければ無い（voicevox と違い managed の概念が無い）", () => {
    expect(pollyAvailable(withPolly(false))).toBe(false);
  });
});
