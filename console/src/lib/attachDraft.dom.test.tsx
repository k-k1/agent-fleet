// useAttachDraft — the composer's side of the attachment draft: what the user sees after
// closing the Start working dialog and reopening it, or after switching the mirror to another session
// and back. The store next door (attachDraft.test.ts) proves the bytes round-trip; this
// proves the wiring around it, which is where the two orderings that can eat a draft live:
// the write that must not run before hydration (it would delete what it is loading), and
// the paste that lands while hydration is still in flight.
import "fake-indexeddb/auto";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { makeAttachment, resetAttachDraftDB, useAttachDraft, writeAttachDraft } from "./attachDraft.ts";

// A stand-in composer: the same three moves both real ones make (stage a file, drop one,
// clear on send/launch), with the list rendered so the DOM can be read back.
function Composer({ dkey }: { dkey: string | null }) {
  const attach = useAttachDraft(dkey);
  return (
    <div>
      <ul>
        {attach.items.map((a) => (
          <li key={a.id} data-name={a.name} data-path={a.path} data-url={a.url} data-has-file={a.file ? "1" : "0"} />
        ))}
      </ul>
      <button className="add" onClick={() => attach.add([makeAttachment(new File(["PNG"], "shot.png", { type: "image/png" }))])} />
      <button className="add-uploaded" onClick={() => attach.add([makeAttachment(new File(["LOG"], "server.log", { type: "text/plain" }), { name: "paste-2-server.log", path: "/p/paste-2-server.log" })])} />
      <button className="rm" onClick={() => attach.remove(0)} />
      <button className="clear" onClick={() => attach.clear()} />
      {/* The send shape: release on press, put back if the session refuses it (MirrorView.send). */}
      <button
        className="fail-send"
        onClick={() => {
          const staged = attach.items;
          attach.clear();
          setTimeout(() => attach.revive(staged), 0);
        }}
      />
    </div>
  );
}

let root: Root | null = null;
let host: HTMLDivElement;

const chips = () => [...document.querySelectorAll("li")];
const names = () => chips().map((li) => li.getAttribute("data-name"));

// settle flushes the store's async work (open + request) as well as React's.
async function settle(): Promise<void> {
  for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}

async function mount(dkey: string | null = "k"): Promise<void> {
  root = createRoot(host);
  await act(async () => root!.render(<Composer dkey={dkey} />));
  await settle();
}

async function click(sel: string): Promise<void> {
  const el = document.querySelector(sel)!;
  await act(async () => el.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  await settle();
}

// Close and reopen (✕ / Esc / another pane taking the tab): the component unmounts, so
// anything still on screen afterwards came back out of the store.
async function reopen(dkey: string | null = "k"): Promise<void> {
  await act(async () => root?.unmount());
  await mount(dkey);
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  globalThis.indexedDB = new IDBFactory();
  resetAttachDraftDB();
  host = document.createElement("div");
  document.body.appendChild(host);
});

