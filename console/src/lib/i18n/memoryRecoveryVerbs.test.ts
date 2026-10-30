// Each recovery verb of the two memory entries names one action in one place:
//   "Restore this point"  — Memory snapshots (a rollback to a snapshot) only
//   "Revert this change"  — Agent memory > Changes
//   "Unforget"            — Agent memory > Memories, for a forgotten memory
//   "Forget this memory", "Purge history…"
// A string that borrowed a verb from another place (Unforget's secret hint once told the member to
// "Restore it from the Changes view") sends them looking for a button that does not exist.
import { describe, it, expect } from "vitest";
import { memory as en } from "./locales/en/memory.ts";
import { memory as ja } from "./locales/ja/memory.ts";
import { errors as enErr } from "./locales/en/errors.ts";
import { errors as jaErr } from "./locales/ja/errors.ts";

const memKeys = (m: object) => Object.keys(m).filter((k) => k.startsWith("mem."));

// Keys that belong to the snapshot restore (or to the backups taken so it can be undone), where the
// word "restore" / 復元 / 戻す is the right one.
const SNAPSHOT_RESTORE = /^mem\.(restore|scope_pick|trigger_(pre_)?restore|ce_intro|bi_intro)/;

describe("memory recovery verbs", () => {
  it("labels the forgotten-memory action Unforget / 忘却を取り消す", () => {
    expect(en["mem.br_unforget"]).toBe("Unforget");
    expect(ja["mem.br_unforget"]).toBe("忘却を取り消す");
    for (const m of [en, ja] as Record<string, string | undefined>[]) {
      expect(m["mem.br_restore"]).toBeUndefined();
      expect(m["mem.br_restored"]).toBeUndefined();
      expect(m["mem.br_restore_secret"]).toBeUndefined();
    }
  });

  it("uses Restore only for snapshots in the mem.* namespace (en)", () => {
    const e: Record<string, string> = en;
    const stray = memKeys(en).filter((k) => /\brestor/i.test(e[k]) && !SNAPSHOT_RESTORE.test(k));
    expect(stray).toEqual([]);
  });

  it("uses 復元 only for snapshots in the mem.* namespace (ja)", () => {
    const j: Record<string, string> = ja;
    const stray = memKeys(ja).filter((k) => /復元/.test(j[k]) && !SNAPSHOT_RESTORE.test(k));
    expect(stray).toEqual([]);
  });

  it("does not leave the English verb 'revert' in ja strings", () => {
    const j: Record<string, string> = ja;
    expect(memKeys(ja).filter((k) => /revert/i.test(j[k]))).toEqual([]);
  });

  it("points the secret hint at the button the Changes view has", () => {
    expect(en["mem.br_unforget_secret"]).toContain(en["mem.af_revert"]);
    expect(ja["mem.br_unforget_secret"]).toContain(ja["mem.af_revert"]);
  });

  it("keeps the snapshot restore and the Changes revert distinct", () => {
    expect(en["mem.restore"]).toBe("Restore this point");
    expect(en["mem.af_revert"]).toBe("Revert this change");
    expect(en["mem.pg_button"]).toMatch(/^Purge history/);
    expect(enErr["err.memory_restore_failed"]).toBe("The restore failed.");
    expect(jaErr["err.memory_restore_failed"]).toBeTruthy();
  });
});
