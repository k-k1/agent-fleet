import { describe, it, expect } from "vitest";
import { voicevoxAvailable, pollyAvailable, type TtsStatus } from "./ttsAvailability.ts";

const st = (voicevox: TtsStatus["voicevox"]): TtsStatus => ({ voicevox, polly: { ready: true } });

describe("voicevoxAvailable", () => {
  it("does not decide before the status arrives (null)", () => {
    // Concluding "absent" before the fetch, or on a failed one, makes the setting vanish for a
    // moment and then come back.
    expect(voicevoxAvailable(null)).toBe(null);
  });

  it("present when the engine is reachable", () => {
    expect(voicevoxAvailable(st({ ready: true, enabled: true }))).toBe(true);
  });

  it("present under ECS management even while stopped", () => {
    // An admin can start it from the toggle, so Zundamon does exist on this deployment.
    expect(voicevoxAvailable(st({ ready: false, enabled: false, managed: true, state: "stopped" }))).toBe(true);
  });

  it("absent when unmanaged and unreachable", () => {
    // This is the ECS default (AF_TTS_ECS_SERVICE unset); on auto even Japanese falls back to Polly.
    expect(voicevoxAvailable(st({ ready: false, enabled: true }))).toBe(false);
  });

  it("does not report absent just because the admin toggle is off", () => {
    // enabled means "not right now", not "does not exist", so the setting stays visible.
    expect(voicevoxAvailable(st({ ready: true, enabled: false }))).toBe(true);
  });
});

describe("pollyAvailable", () => {
  const withPolly = (ready: boolean): TtsStatus => ({ voicevox: { ready: true }, polly: { ready } });

  it("does not decide before the status arrives (null)", () => {
    expect(pollyAvailable(null)).toBe(null);
  });

  it("present when the CP has a region configured", () => {
    expect(pollyAvailable(withPolly(true))).toBe(true);
  });

  // On a deployment without Polly, choosing English still falls back to voicevox in the CP's
  // chooseTTSProvider. The UI reads this flag to drop Polly from the engine choices and switch the
  // reading-language note; without it the note promises a Polly voice while Zundamon speaks.
  it("absent when not configured - unlike voicevox there is no managed notion", () => {
    expect(pollyAvailable(withPolly(false))).toBe(false);
  });
});
