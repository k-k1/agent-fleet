// The background memory review's setting (ADR 0117): off by default, discloses what it sends and
// costs, and says when it cannot work because AF memory is off.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

let unavailable = false;
vi.mock("../../../lib/aiAssistResolution.ts", () => ({ useMemoryReviewUnavailable: () => unavailable }));

const { MemoryReviewSwitch } = await import("./MemoryReviewSwitch.tsx");
const { setSetting, getSettings } = await import("../../../lib/settings.ts");
const { t } = await import("../../../lib/i18n/index.ts");

let root: Root;
let host: HTMLDivElement;

beforeEach(() => {
  localStorage.clear();
  unavailable = false;
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const mount = () => act(async () => root.render(<MemoryReviewSwitch />));
const button = (label: string) => [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === label);

describe("MemoryReviewSwitch", () => {
  it("is off by default and turning it on stores the Agent's value", async () => {
    await mount();
    expect(getSettings().memoryReview).toBe("off");
    await act(async () => button(t("mem.review_propose"))!.click());
    expect(getSettings().memoryReview).toBe("propose");
    await act(async () => button(t("mem.review_off"))!.click());
    expect(getSettings().memoryReview).toBe("off");
  });

  it("discloses where the conversation goes, the cost and the missing approval screen", async () => {
    await mount();
    const note = host.textContent ?? "";
    expect(note).toContain(t("mem.review_note"));
    // The text itself names the provider choice, the tokens and that nothing is saved unapproved.
    for (const word of [/AI 支援|AI assist/, /プロバイダ|provider/, /トークン|tokens/, /承認|approve/]) {
      expect(t("mem.review_note")).toMatch(word);
    }
  });

  it("warns when a mode is chosen while AF memory is off", async () => {
    setSetting("agentMemory", false);
    setSetting("memoryReview", "off");
    await mount();
    expect(host.textContent).not.toContain(t("mem.review_needs_memory"));
    setSetting("memoryReview", "propose");
    await mount();
    expect(host.textContent).toContain(t("mem.review_needs_memory"));
    setSetting("agentMemory", true);
    await mount();
    expect(host.textContent).not.toContain(t("mem.review_needs_memory"));
  });

  it("keeps automatic review a separate switch, off by default, with its own explanation", async () => {
    await mount();
    expect(getSettings().memoryReviewAuto).toBe(false);
    // Choosing the mode does not arm the loop.
    await act(async () => button(t("mem.review_propose"))!.click());
    expect(getSettings().memoryReview).toBe("propose");
    expect(getSettings().memoryReviewAuto).toBe(false);
    expect(host.textContent).toContain(t("mem.review_auto_switch"));
    expect(host.textContent).toContain(t("mem.review_auto_note"));
    // The note names the cost, the sessions it looks at and the look-back.
    expect(t("mem.review_auto_note")).toMatch(/トークン|tokens/);
    expect(t("mem.review_auto_note")).toMatch(/48/);
    const on = button(t("common.on")); // the mode row has no "On": it is the automatic switch
    expect(on).toBeDefined();
    await act(async () => on!.click());
    expect(getSettings().memoryReviewAuto).toBe(true);
  });

  it("says automatic review needs the review mode", async () => {
    setSetting("memoryReview", "off");
    setSetting("memoryReviewAuto", false);
    await mount();
    expect(host.textContent).not.toContain(t("mem.review_auto_needs_review"));
    setSetting("memoryReviewAuto", true);
    await mount();
    expect(host.textContent).toContain(t("mem.review_auto_needs_review"));
    setSetting("memoryReview", "propose");
    await mount();
    expect(host.textContent).not.toContain(t("mem.review_auto_needs_review"));
  });

  it("says the review cannot run here only when no deny-all backend is available", async () => {
    await mount();
    expect(host.textContent).not.toContain(t("mem.review_no_backend"));
    unavailable = true;
    await mount();
    expect(host.textContent).toContain(t("mem.review_no_backend"));
  });
});
