// Muse Code's card carries three facts no other agent card has to, and all three are about what
// the member is billed (ADR 0095 decision 9 / P2-6). Each is easy to render and easy to leave out
// without anything looking broken — a connected card with no note reads as fine — so each has a
// case here, paired with the arm where the note must NOT appear.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));
const toast = vi.fn();
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => toast }));
// The connected branch renders DisconnectButton, which asks for confirmation through context.
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => () => Promise.resolve(true) }));

const { MuseCard } = await import("./MuseCard.tsx");

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const text = () => host?.textContent || "";
const buttons = () => [...(host?.querySelectorAll("button") ?? [])];
const buttonWith = (s: string) => buttons().find((b) => (b.textContent || "").includes(s));

async function mount(st: Record<string, unknown> | undefined, reload = () => {}) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<MuseCard running st={st} reload={reload} />);
  });
}

beforeEach(() => {
  api.mockImplementation(() => Promise.resolve({}));
  apiJSON.mockImplementation(() => Promise.resolve({}));
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.clearAllMocks();
});

describe("MuseCard", () => {
  // 🔴 The account login writes an `api_key` of its own, so this cannot be inferred in the
  // Console — the Agent's `metered` is the answer. Both arms, because a card that never shows the
  // note and a card that always does are equally wrong and only the pair tells them apart.
  it("says it is metered only when the credential really bills per use", async () => {
    await mount({ supported: true, connected: true, email: "m@example.com", metered: true });
    expect(text()).toMatch(/per use|従量/);

    act(() => root?.unmount());
    host?.remove();
    await mount({ supported: true, connected: true, email: "m@example.com", metered: false });
    expect(text()).not.toMatch(/per use|従量/);
    expect(text()).toContain("m@example.com");
  });

  // META_API_KEY overrides the stored sign-in, so connected can be true, metered false, and the
  // member still billed per use. Without this note the card states the opposite of the truth.
  it("surfaces an environment API key even while the pill says connected", async () => {
    await mount({ supported: true, connected: true, email: "m@example.com", metered: false, env_key: true });
    expect(text()).toMatch(/META_API_KEY/);
    // Plain text, not markdown: nothing renders it, so a ** reaches the member verbatim.
    expect(text()).not.toContain("**");
  });

  // The binary is not in the image, so a fresh workspace has to be offered the install rather
  // than a sign-in that cannot run.
  it("offers the install before the sign-in when the binary is absent", async () => {
    await mount({ supported: false, connected: false });
    expect(buttonWith("Muse Code")).toBeTruthy();
    expect(text()).toMatch(/299MB|299MB/);
    // The sign-in must NOT be offered yet: it needs the binary.
    expect(text()).not.toMatch(/Meta account|Meta アカウント/);
  });

  it("offers both credentials once installed, sign-in first", async () => {
    await mount({ supported: true, connected: false });
    expect(text()).toMatch(/Meta account|Meta アカウント/);
    expect(text()).toMatch(/API key|API キー/);
  });

  // 🔴 The warning is on the screen the member decides on, not in a toast after the Agent's 409:
  // this route destroys an account sign-in as well as moving the workspace onto metered billing.
  it("warns before the API-key field, not after the failure", async () => {
    await mount({ supported: true, connected: false });
    const keyBtn = buttonWith("API key") || buttonWith("API キー");
    expect(keyBtn).toBeTruthy();
    await act(async () => {
      keyBtn!.click();
    });
    expect(host?.querySelector('input[type="password"]')).toBeTruthy();
    expect(text()).toMatch(/removes|消えます/i);
    expect(text()).not.toContain("**");
  });

  // muse is managed-only, so there is no launch guard to re-pin implicitly: this affordance is
  // the only route to a pin bump or to repairing a self-installed shadow. It must appear only
  // when the Agent positively reports a mismatch — an unknown version must not nag.
  it("offers the update only when the Agent reports a version mismatch", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(
        path === "api/connections/muse/install"
          ? { state: "idle", installed: true, version: "1.2.9-R3300.4", pin: "1.3.0-R3401.1", updateAvailable: true }
          : {},
      ),
    );
    await mount({ supported: true, connected: true, email: "m@example.com" });
    expect(text()).toContain("1.2.9-R3300.4");
    expect(text()).toContain("1.3.0-R3401.1");

    act(() => root?.unmount());
    host?.remove();
    api.mockImplementation(() =>
      Promise.resolve({ state: "idle", installed: true, version: "", pin: "1.3.0-R3401.1", updateAvailable: false }),
    );
    await mount({ supported: true, connected: true, email: "m@example.com" });
    expect(text()).not.toContain("1.3.0-R3401.1");
  });

  // A sign-in child that exited without writing a credential is a finished failure. Reporting it
  // ends the wait instead of spinning to the 15-minute deadline for something that cannot happen.
  it("stops waiting when the sign-in flow reports it failed", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(
        path === "api/connections/muse/start"
          ? { url: "https://auth.meta.com/oauth/device/?code=ABCD-EFGH", user_code: "ABCD-EFGH", flow_id: "f1" }
          : {},
      ),
    );
    apiJSON.mockImplementation(() => Promise.resolve({ connected: false, failed: true, detail: "device code expired" }));
    await mount({ supported: true, connected: false });
    const signIn = buttonWith("Meta account") || buttonWith("Meta アカウント");
    await act(async () => {
      signIn!.click();
    });
    // The device code is on screen, which is what the member compares in the browser.
    expect(text()).toContain("ABCD-EFGH");
  });
  // The launch defaults (ADR 0095 P2-9). Three things that are each invisible when wrong:
  // the catalogue's contributor twins have to stay offered, the two rows have to be dropdowns
  // rather than segmented controls (four 26-character ids and nine effort values both overflow
  // `.choice-seg`, which is an inline-flex that does not wrap), and the note explaining what a
  // contributor model costs has to be plain text — the card's other notes were written with
  // markdown asterisks that nothing renders, so they reached the member as literal `**`.
  it("offers the model and effort defaults as dropdowns, with the contributor note in plain text", async () => {
    const efforts = ["none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"];
    api.mockImplementation((path: string) =>
      Promise.resolve(
        path === "api/agents/muse/models"
          ? {
              models: [
                { id: "muse-spark-1.3", label: "muse-spark-1.3", efforts },
                { id: "muse-spark-1.3-contributor", label: "muse-spark-1.3-contributor", efforts },
              ],
            }
          : {},
      ),
    );
    await mount({ supported: true, connected: true, email: "m@example.com" });
    const disclosure = buttons().find((b) => b.getAttribute("aria-expanded") !== null);
    expect(disclosure).toBeTruthy();
    await act(async () => {
      disclosure!.click();
    });
    // The catalogue fetch resolves after the mount; let its promise chain settle.
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    // Not a count — the hidden-models row has a select of its own. What is asserted is that
    // NEITHER of the two rows fell to the segmented control.
    expect(host?.querySelector(".choice-seg")).toBeNull();
    const optionsOf = (s: HTMLSelectElement) => [...s.options].map((o) => o.textContent || "");
    const selects = [...(host?.querySelectorAll("select") ?? [])] as HTMLSelectElement[];
    // The member keeps the choice: a contributor twin is offered, not filtered away.
    const modelRow = selects.find((s) => optionsOf(s).includes("muse-spark-1.3-contributor"));
    expect(modelRow).toBeTruthy();
    // All nine effort values reach the picker — the wire enum, not a subset.
    const effortRow = selects.find((s) => optionsOf(s).includes("ultra"));
    expect(effortRow).toBeTruthy();
    for (const e of efforts) expect(optionsOf(effortRow!)).toContain(e);

    // The note itself, not merely the word "contributor" — which the catalogue's own option
    // labels also carry, so asserting on that passes with the note deleted (it did).
    const notes = [...(host?.querySelectorAll(".ps-note") ?? [])].map((n) => n.textContent || "");
    expect(notes.some((n) => /improve the product|製品改善/.test(n))).toBe(true);
    // Nothing in this card may ship raw markdown: it is rendered as plain text.
    expect(text()).not.toContain("**");
  });
});
