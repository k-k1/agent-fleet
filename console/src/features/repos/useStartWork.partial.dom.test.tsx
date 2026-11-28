// A launch whose session was created but whose draft did not fully arrive (#5077) must say so:
// the caller deletes the memo and clears the draft on ok, so ok alone would lose what never
// reached the session.
import "fake-indexeddb/auto";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { clearAttachDraftSnapshot, readAttachDraftSnapshots, resetAttachDraftDB, settleAttachDraft, writeAttachDraftSnapshot } from "../../lib/attachDraft.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import { launchDraftOwner, useHeldLaunchDrafts } from "./launchDeliveryDraft.ts";

const apiJSON = vi.fn();
const pasteImage = vi.fn();
vi.mock("../../core/api/client.ts", async (orig) => {
  const real = (await orig()) as Record<string, unknown>;
  return { ...real, apiJSON: (...a: unknown[]) => apiJSON(...a), pasteImage: (...a: unknown[]) => pasteImage(...a) };
});
const stash = vi.fn();
vi.mock("../memo/launchStash.ts", () => ({ stashLaunchDraft: (...a: unknown[]) => stash(...a) }));
const openSessionChat = vi.fn();
vi.mock("../sessions/open.ts", () => ({ openSessionChat: (...args: unknown[]) => openSessionChat(...args), openSessionTerminal: vi.fn() }));

const { useStartWork } = await import("./useStartWork.ts");
const { LaunchDeliveryRecovery } = await import("./LaunchDeliveryRetry.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { t } = await import("../../lib/i18n/index.ts");
const { takeLaunchSeed, subscribeLaunchSeed } = await import("../../lib/launchSeed.ts");
const { readPromptHistory } = await import("../../lib/promptHistory.ts");

let root: Root | null = null;
let host: HTMLDivElement;
let start: ReturnType<typeof useStartWork> | null = null;
function Probe() {
  start = useStartWork();
  return null;
}
const toasts = () => [...document.querySelectorAll(".ui-toast-msg")].map((n) => n.textContent || "");
const file = (name: string, content = "x") => new File([content], name, { type: "image/png" });
const owner = { tenant: "first", whoami: { email: "first@example.com" }, resolved: true };
const draftKey = () => launchDraftOwner(useTenantStore.getState()).prefix + "s1";
const reviewButton = () => document.querySelector<HTMLButtonElement>(".ui-toast-msg button");
const recoveryDialog = () => document.querySelector<HTMLFormElement>("form[role=dialog]");
const chips = (dialog: Element) => dialog.querySelectorAll(".ma-chip");
// Each chip as [file name, status note]: the name and the note must sit on the same chip.
const chipInfo = (dialog: Element) =>
  [...chips(dialog)].map((chip) => [chip.getAttribute("title"), chip.querySelector(".ma-note")?.textContent ?? ""]);
const pending = t("rp.delivery_pending");
const focusTarget = () => document.querySelector<HTMLButtonElement>("[data-launch-delivery-focus]");
const submit = (dialog: HTMLFormElement) => dialog.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
const dialogButton = (label: string) => [...recoveryDialog()!.querySelectorAll("button")].find((button) => button.textContent === label);
const waitForRetry = () => act(async () => vi.waitFor(() => expect(useHeldLaunchDrafts.getState().operations[draftKey()]).toBeUndefined()));

function waitForAbort(signal: AbortSignal): Promise<never> {
  return new Promise((_, reject) => signal.addEventListener("abort", () => reject(signal.reason), { once: true }));
}

async function editPrompt(text: string) {
  const prompt = recoveryDialog()!.querySelector("textarea")!;
  const setValue = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
  await act(async () => {
    setValue.call(prompt, text);
    prompt.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function submitRetry(dialog: HTMLFormElement) {
  await act(async () => submit(dialog));
  await waitForRetry();
}

async function mount() {
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ToastProvider>
        <button data-launch-delivery-focus type="button">Notifications</button>
        <LaunchDeliveryRecovery />
        <Probe />
      </ToastProvider>,
    );
  });
}

async function reload(scope: Partial<ReturnType<typeof useTenantStore.getState>> = owner) {
  await act(async () => root?.unmount());
  useHeldLaunchDrafts.setState({ drafts: {}, operations: {} });
  resetAttachDraftDB();
  useTenantStore.setState(scope);
  await mount();
}

beforeEach(async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  localStorage.clear();
  globalThis.indexedDB = new IDBFactory();
  resetAttachDraftDB();
  useTenantStore.setState(owner);
  useHeldLaunchDrafts.setState({ drafts: {}, operations: {} });
  takeLaunchSeed("s1");
  apiJSON.mockReset();
  pasteImage.mockReset();
  openSessionChat.mockClear();
  stash.mockReset();
  stash.mockResolvedValue({ ok: true });
  host = document.createElement("div");
  document.body.appendChild(host);
  await mount();
});
afterEach(async () => {
  await act(async () => root?.unmount());
  host.remove();
  root = null;
  start = null;
  vi.unstubAllGlobals();
});

