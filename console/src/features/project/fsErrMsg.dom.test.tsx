import { describe, expect, it } from "vitest";
import { fsErrMsg } from "./fsErrMsg.ts";
import { t } from "../../lib/i18n/index.ts";

describe("fsErrMsg", () => {
  it("words a protected space in the catalogue, not the server's English", () => {
    const msg = fsErrMsg({ code: "space_protected", message: "server sentence" });
    expect(msg).toBe(t("err.space_protected"));
    expect(msg).not.toBe("server sentence");
  });

  it("keeps the server's message for every other refusal", () => {
    expect(fsErrMsg({ code: "denied", message: "this path is protected and cannot be changed" })).toBe(
      "this path is protected and cannot be changed",
    );
    expect(fsErrMsg("boom")).toBe("boom");
  });
});
