// The AWS / Google Cloud chips under the WS bar's width fold (wsBarFold.ts, #1651 review).
// On a full desktop bar the chips stay mounted but hidden by CSS, and the ⋯ popover draws
// copies of them:
//   - a hidden chip must close its popover, or the popover's dismiss layer outlives it and
//     eats the next click anywhere on the page;
//   - a copy must be passive, or every ask, listener and expiry timer runs twice.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { AwsProfilesChip } from "../features/awslogin/AwsProfilesChip.tsx";
import { GcpProfilesChip } from "../features/gcplogin/GcpProfilesChip.tsx";
import { useAwsLoginStore } from "../features/awslogin/store.ts";
import { useGcpLoginStore } from "../features/gcplogin/store.ts";
import { useWorkspaceStore } from "../core/store/workspace.ts";
import { ConfirmProvider } from "../ui/ConfirmProvider.tsx";
import { ToastProvider } from "../ui/ToastProvider.tsx";
import type { ReactNode } from "react";

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
let root: Root | null = null;
let host: HTMLDivElement | null = null;
const awsRefresh = vi.fn(async () => null);
const gcpRefresh = vi.fn(async () => {});
const gcpRequests = vi.fn(async () => {});

beforeEach(() => {
  g.IS_REACT_ACT_ENVIRONMENT = true;
  awsRefresh.mockClear();
  gcpRefresh.mockClear();
  gcpRequests.mockClear();
  useWorkspaceStore.setState({ state: "running" });
  useAwsLoginStore.setState({
    profiles: [{ name: "dev", label: "", accountId: "1", roleName: "r", state: "signed_in", expiresAt: "", expiring: false }],
    loggingOut: {},
    refreshExpiry: awsRefresh,
  });
  useGcpLoginStore.setState({
    profiles: [{ name: "g", label: "", project: "p", account: "", state: "signed_in" }],
    requests: [],
    refreshProfiles: gcpRefresh,
    refresh: gcpRequests,
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  delete g.IS_REACT_ACT_ENVIRONMENT;
});

const draw = (el: ReactNode) => act(async () => root!.render(
      <ToastProvider>
        <ConfirmProvider>{el}</ConfirmProvider>
      </ToastProvider>,
    ));

const cases = [
  { name: "AWS", btn: ".ws-aws:not(.ws-gcp) .ws-aws-btn", Chip: AwsProfilesChip },
  { name: "Google Cloud", btn: ".ws-gcp-btn", Chip: GcpProfilesChip },
] as const;

describe("WS bar profile chips under the width fold", () => {
  for (const { name, btn, Chip } of cases) {
    it(`${name}: folding the chip away closes its popover`, async () => {
      await draw(<Chip />);
      await act(async () => host!.querySelector<HTMLButtonElement>(btn)!.click());
      expect(host!.querySelector(btn)!.getAttribute("aria-expanded")).toBe("true");
      await draw(<Chip hidden />);
      expect(host!.querySelector(btn)!.getAttribute("aria-expanded")).toBe("false");
      // Unfolded again, it does not come back open.
      await draw(<Chip />);
      expect(host!.querySelector(btn)!.getAttribute("aria-expanded")).toBe("false");
    });
  }

  it("a passive copy asks nothing and listens to nothing; the owner does", async () => {
    await draw(<><AwsProfilesChip passive /><GcpProfilesChip passive /></>);
    await act(async () => void window.dispatchEvent(new Event("focus")));
    expect(awsRefresh).not.toHaveBeenCalled();
    expect(gcpRefresh).not.toHaveBeenCalled();
    // Positive control: the same chips as owners ask on mount and on focus.
    await draw(<><AwsProfilesChip /><GcpProfilesChip /></>);
    expect(awsRefresh).toHaveBeenCalled();
    expect(gcpRefresh).toHaveBeenCalled();
    gcpRequests.mockClear();
    await act(async () => void window.dispatchEvent(new Event("focus")));
    expect(gcpRequests).toHaveBeenCalledTimes(1);
  });
});
