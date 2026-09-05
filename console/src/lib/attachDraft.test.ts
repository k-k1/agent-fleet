// The attachment-draft store (lib/attachDraft): what actually survives a closed dialog or
// a switched-away mirror. The contract worth pinning is not "something came back" but
// WHICH bytes came back and which were deliberately dropped — a chip that restores
// without its file would be sent as a path the agent can't read, and a 30MB PDF copied
// into the browser to draw an icon is waste that only shows up as a quota error later.
import "fake-indexeddb/auto";
import { IDBFactory } from "fake-indexeddb";
import { beforeEach, describe, expect, it } from "vitest";
import { clearAttachDraft, readAttachDraft, resetAttachDraftDB, writeAttachDraft } from "./attachDraft.ts";

const png = (body: string, name = "shot.png"): File => new File([body], name, { type: "image/png" });
const log = (body: string, name = "server.log"): File => new File([body], name, { type: "text/plain" });
const textOf = async (bytes?: ArrayBuffer): Promise<string> => (bytes ? new TextDecoder().decode(bytes) : "");

beforeEach(() => {
  // A fresh database per test, and a fresh connection to it (the module caches one).
  globalThis.indexedDB = new IDBFactory();
  resetAttachDraftDB();
});

describe("attachment drafts", () => {
  it("restores a staged image with its bytes, name and type", async () => {
    await writeAttachDraft("k", [{ name: "shot.png", type: "image/png", image: true, path: "", file: png("PNGBYTES") }]);
    const got = await readAttachDraft("k");
    expect(got).toHaveLength(1);
    expect(got[0].name).toBe("shot.png");
    expect(got[0].type).toBe("image/png");
    expect(got[0].image).toBe(true);
    expect(await textOf(got[0].bytes)).toBe("PNGBYTES");
  });

  it("keeps the bytes of an uploaded image (the thumbnail needs them) but not of an uploaded file", async () => {
    await writeAttachDraft("k", [
      { name: "paste-1.png", type: "image/png", image: true, path: "/p/paste-1.png", file: png("IMG") },
      { name: "paste-2-server.log", type: "text/plain", image: false, path: "/p/paste-2-server.log", file: log("LOG") },
    ]);
    const got = await readAttachDraft("k");
    expect(await textOf(got[0].bytes)).toBe("IMG");
    // The uploaded non-image is already on disk in the session's pasted dir, and its chip
    // is an icon + a name — so the draft keeps the path and drops the copy.
    expect(got[1].bytes).toBeUndefined();
    expect(got[1].path).toBe("/p/paste-2-server.log");
    expect(got[1].name).toBe("paste-2-server.log");
  });

  it("keeps the bytes of a not-yet-uploaded file whatever its type — they are the only copy", async () => {
    // The Start working dialog stages before any session exists, so nothing has a path yet.
    await writeAttachDraft("k", [{ name: "notes.txt", type: "text/plain", image: false, path: "", file: log("NOTES", "notes.txt") }]);
    expect(await textOf((await readAttachDraft("k"))[0].bytes)).toBe("NOTES");
  });

  it("leaves out a file it cannot read rather than storing a chip with no bytes", async () => {
    // The picked file moved or was replaced on disk. Restoring it as a thumbnail-less
    // chip would be a picture of an attachment that then isn't uploaded at launch.
    const gone = { name: "gone.png", type: "image/png", image: true, path: "", file: { arrayBuffer: () => Promise.reject(new Error("gone")) } as unknown as File };
    await writeAttachDraft("k", [gone, { name: "ok.png", type: "image/png", image: true, path: "", file: png("OK", "ok.png") }]);
    expect((await readAttachDraft("k")).map((r) => r.name)).toEqual(["ok.png"]);
    await writeAttachDraft("k", [gone]);
    expect(await readAttachDraft("k")).toEqual([]);
  });

  it("replaces the whole list, and an empty list forgets the draft", async () => {
    await writeAttachDraft("k", [{ name: "a.png", type: "image/png", image: true, path: "", file: png("A", "a.png") }]);
    await writeAttachDraft("k", [{ name: "b.png", type: "image/png", image: true, path: "", file: png("B", "b.png") }]);
    const got = await readAttachDraft("k");
    expect(got.map((r) => r.name)).toEqual(["b.png"]);
    await writeAttachDraft("k", []);
    expect(await readAttachDraft("k")).toEqual([]);
  });

  it("keeps keys apart and clears only the one asked for", async () => {
    await writeAttachDraft("one", [{ name: "a.png", type: "image/png", image: true, path: "", file: png("A", "a.png") }]);
    await writeAttachDraft("two", [{ name: "b.png", type: "image/png", image: true, path: "", file: png("B", "b.png") }]);
    await clearAttachDraft("one");
    expect(await readAttachDraft("one")).toEqual([]);
    expect((await readAttachDraft("two")).map((r) => r.name)).toEqual(["b.png"]);
  });

  it("a null key is a no-op, not a crash (a repo-less launch / an unnamed session)", async () => {
    await writeAttachDraft(null, [{ name: "a.png", type: "image/png", image: true, path: "", file: png("A") }]);
    await clearAttachDraft(null);
    expect(await readAttachDraft(null)).toEqual([]);
  });

  it("a clear that lands mid-write wins — the sent attachments must not come back", async () => {
    // The write reads the file's bytes before it can open its transaction; a send/launch
    // in that window used to be overtaken by the older write and restore what was sent.
    const writing = writeAttachDraft("k", [{ name: "a.png", type: "image/png", image: true, path: "", file: png("A") }]);
    await clearAttachDraft("k");
    await writing;
    expect(await readAttachDraft("k")).toEqual([]);
  });

  it("ignores junk records instead of taking the composer down with them", async () => {
    const db = await new Promise<IDBDatabase>((resolve, reject) => {
      const req = indexedDB.open("af-drafts", 1);
      req.onupgradeneeded = () => req.result.createObjectStore("attachments", { keyPath: "key" });
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error);
    });
    await new Promise<void>((resolve) => {
      const tx = db.transaction("attachments", "readwrite");
      tx.objectStore("attachments").put({
        key: "k",
        at: Date.now(),
        items: [null, { name: 42 }, { name: "ok.png", image: 1, bytes: "not-bytes" }],
      });
      tx.oncomplete = () => resolve();
    });
    db.close();
    resetAttachDraftDB();
    const got = await readAttachDraft("k");
    expect(got).toHaveLength(1);
    expect(got[0]).toEqual({ name: "ok.png", type: "", image: true, path: "", bytes: undefined });
  });

  it("prunes a draft nobody came back for, and keeps a fresh one", async () => {
    await writeAttachDraft("old", [{ name: "a.png", type: "image/png", image: true, path: "", file: png("A") }]);
    await writeAttachDraft("new", [{ name: "b.png", type: "image/png", image: true, path: "", file: png("B") }]);
    // Age "old" past the cutoff behind the store's back, then reconnect: pruning runs on
    // the first open of a page session.
    const db = await new Promise<IDBDatabase>((resolve) => {
      const req = indexedDB.open("af-drafts", 1);
      req.onsuccess = () => resolve(req.result);
    });
    await new Promise<void>((resolve) => {
      const tx = db.transaction("attachments", "readwrite");
      const store = tx.objectStore("attachments");
      const get = store.get("old");
      get.onsuccess = () => store.put({ ...get.result, at: Date.now() - 15 * 24 * 60 * 60 * 1000 });
      tx.oncomplete = () => resolve();
    });
    db.close();
    resetAttachDraftDB();
    expect(await readAttachDraft("old")).toEqual([]);
    expect((await readAttachDraft("new")).map((r) => r.name)).toEqual(["b.png"]);
  });
});
