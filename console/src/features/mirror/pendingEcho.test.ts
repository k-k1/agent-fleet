import { describe, expect, it } from "vitest";
import { echoLanded } from "./pendingEcho.ts";

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
});