afterEach(async () => {
  await act(async () => root?.unmount());
  root = null;
  host.remove();
  vi.restoreAllMocks();
  delete (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT;
});

describe("useAttachDraft", () => {
  it("brings a staged file back after a close and reopen", async () => {
    await mount();
    await click(".add");
    expect(names()).toEqual(["shot.png"]);
    await reopen();
    expect(names()).toEqual(["shot.png"]);
    // Restored with its bytes (it still has to be uploadable) and a fresh preview URL.
    expect(chips()[0].getAttribute("data-has-file")).toBe("1");
    expect(chips()[0].getAttribute("data-url")).toMatch(/^blob:/);
  });

  it("brings an uploaded attachment back with the path the prompt will reference", async () => {
    await mount();
    await click(".add-uploaded");
    await reopen();
    expect(names()).toEqual(["paste-2-server.log"]);
    expect(chips()[0].getAttribute("data-path")).toBe("/p/paste-2-server.log");
  });

  it("forgets one the user removed, and forgets everything on clear (sent / launched)", async () => {
    await mount();
    await click(".add");
    await click(".add-uploaded");
    await click(".rm");
    await reopen();
    expect(names()).toEqual(["paste-2-server.log"]);
    await click(".clear");
    await reopen();
    expect(names()).toEqual([]);
  });

  it("does not eat the draft when the view is closed before it finished loading", async () => {
    // Hydration is async, so the write-through fires first with the empty initial state.
    // Unguarded that deletes the record it is in the middle of reading — usually papered
    // over by the re-write once the load lands, but a tab switched away in that window
    // (or a pane closed right after opening) never gets there and the draft is gone.
    await writeAttachDraft("k", [{ name: "seed.png", type: "image/png", image: true, path: "", file: new File(["S"], "seed.png", { type: "image/png" }) }]);
    root = createRoot(host);
    await act(async () => root!.render(<Composer dkey="k" />)); // hydration still in flight
    await act(async () => root!.unmount());
    root = null;
    await settle();
    await mount();
    expect(names()).toEqual(["seed.png"]);
  });

  it("keeps a file staged while the draft was still loading", async () => {
    await writeAttachDraft("k", [{ name: "seed.png", type: "image/png", image: true, path: "", file: new File(["S"], "seed.png", { type: "image/png" }) }]);
    root = createRoot(host);
    await act(async () => root!.render(<Composer dkey="k" />)); // no settle: hydration in flight
    await click(".add");
    expect(names()).toEqual(["seed.png", "shot.png"]);
    await reopen();
    expect(names()).toEqual(["seed.png", "shot.png"]);
  });

  it("writes down a file staged while an EMPTY draft was still loading", async () => {
    // Nothing to restore, so the load changes no state: unless finishing it re-runs the
    // write-through, the file sits on screen having never been written down, and closing
    // the dialog throws it away exactly like before this feature existed.
    root = createRoot(host);
    await act(async () => root!.render(<Composer dkey="k" />)); // hydration in flight
    await click(".add");
    await reopen();
    expect(names()).toEqual(["shot.png"]);
  });

  it("switches drafts with the key instead of carrying one session's files into another", async () => {
    await mount("a");
    await click(".add");
    // The mirror doesn't remount on a session switch — the key changes underneath it.
    await act(async () => root!.render(<Composer dkey="b" />));
    await settle();
    expect(names()).toEqual([]);
    await click(".add-uploaded");
    expect(names()).toEqual(["paste-2-server.log"]);
    await act(async () => root!.render(<Composer dkey="a" />));
    await settle();
    expect(names()).toEqual(["shot.png"]);
  });

  it("releases a preview URL as soon as its chip leaves the list", async () => {
    const revoke = vi.spyOn(URL, "revokeObjectURL");
    await mount();
    await click(".add");
    const url = chips()[0].getAttribute("data-url");
    await click(".rm");
    expect(revoke).toHaveBeenCalledWith(url);
    await click(".add");
    const kept = chips()[0].getAttribute("data-url");
    await act(async () => root?.unmount());
    root = null;
    expect(revoke).toHaveBeenCalledWith(kept);
  });

  it("puts the files back when the session refused the turn", async () => {
    const revoke = vi.spyOn(URL, "revokeObjectURL");
    await mount();
    await click(".add");
    const before = chips()[0].getAttribute("data-url");
    await click(".fail-send");
    expect(names()).toEqual(["shot.png"]);
    // Putting one back mints a fresh URL: the old one was revoked on release, and reusing it
    // leaves a broken image.
    expect(revoke).toHaveBeenCalledWith(before);
    expect(chips()[0].getAttribute("data-url")).not.toBe(before);
    expect(chips()[0].getAttribute("data-has-file")).toBe("1");
    await reopen(); // what was put back is written to the draft too
    expect(names()).toEqual(["shot.png"]);
  });

  it("does not duplicate one the user staged again while the send was failing", async () => {
    await mount();
    await click(".add-uploaded");
    const el = document.querySelector(".fail-send")!;
    await act(async () => {
      el.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      document.querySelector(".add-uploaded")!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await settle();
    expect(names()).toEqual(["paste-2-server.log"]);
  });

  it("stays usable with no key at all (a repo-less launch): staged, but not persisted", async () => {
    await mount(null);
    await click(".add");
    expect(names()).toEqual(["shot.png"]);
    await reopen(null);
    expect(names()).toEqual([]);
  });
});
