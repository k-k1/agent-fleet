import { describe, expect, it } from "vitest";
import { COOLDOWN_MS, INPUT_FRESH_MS, armedNext, shouldRequestSigninStart, type SigninGate } from "./signinGate.ts";

const NOW = 10_000_000;
const open: SigninGate = {
  state: "stopped",
  armed: true,
  visible: true,
  now: NOW,
  lastInputAt: NOW - 1000,
  lastRequestAt: 0,
  requestedThisLoad: false,
};

describe("shouldRequestSigninStart", () => {
  it("asks for a visible tab with recent input on a stopped workspace", () => {
    expect(shouldRequestSigninStart(open)).toBe(true);
  });

  it("never asks from a hidden tab", () => {
    expect(shouldRequestSigninStart({ ...open, visible: false })).toBe(false);
  });

  it("never asks without input, or when the last input is stale", () => {
    expect(shouldRequestSigninStart({ ...open, lastInputAt: 0 })).toBe(false);
    expect(shouldRequestSigninStart({ ...open, lastInputAt: NOW - INPUT_FRESH_MS - 1 })).toBe(false);
  });

  it("never asks when the state is anything but stopped, unknown above all", () => {
    for (const state of ["unknown", "…", "running", "starting", "starting…", "stopping…", "none"]) {
      expect(shouldRequestSigninStart({ ...open, state })).toBe(false);
    }
  });

  it("asks once per page load, and once per cooldown across reloads", () => {
    expect(shouldRequestSigninStart({ ...open, requestedThisLoad: true })).toBe(false);
    expect(shouldRequestSigninStart({ ...open, lastRequestAt: NOW - COOLDOWN_MS + 1 })).toBe(false);
    expect(shouldRequestSigninStart({ ...open, lastRequestAt: NOW - COOLDOWN_MS })).toBe(true);
  });

  it("does not ask when the page is not armed", () => {
    expect(shouldRequestSigninStart({ ...open, armed: false })).toBe(false);
    expect(shouldRequestSigninStart({ ...open, armed: null })).toBe(false);
  });
});

describe("armedNext", () => {
  it("arms on a workspace first seen stopped", () => {
    expect(armedNext(null, "…")).toBeNull();
    expect(armedNext(null, "unknown")).toBeNull();
    expect(armedNext(null, "stopped")).toBe(true);
  });

  it("is disarmed for good by a workspace first seen running", () => {
    expect(armedNext(null, "running")).toBe(false);
    expect(armedNext(false, "stopped")).toBe(false);
  });

  it("is disarmed by any later stop: the idle auto-stop or the member's own", () => {
    // running → stopped while the tab is open must not restart it (the stop/start loop).
    let a = armedNext(null, "running");
    a = armedNext(a, "stopping…");
    a = armedNext(a, "stopped");
    expect(a).toBe(false);
  });

  it("is disarmed by a start the member began and that then failed", () => {
    let a = armedNext(null, "stopped");
    a = armedNext(a, "starting…");
    a = armedNext(a, "stopped");
    expect(a).toBe(false);
  });

  it("is disarmed by an outage seen after arming", () => {
    let a = armedNext(null, "stopped");
    a = armedNext(a, "unknown");
    expect(a).toBe(false);
  });
});
