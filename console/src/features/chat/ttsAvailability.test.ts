import { describe, it, expect } from "vitest";
import { voicevoxAvailable, pollyAvailable, engineActivity, type TtsStatus } from "./ttsAvailability.ts";

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

// engineActivity is what a member is told the engine is doing, which on-demand made a real
// question: "stopped" is now the normal state, and the answer decides whether they are shown
// a button to call it (ADR 0070 decisions 4 and 16).
describe("engineActivity", () => {
  const managed = (v: Partial<TtsStatus["voicevox"]>): TtsStatus =>
    st({ ready: false, managed: true, mode: "ondemand", enabled: true, ...v });

  it("says nothing before the status arrives", () => {
    expect(engineActivity(null)).toBe("unknown");
  });

  it("is unavailable where no engine was ever provisioned", () => {
    expect(engineActivity(st({ ready: false }))).toBe("unavailable");
  });

  it("is stopped - not unavailable - when a managed engine is scaled to zero", () => {
    // The distinction is the whole point: this one can be called, the one above cannot.
    expect(engineActivity(managed({ state: "stopped" }))).toBe("stopped");
  });

  it("is starting while the task is being placed", () => {
    expect(engineActivity(managed({ state: "starting" }))).toBe("starting");
  });

  // ECS RUNNING only says the container started. /version answers 200 before a voice model is
  // loaded, so a member told "ready" here would be told Zundamon is reading while Polly is.
  it("is warming when the container runs but is not ready", () => {
    expect(engineActivity(managed({ state: "running", ready: false }))).toBe("warming");
  });

  it("is ready only once the CP says ready", () => {
    expect(engineActivity(managed({ state: "running", ready: true }))).toBe("ready");
  });

  // The administrator's intent wins over whatever the engine happens to be doing: while the
  // mode is off, routing goes to Polly and a member must not be offered a button that would
  // buy a task nobody can hear.
  it("is off while the mode is off, even with the engine still up", () => {
    expect(engineActivity(managed({ state: "stopping", ready: true, mode: "off", enabled: false }))).toBe("off");
  });
});
