import { beforeEach, describe, expect, it, vi } from "vitest";

const raw = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  raw: (...a: unknown[]) => raw(...a),
  errDetail: (e: { message?: string }) => e.message ?? "",
}));

import { exportSessionMarkdown } from "./exportMarkdown.ts";
import { t } from "../../lib/i18n/index.ts";
import { setFormatPrefs } from "../../lib/intl.ts";

describe("exportSessionMarkdown", () => {
  beforeEach(() => {
    raw.mockReset();
    setFormatPrefs("auto", "auto");
  });

  it("reports a body that fails after a 200 instead of dropping it", async () => {
    raw.mockResolvedValue({
      ok: true,
      headers: new Headers(),
      blob: () => Promise.reject(new TypeError("network")),
    });
    const notify = vi.fn();
    await exportSessionMarkdown("s1", "names", notify);
    expect(notify).toHaveBeenCalledTimes(1);
    expect(notify.mock.calls[0][0]).toBe(t("srow.export_failed"));
  });

  it("shows the Agent's error and asks for the browser's zone", async () => {
    raw.mockResolvedValue({
      ok: false,
      status: 400,
      json: () => Promise.resolve({ error: { code: "unsupported_kind", message: "nope" } }),
    });
    const notify = vi.fn();
    await exportSessionMarkdown("s1", "full", notify);
    expect(String(raw.mock.calls[0][0])).toMatch(/^api\/sessions\/s1\/export\?tools=full&tz=/);
    expect(notify.mock.calls[0][0]).toBe(t("srow.export_failed") + ": nope");
  });

  it("sends no ?tz= when a zone is chosen: the Agent reads the setting from ui-prefs", async () => {
    raw.mockResolvedValue({ ok: false, status: 400, json: () => Promise.resolve({}) });
    setFormatPrefs("auto", "Asia/Tokyo");
    await exportSessionMarkdown("s1", "names", vi.fn());
    expect(String(raw.mock.calls[0][0])).toBe("api/sessions/s1/export?tools=names");
  });

  it("falls back to ?tz= with the browser's zone for an unknown stored zone", async () => {
    raw.mockResolvedValue({ ok: false, status: 400, json: () => Promise.resolve({}) });
    setFormatPrefs("auto", "Mars/Olympus_Mons");
    await exportSessionMarkdown("s1", "names", vi.fn());
    expect(String(raw.mock.calls[0][0])).toMatch(/^api\/sessions\/s1\/export\?tools=names&tz=.+/);
  });

  it("asks for the tool-free variant", async () => {
    raw.mockResolvedValue({ ok: false, status: 400, json: () => Promise.resolve({}) });
    await exportSessionMarkdown("s1", "none", vi.fn());
    expect(String(raw.mock.calls[0][0])).toMatch(/\?tools=none&tz=/);
  });

  // An Agent older than the route answers 404 in plain text.
  it("names the status when the error body is not JSON", async () => {
    raw.mockResolvedValue({ ok: false, status: 404, json: () => Promise.reject(new SyntaxError("not json")) });
    const notify = vi.fn();
    await exportSessionMarkdown("s1", "names", notify);
    expect(notify.mock.calls[0][0]).toBe(t("srow.export_failed") + ": HTTP 404");
  });
});
