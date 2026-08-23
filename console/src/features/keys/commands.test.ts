// Guards the real keyboard registry (ALL_COMMANDS / GROUPS) against the class of bug a
// merge introduced once: two commands bound to the same leader key (memo.add vs the voice
// toggle both on "m"), where assignFromSeq's first-match-wins silently shadows one. These
// invariants encode the Command/Group contract from registry.ts ("the first key is the
// group id; a single-key seq is a top-level action"), so a future collision fails CI
// instead of quietly killing a shortcut.
//
// commands.ts transitively imports app stores that read localStorage at module load, and
// the vitest env is bare node (no DOM). A tiny in-memory shim (installed before the dynamic
// import below) lets us exercise the REAL registry rather than a hand-copied sample.
import { describe, it, expect, beforeAll } from "vitest";
import type { Command, Group } from "../../lib/keys/registry.ts";
import { canonical } from "../../lib/keys/chords.ts";

class MemStorage {
  private m = new Map<string, string>();
  get length() {
    return this.m.size;
  }
  getItem(k: string) {
    return this.m.has(k) ? this.m.get(k)! : null;
  }
  setItem(k: string, v: string) {
    this.m.set(k, String(v));
  }
  removeItem(k: string) {
    this.m.delete(k);
  }
  clear() {
    this.m.clear();
  }
  key(i: number) {
    return [...this.m.keys()][i] ?? null;
  }
}
const g = globalThis as unknown as { localStorage?: Storage; window?: unknown };
g.localStorage ??= new MemStorage() as unknown as Storage;
// client.ts binds window.fetch at module load; point window at globalThis (Node has a
// global fetch, and no request is ever made — we only read the command DATA).
g.window ??= globalThis;

let ALL_COMMANDS: Command[];
let GROUPS: Group[];
let APP_DEFAULTS: Record<string, string>;
beforeAll(async () => {
  const mod = await import("./commands.ts");
  ALL_COMMANDS = mod.ALL_COMMANDS;
  GROUPS = mod.GROUPS;
  // bindings.ts also touches localStorage-backed settings at import time — load it after
  // the shim, like commands.ts.
  APP_DEFAULTS = (await import("./bindings.ts")).APP_DEFAULTS;
});

const dupes = <T>(xs: T[]): T[] => [...new Set(xs.filter((x, i) => xs.indexOf(x) !== i))];

describe("keyboard command registry invariants", () => {
  it("has no duplicate leader sequences (a dup shadows one command — first match wins)", () => {
    const seqs = ALL_COMMANDS.map((c) => c.seq).filter((s): s is string => !!s);
    expect(dupes(seqs)).toEqual([]);
  });

  it("has no duplicate direct-accelerator keys", () => {
    // Canonicalize first: the dispatcher matches canonical chords, so "Mod+B" and
    // "mod+b" ARE the same binding even though a raw string compare says otherwise.
    const keys = ALL_COMMANDS.flatMap((c) => c.keys ?? []).map(canonical);
    expect(dupes(keys)).toEqual([]);
  });

  it("keeps direct keys clear of the reserved app chords (leader / palette / cheat-sheet)", () => {
    // The three dispatcher-level chords (APP_DEFAULTS) are matched before commands —
    // a command squatting on one would silently never fire.
    const reserved = new Set(Object.values(APP_DEFAULTS).map(canonical));
    const clashes = ALL_COMMANDS.flatMap((c) => c.keys ?? [])
      .map(canonical)
      .filter((k) => reserved.has(k));
    expect(clashes).toEqual([]);
  });

  it("keeps direct keys off the chords the browser/OS owns (we'd never receive them)", () => {
    // Alt is our accelerator namespace, but a handful of Alt chords never reach the page:
    // Alt+D = address bar and Alt+E/F = the Chrome menu (Firefox's menu mnemonics add
    // E/F/V/S/B/T/H on Windows/Linux), Alt+←/→/Home = browser history navigation.
    // Anything listed here would look bound in the cheat-sheet and silently do nothing.
    const reserved = ["alt+d", "alt+e", "alt+f", "alt+arrowleft", "alt+arrowright", "alt+home"].map(canonical);
    const clashes = ALL_COMMANDS.flatMap((c) => c.keys ?? [])
      .map(canonical)
      .filter((k) => reserved.includes(k));
    expect(clashes).toEqual([]);
  });

  it("keeps group ids and single-key sequences disjoint (a key can't be both a group and an action)", () => {
    const groupIds = new Set(GROUPS.map((g) => g.id));
    const singleKeySeqs = ALL_COMMANDS.map((c) => c.seq).filter((s): s is string => !!s && !s.includes(" "));
    expect(singleKeySeqs.filter((s) => groupIds.has(s))).toEqual([]);
  });

  it("routes every multi-key sequence through a declared group (else which-key shows no heading)", () => {
    const groupIds = new Set(GROUPS.map((g) => g.id));
    const orphans = ALL_COMMANDS.map((c) => c.seq)
      .filter((s): s is string => !!s && s.includes(" "))
      .filter((s) => !groupIds.has(s.split(" ")[0]));
    expect(orphans).toEqual([]);
  });

  it("declares no empty group (every GROUPS entry owns at least one command)", () => {
    const usedPrefixes = new Set(
      ALL_COMMANDS.map((c) => c.seq)
        .filter((s): s is string => !!s && s.includes(" "))
        .map((s) => s.split(" ")[0]),
    );
    expect(GROUPS.map((g) => g.id).filter((id) => !usedPrefixes.has(id))).toEqual([]);
  });

  it("has unique command ids", () => {
    expect(dupes(ALL_COMMANDS.map((c) => c.id))).toEqual([]);
  });
});