const launch = async (images: File[]) => {
  let r!: Awaited<ReturnType<NonNullable<typeof start>>>;
  await act(async () => {
    r = await start!(
      { dir: "/repos/x", repo: "x" },
      {
        kind: "claude", driver: "", model: "", effort: "", startMode: "normal",
        prompt: "look at these", title: "", images, subdir: "", base: "", newBranch: "", worktree: false,
      },
    );
  });
  return r;
};

const created = (path: string, body?: unknown) =>
  path === "api/sessions" ? Promise.resolve({ name: "s1" }) : Promise.resolve(body ?? { queued: "s1" });

describe("partial draft delivery", () => {
  it("is complete when every file and the prompt arrive", async () => {
    apiJSON.mockImplementation((p: string) => created(p));
    pasteImage.mockResolvedValue({ status: 200, path: "/p/a.png" });
    const r = await launch([file("a.png")]);
    expect(r).toEqual({ ok: true, name: "s1" });
    expect(toasts()).not.toContain(t("rp.draft_kept"));
  });

  it("retains a refused attachment for retry to the created session", async () => {
    apiJSON.mockImplementation((p: string) => created(p));
    pasteImage.mockResolvedValueOnce({ status: 200, path: "/p/a.png" }).mockResolvedValueOnce({ status: 413, error: { code: "paste_too_large" } });
    const r = await launch([file("a.png"), file("b.png")]);
    expect(r.ok).toBe(true);
    expect(r.incomplete).toEqual({ files: ["b.png"], prompt: true, saved: false, held: true });
    expect(stash).not.toHaveBeenCalled();
    expect(apiJSON).toHaveBeenCalledTimes(1);
    const review = document.querySelector<HTMLButtonElement>(".ui-toast-msg button")!;
    await act(async () => review.click());
    let dialog = document.querySelector<HTMLFormElement>("form[role=dialog]")!;
    expect(dialog.querySelector("textarea")?.value).toBe("look at these");
    expect(chipInfo(dialog)).toEqual([["a.png", ""], ["b.png", pending]]);
    pasteImage.mockResolvedValueOnce({ status: 413, error: { code: "paste_too_large" } });
    await submitRetry(dialog);
    expect(chipInfo(dialog)).toEqual([["a.png", ""], ["b.png", pending]]);
    expect(apiJSON).toHaveBeenCalledTimes(1);

    await act(async () => dialog.querySelector<HTMLButtonElement>("header button")!.click());
    expect(dialog.isConnected).toBe(false);
    await act(async () => review.click());
    dialog = document.querySelector<HTMLFormElement>("form[role=dialog]")!;
    expect(chipInfo(dialog)).toEqual([["a.png", ""], ["b.png", pending]]);
    await act(async () => dialog.querySelectorAll<HTMLButtonElement>(".ma-del")[1].click());
    const picker = dialog.querySelector<HTMLInputElement>('input[type="file"]')!;
    Object.defineProperty(picker, "files", { value: [file("smaller.png")] });
    await act(async () => picker.dispatchEvent(new Event("change", { bubbles: true })));

    pasteImage.mockResolvedValueOnce({ status: 200, path: "/p/smaller.png" });
    await submitRetry(dialog);
    expect(pasteImage.mock.calls.map(([session, f]) => [session, f.name])).toEqual([
      ["s1", "a.png"], ["s1", "b.png"], ["s1", "b.png"], ["s1", "smaller.png"],
    ]);
    expect(apiJSON).toHaveBeenCalledTimes(2);
    expect(apiJSON).toHaveBeenLastCalledWith("api/sessions/s1/input", "POST", {
      prompt: expect.stringMatching(/look at these[\s\S]*\/p\/a\.png[\s\S]*\/p\/smaller\.png/),
      when_ready: true,
    }, undefined, { signal: expect.any(AbortSignal) });
    expect(document.querySelector("form[role=dialog]")).toBeNull();
    expect(review.isConnected).toBe(false);
  });

  it("shows the held draft as an update-style toast and edits files through the launch form's controls", async () => {
    apiJSON.mockImplementation((p: string) => created(p));
    pasteImage.mockResolvedValueOnce({ status: 200, path: "/p/a.png" }).mockResolvedValueOnce({ status: 413, error: { code: "paste_too_large" } });
    await launch([file("a.png"), file("b.png")]);
    const toast = document.querySelector(".ui-toast-msg .update-toast")!;
    expect(toast.querySelector(".update-toast-txt")?.textContent).toBe(t("rp.delivery_held", { session: "s1" }));
    expect(toast.querySelector("button.update-toast-btn")?.textContent).toBe(t("rp.delivery_review"));

    await act(async () => reviewButton()!.click());
    const dialog = recoveryDialog()!;
    // The picker is the launch form's hidden input behind a + button, not a visible native input.
    const picker = dialog.querySelector<HTMLInputElement>('input[type="file"]')!;
    expect(picker.hidden).toBe(true);
    const plus = dialog.querySelector<HTMLButtonElement>("button.launch-attach-btn")!;
    const clicked = vi.spyOn(picker, "click");
    await act(async () => plus.click());
    expect(clicked).toHaveBeenCalledTimes(1);

    const prompt = dialog.querySelector("textarea")!;
    const pasted = new Event("paste", { bubbles: true, cancelable: true });
    Object.defineProperty(pasted, "clipboardData", { value: { files: [file("pasted.png")] } });
    await act(async () => prompt.dispatchEvent(pasted));
    expect(pasted.defaultPrevented).toBe(true);
    expect(chipInfo(dialog)).toEqual([["a.png", ""], ["b.png", pending], ["pasted.png", pending]]);

    const dropped = new Event("drop", { bubbles: true, cancelable: true });
    Object.defineProperty(dropped, "dataTransfer", { value: { types: ["Files"], files: [file("dropped.png")] } });
    await act(async () => prompt.dispatchEvent(dropped));
    expect(dropped.defaultPrevented).toBe(true);
    expect(chipInfo(dialog)).toEqual([["a.png", ""], ["b.png", pending], ["pasted.png", pending], ["dropped.png", pending]]);
    // The browser only allows a drop where dragover was cancelled.
    const dragOver = (types: string[]) => {
      const ev = new Event("dragover", { bubbles: true, cancelable: true });
      Object.defineProperty(ev, "dataTransfer", { value: { types } });
      prompt.dispatchEvent(ev);
      return ev.defaultPrevented;
    };
    expect(dragOver(["Files"])).toBe(true);
    expect(dragOver(["text/plain"])).toBe(false);

    const textPaste = new Event("paste", { bubbles: true, cancelable: true });
    Object.defineProperty(textPaste, "clipboardData", { value: { files: [] } });
    await act(async () => prompt.dispatchEvent(textPaste));
    expect(textPaste.defaultPrevented).toBe(false);
    expect(chips(dialog)).toHaveLength(4);

    // Each x names its file and removes that file, not its neighbour.
    const removers = [...dialog.querySelectorAll<HTMLButtonElement>(".ma-del")];
    expect(removers.map((x) => x.getAttribute("aria-label"))).toEqual(
      ["a.png", "b.png", "pasted.png", "dropped.png"].map((name) => t("chat.remove_named", { name })),
    );
    await act(async () => removers[1].click());
    expect(chipInfo(dialog)).toEqual([["a.png", ""], ["pasted.png", pending], ["dropped.png", pending]]);
    expect(useHeldLaunchDrafts.getState().drafts[draftKey()].files.map(({ file }) => file.name))
      .toEqual(["a.png", "pasted.png", "dropped.png"]);
  });

  it("removes only the entry whose x was pressed when the same File is staged twice", async () => {
    apiJSON.mockImplementation((p: string) => created(p));
    pasteImage.mockResolvedValueOnce({ status: 200, path: "/p/a.png" }).mockResolvedValueOnce({ status: 413 });
    const twice = file("a.png");
    await launch([twice, twice]);
    await act(async () => reviewButton()!.click());
    const dialog = recoveryDialog()!;
    expect(chipInfo(dialog)).toEqual([["a.png", ""], ["a.png", pending]]);
    await act(async () => dialog.querySelectorAll<HTMLButtonElement>(".ma-del")[1].click());
    expect(chipInfo(dialog)).toEqual([["a.png", ""]]);
    expect(useHeldLaunchDrafts.getState().drafts[draftKey()].files.map(({ path }) => path)).toEqual(["/p/a.png"]);
  });

  it("offers no attachment edits while an upload runs or after its outcome is unknown", async () => {
    apiJSON.mockImplementation((path: string, _method, _body, _headers, init) =>
      path === "api/sessions" ? created(path) : waitForAbort(init.signal));
    pasteImage.mockResolvedValue({ status: 413 });
    await launch([file("a.png"), file("b.png")]);
    const key = draftKey();
    await act(async () => reviewButton()!.click());
    const dialog = recoveryDialog()!;
    const prompt = dialog.querySelector("textarea")!;
    const filesNow = () => useHeldLaunchDrafts.getState().drafts[key].files;
    // Paste, drop and dragover must all be refused: a cancelled dragover is the browser's
    // signal that a drop is welcome, so it has to stay uncancelled too.
    const tryEdits = async () => {
      const before = filesNow();
      const pasted = new Event("paste", { bubbles: true, cancelable: true });
      Object.defineProperty(pasted, "clipboardData", { value: { files: [file("late.png")] } });
      const dropped = new Event("drop", { bubbles: true, cancelable: true });
      Object.defineProperty(dropped, "dataTransfer", { value: { types: ["Files"], files: [file("late.png")] } });
      const over = new Event("dragover", { bubbles: true, cancelable: true });
      Object.defineProperty(over, "dataTransfer", { value: { types: ["Files"] } });
      await act(async () => {
        prompt.dispatchEvent(pasted);
        prompt.dispatchEvent(dropped);
        prompt.dispatchEvent(over);
      });
      expect(over.defaultPrevented).toBe(false);
      expect(dropped.defaultPrevented).toBe(false);
      expect(filesNow()).toBe(before);
    };

    // Uploading: the + button is disabled and the chips carry no x.
    pasteImage.mockImplementationOnce((_s, _f, signal: AbortSignal) => waitForAbort(signal));
    await act(async () => submit(dialog));
    await act(async () => vi.waitFor(() => expect(useHeldLaunchDrafts.getState().operations[key]?.phase).toBe("uploading")));
    expect(dialog.querySelector<HTMLButtonElement>("button.launch-attach-btn")!.disabled).toBe(true);
    expect(dialog.querySelector(".ma-del")).toBeNull();
    expect(chipInfo(dialog)).toEqual([["a.png", pending], ["b.png", pending]]);
    await tryEdits();
    await act(async () => dialogButton(t("rp.delivery_cancel_upload"))!.click());
    await waitForRetry();

    // Idle again: editable, and the same drop is accepted.
    expect(dialog.querySelector(".ma-del")).not.toBeNull();
    pasteImage.mockResolvedValue({ status: 200, path: "/p/x.png" });
    await act(async () => submit(dialog));
    await act(async () => vi.waitFor(() => expect(apiJSON).toHaveBeenCalledTimes(2)));
    await act(async () => dialogButton(t("rp.delivery_stop_waiting"))!.click());
    await waitForRetry();

    // Outcome unknown: no + button, no x, names still identify the files, edits are refused.
    expect(dialog.querySelector("button.launch-attach-btn, input[type=file], .ma-del")).toBeNull();
    expect(chips(dialog)).toHaveLength(2);
    expect([...chips(dialog)].map((chip) => chip.getAttribute("title"))).toEqual(["a.png", "b.png"]);
    await tryEdits();
  });

  it("restores edits and unsent bytes after reload, then sends to the original session", async () => {
    apiJSON.mockImplementation((p: string) => created(p));
    pasteImage.mockResolvedValueOnce({ status: 200, path: "/p/a.png" }).mockResolvedValueOnce({ status: 413 });
    await launch([file("a.png"), file("b.png")]);
    await act(async () => reviewButton()!.click());
    const dialog = recoveryDialog()!;
    const picker = dialog.querySelector<HTMLInputElement>('input[type="file"]')!;
    await editPrompt("edited after refusal");
    await act(async () => dialog.querySelectorAll<HTMLButtonElement>(".ma-del")[1].click());
    Object.defineProperty(picker, "files", { value: [file("new.png", "new file bytes")] });
    await act(async () => picker.dispatchEvent(new Event("change", { bubbles: true })));
    await settleAttachDraft(draftKey());

    await reload();
    await vi.waitFor(() => expect(reviewButton()).not.toBeNull());
    const review = reviewButton()!;
    review.focus();
    await act(async () => review.click());
    const restored = recoveryDialog()!;
    expect(restored.querySelector("textarea")?.value).toBe("edited after refusal");
    expect(chipInfo(restored)).toEqual([["a.png", ""], ["new.png", pending]]);
    expect(apiJSON).toHaveBeenCalledTimes(1);
    pasteImage.mockImplementationOnce(async (_session: string, uploaded: File) => {
      expect(uploaded.type).toBe("image/png");
      expect(await uploaded.text()).toBe("new file bytes");
      return { status: 200, path: "/p/new.png" };
    });
    await submitRetry(restored);
    expect(recoveryDialog()).toBeNull();
    expect(document.activeElement).toBe(focusTarget());
    expect(pasteImage.mock.calls.map(([session, file]) => [session, file.name])).toEqual([
      ["s1", "a.png"], ["s1", "b.png"], ["s1", "new.png"],
    ]);
    expect(apiJSON).toHaveBeenCalledTimes(2);
    expect(apiJSON).toHaveBeenLastCalledWith("api/sessions/s1/input", "POST", {
      prompt: expect.stringMatching(/edited after refusal[\s\S]*\/p\/a\.png[\s\S]*\/p\/new\.png/),
      when_ready: true,
    }, undefined, { signal: expect.any(AbortSignal) });
    await reload();
    expect(await readAttachDraftSnapshots(launchDraftOwner(useTenantStore.getState()).prefix)).toEqual([]);
    expect(reviewButton()).toBeNull();
  });

  it("only restores offers for the resolved member and tenant", async () => {
    apiJSON.mockImplementation((p: string) => created(p));
    pasteImage.mockResolvedValue({ status: 413 });
    await launch([file("b.png")]);
    await reload({ ...owner, resolved: false });
    expect(reviewButton()).toBeNull();

    await act(async () => useTenantStore.setState({ ...owner, whoami: { email: "another@example.com" } }));
    expect(reviewButton()).toBeNull();
    await act(async () => useTenantStore.setState({ ...owner, tenant: "other" }));
    expect(reviewButton()).toBeNull();
    await act(async () => useTenantStore.setState(owner));
    await vi.waitFor(() => expect(reviewButton()).not.toBeNull());
    await act(async () => reviewButton()!.click());
    await act(async () => useTenantStore.setState({ tenant: "other" }));
    expect(recoveryDialog()).toBeNull();
    expect(reviewButton()).toBeNull();
    expect(apiJSON).toHaveBeenCalledTimes(1);
  });

  it("stops a retry when the tenant changes during an upload", async () => {
    apiJSON.mockImplementation((p: string) => created(p));
    pasteImage.mockResolvedValue({ status: 413 });
    await launch([file("b.png"), file("c.png")]);
    const prefix = launchDraftOwner(useTenantStore.getState()).prefix;
    await act(async () => reviewButton()!.click());
    let finishUpload!: (result: { status: number; path: string }) => void;
    pasteImage.mockImplementationOnce(() => new Promise((resolve) => { finishUpload = resolve; }));
    await act(async () => recoveryDialog()!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
    await vi.waitFor(() => expect(finishUpload).toBeTypeOf("function"));
    await act(async () => useTenantStore.setState({ tenant: "other" }));
    expect(recoveryDialog()).toBeNull();
    expect(reviewButton()).toBeNull();
    await act(async () => useTenantStore.setState(owner));
    expect(reviewButton()?.disabled).toBe(true);
    await act(async () => reviewButton()!.click());
    expect(recoveryDialog()).toBeNull();
    await act(async () => finishUpload({ status: 200, path: "/p/b.png" }));
    await settleAttachDraft(prefix + "s1");
    const [stored] = await readAttachDraftSnapshots(prefix);
    expect(stored.items.map(({ name, path }) => [name, path])).toEqual([
      ["b.png", "/p/b.png"], ["c.png", ""],
    ]);
    expect(pasteImage).toHaveBeenCalledTimes(3);
    expect(apiJSON).toHaveBeenCalledTimes(1);
    expect(recoveryDialog()).toBeNull();
    await waitForRetry();
    expect(reviewButton()?.disabled).toBe(false);
  });

  it("cancels a stalled upload and retries only the pending files for the original session", async () => {
    apiJSON.mockImplementation((p: string) => created(p));
    pasteImage.mockResolvedValue({ status: 413 });
    await launch([file("a.png"), file("b.png", "pending bytes"), file("c.png")]);
    const key = draftKey();
    await act(async () => reviewButton()!.click());
    pasteImage.mockResolvedValueOnce({ status: 200, path: "/p/a.png" })
      .mockImplementationOnce((_session, _file, signal: AbortSignal) => waitForAbort(signal));
    await act(async () => submit(recoveryDialog()!));
    await act(async () => vi.waitFor(() => expect(pasteImage).toHaveBeenCalledTimes(5)));
    await act(async () => recoveryDialog()!.querySelector<HTMLButtonElement>("header button")!.click());
    expect(recoveryDialog()).toBeNull();
    const cancel = [...document.querySelectorAll<HTMLButtonElement>(".ui-toast-msg button")]
      .find((button) => button.textContent === t("rp.delivery_cancel_upload"));
    await act(async () => cancel!.click());
    await waitForRetry();
    expect(pasteImage.mock.lastCall?.[2].aborted).toBe(true);
    expect(apiJSON).toHaveBeenCalledTimes(1);
    expect(useHeldLaunchDrafts.getState().drafts[key].files.map(({ path }) => path)).toEqual(["/p/a.png", "", ""]);

    await reload();
    await act(async () => vi.waitFor(() => expect(useHeldLaunchDrafts.getState().drafts[key]).toBeDefined()));
    await act(async () => reviewButton()!.click());
    pasteImage.mockImplementation(async (_session, uploaded: File) => ({ status: 200, path: "/p/" + uploaded.name }));
    await submitRetry(recoveryDialog()!);
    expect(pasteImage.mock.calls.slice(5).map(([session, uploaded]) => [session, uploaded.name])).toEqual([
      ["s1", "b.png"], ["s1", "c.png"],
    ]);
    expect(apiJSON).toHaveBeenLastCalledWith("api/sessions/s1/input", "POST", {
      prompt: expect.stringMatching(/look at these[\s\S]*\/p\/a\.png[\s\S]*\/p\/b\.png[\s\S]*\/p\/c\.png/),
      when_ready: true,
    }, undefined, { signal: expect.any(AbortSignal) });
  });

  it("stops waiting for stalled input and blocks a stale tab or reload from sending it again", async () => {
    apiJSON.mockImplementation((path: string, _method, _body, _headers, init) =>
      path === "api/sessions" ? created(path) : waitForAbort(init.signal));
    pasteImage.mockResolvedValueOnce({ status: 413 }).mockResolvedValueOnce({ status: 200, path: "/p/a.png" });
    await launch([file("a.png")]);
    const key = draftKey();
    const stale = useHeldLaunchDrafts.getState().drafts[key];
    await act(async () => reviewButton()!.click());
    await act(async () => submit(recoveryDialog()!));
    await act(async () => vi.waitFor(() => expect(apiJSON).toHaveBeenCalledTimes(2)));
    const prefix = launchDraftOwner(useTenantStore.getState()).prefix;
    expect(await readAttachDraftSnapshots(prefix)).toMatchObject([{ deliveryPending: true, data: { session: "s1", prompt: "look at these" } }]);
    await act(async () => dialogButton(t("rp.delivery_stop_waiting"))!.click());
    await waitForRetry();
    expect(apiJSON.mock.lastCall?.[4].signal.aborted).toBe(true);
    expect(recoveryDialog()!.textContent).toContain(t("rp.delivery_unknown", { session: "s1" }));

    expect(await writeAttachDraftSnapshot(key, { session: "s1", kind: "claude", repo: "x", prompt: "stale edit" }, undefined, true)).toBe(false);
    await act(async () => useHeldLaunchDrafts.setState({ drafts: { [key]: stale } }));
    await submitRetry(recoveryDialog()!);
    expect(recoveryDialog()!.textContent).toContain(t("rp.delivery_unknown", { session: "s1" }));
    expect(pasteImage).toHaveBeenCalledTimes(2);

    await reload();
    await act(async () => vi.waitFor(() => expect(useHeldLaunchDrafts.getState().drafts[key]).toBeDefined()));
    await act(async () => reviewButton()!.click());
    await waitForRetry();
    const restored = recoveryDialog()!;
    expect(restored.querySelector("textarea")?.value).toBe("look at these");
    expect(restored.querySelector("textarea")?.readOnly).toBe(true);
    expect(chips(restored)).toHaveLength(1);
    expect(restored.querySelector(".ma-del, input[type=file]")).toBeNull();
    expect(dialogButton(t("rp.delivery_send"))).toBeUndefined();
    await submitRetry(restored);
    expect(apiJSON).toHaveBeenCalledTimes(2);
    expect(readPromptHistory("x")).toEqual([]);
    openSessionChat.mockClear();
    await act(async () => dialogButton(t("rp.delivery_open_session"))!.click());
    expect(recoveryDialog()).toBeNull();
    expect(openSessionChat).toHaveBeenCalledExactlyOnceWith("s1");
  });

  it.each([
    ["network loss", undefined],
    ["gateway timeout", { error: { code: "http_504" } }],
    ["request timeout", { error: { status: 408 } }],
    ["proxy reporting a closed request", { error: { status: 499 } }],
    ["malformed gateway error", { error: "connection lost" }],
    ["missing acknowledgement", {}],
  ])("keeps input with %s in the unknown state", async (_failure, response) => {
    apiJSON.mockImplementation((p: string) => created(p));
    pasteImage.mockResolvedValueOnce({ status: 413 }).mockResolvedValueOnce({ status: 200, path: "/p/a.png" });
    await launch([file("a.png")]);
    if (response === undefined) apiJSON.mockRejectedValueOnce(new Error("connection lost"));
    else apiJSON.mockResolvedValueOnce(response);
    await act(async () => reviewButton()!.click());
    await submitRetry(recoveryDialog()!);
    expect(recoveryDialog()!.textContent).toContain(t("rp.delivery_unknown", { session: "s1" }));
    expect(apiJSON).toHaveBeenCalledTimes(2);
  });

  it("reports storage loss after the retry dialog closes and keeps the prompt unsent", async () => {
    apiJSON.mockImplementation((p: string) => created(p));
    pasteImage.mockResolvedValueOnce({ status: 413 });
    await launch([file("a.png")]);
    await act(async () => reviewButton()!.click());
    let finishUpload!: (result: { status: number; path: string }) => void;
    pasteImage.mockImplementationOnce(() => new Promise((resolve) => { finishUpload = resolve; }));
    await act(async () => submit(recoveryDialog()!));
    await act(async () => vi.waitFor(() => expect(finishUpload).toBeTypeOf("function")));
    await act(async () => recoveryDialog()!.querySelector<HTMLButtonElement>("header button")!.click());
    expect(recoveryDialog()).toBeNull();

    vi.stubGlobal("indexedDB", undefined);
    resetAttachDraftDB();
    await act(async () => finishUpload({ status: 200, path: "/p/a.png" }));
    await waitForRetry();
    expect(toasts()).toContain(t("rp.delivery_storage_unavailable"));
    expect(apiJSON).toHaveBeenCalledTimes(1);
    expect(useHeldLaunchDrafts.getState().drafts[draftKey()]).toMatchObject({
      session: "s1", prompt: "look at these", files: [{ path: "/p/a.png" }],
    });
  });

  it("shows another tab's lock for send and discard without queuing either action", async () => {
    apiJSON.mockImplementation((p: string) => created(p));
    pasteImage.mockResolvedValue({ status: 413 });
    await launch([file("a.png")]);
    const key = draftKey();
    let held = true;
    const request = vi.fn(async (_name: string, _options: LockOptions, run: LockGrantedCallback<unknown>) =>
      run(held ? null : { name: key, mode: "exclusive" }));
    const original = Object.getOwnPropertyDescriptor(navigator, "locks");
    Object.defineProperty(navigator, "locks", { configurable: true, value: { request } });
    try {
      const discard = document.querySelector<HTMLButtonElement>(`[aria-label="${t("rp.delivery_discard")}"]`)!;
      await act(async () => discard.click());
      expect(toasts()).toContain(t("rp.delivery_other_tab"));
      expect(reviewButton()).not.toBeNull();
      await act(async () => reviewButton()!.click());
      await submitRetry(recoveryDialog()!);
      expect(recoveryDialog()!.textContent).toContain(t("rp.delivery_other_tab"));
      expect(request.mock.calls.map(([name, options]) => [name, options])).toEqual([
        [key, { ifAvailable: true }], [key, { ifAvailable: true }],
      ]);
      expect(pasteImage).toHaveBeenCalledTimes(1);
      expect(apiJSON).toHaveBeenCalledTimes(1);

      held = false;
      pasteImage.mockResolvedValueOnce({ status: 200, path: "/p/a.png" });
      await submitRetry(recoveryDialog()!);
      expect(recoveryDialog()).toBeNull();
      expect(apiJSON).toHaveBeenCalledTimes(2);
    } finally {
      if (original) Object.defineProperty(navigator, "locks", original);
      else Reflect.deleteProperty(navigator, "locks");
    }
  });

  it("keeps the original form draft until the launch has a resolved owner", async () => {
    await act(async () => useTenantStore.setState({ resolved: false, whoami: null }));
    expect(await launch([file("b.png")])).toEqual({ ok: false });
    expect(toasts()).toContain(t("rp.delivery_identity_pending"));
    expect(apiJSON).not.toHaveBeenCalled();
    expect(stash).not.toHaveBeenCalled();
  });

  it("cannot revive or send a draft another tab already cleared", async () => {
    apiJSON.mockImplementation((p: string) => created(p));
    pasteImage.mockResolvedValue({ status: 413 });
    await launch([file("b.png")]);
    await act(async () => reviewButton()!.click());
    expect(await clearAttachDraftSnapshot(draftKey())).toBe(true);
    await editPrompt("stale edit from another tab");
    await act(async () => settleAttachDraft(draftKey()));
    const prefix = launchDraftOwner(useTenantStore.getState()).prefix;
    expect(await readAttachDraftSnapshots(prefix)).toEqual([]);
    await submitRetry(recoveryDialog()!);
    expect(reviewButton()).toBeNull();
    expect(pasteImage).toHaveBeenCalledTimes(1);
    expect(apiJSON).toHaveBeenCalledTimes(1);
  });

  it("suppresses a delivered draft after a failed IndexedDB delete and a reload", async () => {
    apiJSON.mockImplementation((p: string) => created(p));
    pasteImage.mockResolvedValueOnce({ status: 413 }).mockResolvedValueOnce({ status: 200, path: "/p/b.png" });
    await launch([file("b.png")]);
    await act(async () => reviewButton()!.click());
    const deletion = vi.spyOn(IDBObjectStore.prototype, "delete").mockImplementationOnce(() => {
      throw new DOMException("Delete interrupted", "AbortError");
    });
    try {
      await submitRetry(recoveryDialog()!);
    } finally {
      deletion.mockRestore();
    }
    const prefix = launchDraftOwner(useTenantStore.getState()).prefix;
    expect(await readAttachDraftSnapshots(prefix)).toHaveLength(1);
    expect(apiJSON).toHaveBeenCalledTimes(2);
    await reload();
    await vi.waitFor(async () => expect(await readAttachDraftSnapshots(prefix)).toEqual([]));
    expect(reviewButton()).toBeNull();
    expect(apiJSON).toHaveBeenCalledTimes(2);
  });

  it("keeps the text memo fallback if the browser refuses to persist the draft", async () => {
    vi.stubGlobal("indexedDB", undefined);
    resetAttachDraftDB();
    apiJSON.mockImplementation((p: string) => created(p));
    pasteImage.mockResolvedValue({ status: 413 });
    await launch([file("b.png")]);
    expect(reviewButton()).not.toBeNull();
    expect(stash).toHaveBeenCalledExactlyOnceWith("x", "look at these", []);
    expect(toasts()).toContain(t("rp.delivery_storage_failed"));
  });

  it("keeps the retry draft if the session refuses the input", async () => {
    apiJSON.mockImplementation((p: string) => created(p, { error: { code: "busy", status: 409 } }));
    pasteImage.mockResolvedValueOnce({ status: 413 }).mockResolvedValueOnce({ status: 200, path: "/p/b.png" });
    await launch([file("b.png")]);
    const review = document.querySelector<HTMLButtonElement>(".ui-toast-msg button")!;
    await act(async () => review.click());
    const dialog = document.querySelector<HTMLFormElement>("form[role=dialog]")!;
    await submitRetry(dialog);
    const refusedCall = apiJSON.mock.lastCall?.slice(0, 3);
    expect(dialog.isConnected).toBe(true);
    expect(dialog.querySelector("textarea")?.value).toBe("look at these");
    expect(chips(dialog)).toHaveLength(1);
    expect(readPromptHistory("x")).toEqual([]);

    const onSeed = vi.fn(() => takeLaunchSeed("s1"));
    const unsubscribe = subscribeLaunchSeed("s1", onSeed);
    try {
      apiJSON.mockImplementation((p: string) => created(p, { sent: "s1" }));
      await submitRetry(dialog);
      expect(apiJSON).toHaveBeenCalledTimes(3);
      expect(apiJSON.mock.lastCall?.slice(0, 3)).toEqual(refusedCall);
      expect(pasteImage).toHaveBeenCalledTimes(2);
      expect(onSeed).toHaveBeenCalledTimes(1);
      expect(onSeed.mock.results[0]?.value).toContain("/p/b.png");
      expect(readPromptHistory("x")).toEqual(["look at these"]);
      expect(dialog.isConnected).toBe(false);
    } finally {
      unsubscribe();
    }
  });

  it("discards a held draft only after the retry dialog is closed", async () => {
    apiJSON.mockImplementation((p: string) => created(p));
    pasteImage.mockResolvedValue({ status: 413 });
    await launch([file("b.png")]);
    const review = document.querySelector<HTMLButtonElement>(".ui-toast-msg button")!;
    const discard = document.querySelector<HTMLButtonElement>(`[aria-label="${t("rp.delivery_discard")}"]`)!;
    await act(async () => review.click());
    const dialog = document.querySelector("form[role=dialog]")!;
    await act(async () => discard.click());
    expect(dialog.isConnected).toBe(true);
    await act(async () => dialog.querySelector<HTMLButtonElement>("header button")!.click());
    await act(async () => discard.click());
    await vi.waitFor(() => expect(review.isConnected).toBe(false));
    expect(apiJSON).toHaveBeenCalledTimes(1);
    await settleAttachDraft(draftKey());
    await reload();
    expect(await readAttachDraftSnapshots(launchDraftOwner(useTenantStore.getState()).prefix)).toEqual([]);
    expect(reviewButton()).toBeNull();
  });

  it("reports the file whose upload failed on the network", async () => {
    apiJSON.mockImplementation((p: string) => created(p));
    pasteImage.mockRejectedValue(new Error("offline"));
    const r = await launch([file("a.png")]);
    expect(r.incomplete).toEqual({ files: ["a.png"], prompt: true, saved: false, held: true });
  });

  it("reports a first prompt the Agent refused", async () => {
    apiJSON.mockImplementation((p: string) => (p === "api/sessions" ? Promise.resolve({ name: "s1" }) : Promise.resolve({ error: { code: "busy", message: "no" } })));
    pasteImage.mockResolvedValue({ status: 200, path: "/p/a.png" });
    const r = await launch([file("a.png")]);
    expect(r.ok).toBe(true);
    expect(r.incomplete).toEqual({ files: [], prompt: true, saved: true });
  });

  it("says so when the draft could not be saved as a memo either", async () => {
    stash.mockResolvedValue({ ok: false, reason: "failed" });
    pasteImage.mockResolvedValue({ status: 200, path: "/p/a.png" });
    apiJSON.mockImplementation((p: string) => created(p, { error: { code: "busy" } }));
    const r = await launch([file("a.png")]);
    expect(r.incomplete?.saved).toBe(false);
    expect(toasts()).toContain(t("rp.draft_kept"));
  });

  it("falls back to saving the text alone when the files cannot be saved", async () => {
    stash.mockReset();
    stash.mockResolvedValueOnce({ ok: false, reason: "too_large" }).mockResolvedValueOnce({ ok: true });
    apiJSON.mockImplementation((p: string) => created(p, { error: { code: "busy" } }));
    pasteImage.mockResolvedValue({ status: 200, path: "/p/big.png" });
    const r = await launch([file("big.png")]);
    expect(stash).toHaveBeenNthCalledWith(2, "x", "look at these", []);
    expect(r.incomplete?.saved).toBe(true);
    expect(toasts()).toContain(t("rp.draft_saved_text"));
    expect(toasts()).not.toContain(t("rp.draft_saved"));
  });

  it("reports a first prompt whose request itself failed", async () => {
    apiJSON.mockImplementation((p: string) => (p === "api/sessions" ? Promise.resolve({ name: "s1" }) : Promise.reject(new Error("x"))));
    pasteImage.mockResolvedValue({ status: 200, path: "/p/a.png" });
    const r = await launch([file("a.png")]);
    expect(r.incomplete?.prompt).toBe(true);
  });
});
