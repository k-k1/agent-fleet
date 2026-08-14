import { describe, it, expect, beforeEach } from "vitest";
import {
  signalProviderRequired,
  subscribeProviderRequired,
  clearProviderRequired,
  providerRequiredState,
} from "./providerRequired.ts";

// docs/61 §61.9.4. Unlike the auth-expiry latch this one must be RE-ARMABLE for a
// different tenant — switching to another department after dismissing the dialog
// is a normal thing to do, and the second refusal has to raise the dialog again.
describe("providerRequired", () => {
  beforeEach(() => clearProviderRequired());

  it("fires once per tenant, not once per failing request", () => {
    const seen: string[] = [];
    const off = subscribeProviderRequired((p) => seen.push(p.tenant));
    signalProviderRequired({ tenant: "sales", provider: "" });
    signalProviderRequired({ tenant: "sales", provider: "" }); // the flood behind one refusal
    signalProviderRequired({ tenant: "sales", provider: "" });
    expect(seen).toEqual(["sales"]);
    off();
  });

  it("re-arms for a different tenant", () => {
    const seen: string[] = [];
    const off = subscribeProviderRequired((p) => seen.push(p.tenant));
    signalProviderRequired({ tenant: "sales", provider: "" });
    signalProviderRequired({ tenant: "dev", provider: "entra" });
    expect(seen).toEqual(["sales", "dev"]);
    off();
  });

  it("re-fires for the same tenant after being cleared (the dialog was dismissed)", () => {
    const seen: string[] = [];
    const off = subscribeProviderRequired((p) => seen.push(p.tenant));
    signalProviderRequired({ tenant: "sales", provider: "" });
    clearProviderRequired();
    expect(providerRequiredState()).toBeNull();
    signalProviderRequired({ tenant: "sales", provider: "" });
    expect(seen).toEqual(["sales", "sales"]);
    off();
  });

  it("delivers a pending refusal to a listener that subscribes late", () => {
    signalProviderRequired({ tenant: "sales", provider: "entra" });
    let got = "";
    const off = subscribeProviderRequired((p) => {
      got = p.tenant + ":" + p.provider;
    });
    expect(got).toBe("sales:entra");
    off();
  });
});
