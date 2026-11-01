// The notification center's row headings must translate every kind wording.ts handles.
//
// Damage: the three handoff kinds (docs/log/77) and carried-interaction had branches in wording()
// but were missing from the heading table, so the center showed the raw identifier
// `handoff-offer` in either locale. The types cannot catch it (the table is
// Record<string, MsgKey> while the kinds are string-literal branches), so the kinds are scraped
// out of wording.ts's *source* and compared. Add a branch, forget the translation, this fails.
import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";
import { NOTIFICATION_KIND_LABELS, notificationKindLabel, notificationRowSubtitle, notificationWording } from "./wording.ts";
import { setLocale } from "../../lib/i18n/index.ts";
import { ja } from "../../lib/i18n/locales/ja.ts";
import { en } from "../../lib/i18n/locales/en.ts";

/** Collect the kinds appearing in wording()'s `n.kind === "…"` branches from its source. */
function kindsInWording(): string[] {
  const src = readFileSync(path.resolve(__dirname, "wording.ts"), "utf8");
  const out = new Set<string>();
  for (const m of src.matchAll(/n\.kind === "([a-z-]+)"/g)) out.add(m[1]);
  return [...out];
}

describe("notification row headings", () => {
  it("every kind wording() handles has a translation", () => {
    const kinds = kindsInWording();
    // If the scrape stops matching (the code was written differently) the check would silently
    // measure nothing, so assert a lower bound too.
    expect(kinds.length).toBeGreaterThan(10);
    expect(kinds.filter((k) => !NOTIFICATION_KIND_LABELS[k])).toEqual([]);
  });

  it("the table's translation keys exist in both ja and en", () => {
    for (const key of Object.values(NOTIFICATION_KIND_LABELS)) {
      expect(ja[key], key).toBeTruthy();
      expect(en[key as keyof typeof en], key).toBeTruthy();
    }
  });

  it("a handoff notification is not shown as a raw identifier", () => {
    setLocale("ja");
    expect(notificationKindLabel("handoff-offer")).toBe("引き継ぎが届きました");
    setLocale("en");
    expect(notificationKindLabel("handoff-offer")).toBe("A handoff arrived");
    setLocale("ja");
  });

  it("an unknown kind falls back to the identifier (new CP, old Console)", () => {
    expect(notificationKindLabel("brand-new-kind")).toBe("brand-new-kind");
  });

  it("a handoff does not fall through to the usage-reset wording (the last-branch mix-up)", () => {
    setLocale("ja");
    const w = notificationWording({ kind: "handoff-offer", displayName: "残作業の続き", payload: {} });
    expect(w.title).toBe(ja["notif.handoff_offer.title"]);
    expect(w.body).toBe("残作業の続き");
  });

  // #2058: a session the recipient's Agent reported after they gave the handoff back. The body
  // must name that session, since the offer it came from was left unchanged.
  it("a session after a give-back names the session and the handoff", () => {
    setLocale("en");
    const w = notificationWording({ kind: "handoff-late-session", displayName: "Carry on", payload: { sessionName: "s-1" } });
    expect(w.title).toBe(en["notif.handoff_late_session.title"]);
    expect(w.body).toContain("s-1");
    expect(w.body).toContain("Carry on");
    expect(notificationKindLabel("handoff-late-session")).toBe(en["noti.kind_handoff_late_session"]);
    setLocale("ja");
  });

  // Notification about residue an architecture change could not restore automatically
  // (docs/decisions/0068). Its subject is the workspace, not a session, so displayName is empty:
  // taking the default "body = displayName" would produce a notification with no content. The row
  // must show the payload's contents themselves.
  it("architecture residue puts the payload subject in the row (displayName is empty)", () => {
    setLocale("ja");
    const w = notificationWording({
      kind: "arch-residue",
      displayName: "",
      payload: { from: "amd64", repos: ["demo/node_modules"], bins: ["mytool"] },
    });
    expect(w.title).toBe(ja["notif.arch_residue.title"]);
    expect(w.body).toBe("demo/node_modules, mytool");
    // It must not fall through to the usage-reset wording at the end (the trap schedule-* hit).
    expect(w.body).not.toContain("上限");
    expect(w.speech).toBe(ja["notif.arch_residue.speech"]);
  });

  it("architecture residue with an empty payload still has a body", () => {
    setLocale("ja");
    const w = notificationWording({ kind: "arch-residue", displayName: "", payload: {} });
    expect(w.body).toBe(ja["notif.arch_residue.body_generic"]);
  });

  // Measured on a real workspace (2026-09-07): the row rendered as the heading and a timestamp
  // and nothing else, because the subtitle defaults to displayName and this kind has none. The
  // row is the only place the list can appear — clicking opens nothing by design — so an empty
  // subtitle means the member is told something needs reinstalling and never told what.
  it("the architecture-residue ROW names what has to be reinstalled", () => {
    setLocale("ja");
    const sub = notificationRowSubtitle({
      kind: "arch-residue",
      displayName: "",
      payload: { from: "amd64", repos: ["demo/node_modules"], bins: ["mytool"] },
    });
    expect(sub).toBe("demo/node_modules, mytool");
  });

  // The Agent memory switch's own run left files to look at (#1972). The workspace is the
  // subject, so the row's body has to carry the counts, and each kind of run has its own title.
  it("a memory-sync run words its kind and counts on the notification and the row", () => {
    setLocale("ja");
    const payload = { run: "write_back", leftovers: 3, conflicts: 2, secrets: 1 };
    const w = notificationWording({ kind: "memory-sync", displayName: "", payload });
    expect(w.title).toBe(ja["notif.memory_sync.write_back.title"]);
    expect(w.body).toBe("確認が必要なファイルが 3 件あります（競合 2 件、秘密情報あり 1 件）。設定 > エージェントメモリ > 設定 に一覧があります。");
    expect(notificationRowSubtitle({ kind: "memory-sync", displayName: "", payload })).toBe(w.body);
    expect(notificationWording({ kind: "memory-sync", displayName: "", payload: { run: "catch_up" } }).title).toBe(
      ja["notif.memory_sync.catch_up.title"],
    );
    expect(notificationWording({ kind: "memory-sync", displayName: "", payload: {} }).title).toBe(ja["notif.memory_sync.import.title"]);
    expect(notificationKindLabel("memory-sync")).toBe(ja["noti.kind_memory_sync"]);
  });

  // The start deadline stopped a launch that never finished (#1298). The workspace is the
  // subject, so displayName is empty, and the last phase is the only record of why: on ecs-ec2
  // the stop clears it from the workspace itself. Row and OS notification must both carry it.
  it("a start-deadline stop names the limit and the last phase on the notification and the row", () => {
    setLocale("en");
    const n = {
      kind: "start-deadline",
      displayName: "",
      payload: { limitMinutes: 30, phase: "blocked: no container instance met all of its requirements" },
    };
    const w = notificationWording(n);
    expect(w.title).toBe(en["notif.start_deadline.title"]);
    expect(w.body).toBe(
      `It was still starting after 30 min. Last step: ${en["wsstart.blocked"]} — blocked: no container instance met all of its requirements`,
    );
    expect(w.speech).toBe(en["notif.start_deadline.speech"]);
    expect(notificationRowSubtitle(n)).toBe(w.body);
    expect(notificationKindLabel("start-deadline")).toBe(en["noti.kind_start_deadline"]);
    setLocale("ja");
  });

  // The phase is worded the way the starting dialog words it; the member reading the
  // notification in Japanese must not be left with "slot: creating" alone.
  it("a start-deadline stop words a known phase, keeps the raw text, and passes an unknown one through", () => {
    for (const [loc, cat] of [["ja", ja], ["en", en]] as const) {
      setLocale(loc);
      const body = (phase: string) =>
        notificationWording({ kind: "start-deadline", displayName: "", payload: { limitMinutes: 30, phase } }).body;
      const limit = cat["notif.start_deadline.body_limit"].replace("{minutes}", "30");
      const sep = loc === "ja" ? "。最後の段階：" : ". Last step: ";
      expect(body("slot: creating")).toBe(`${limit}${sep}${cat["wsstart.slot_creating"]} — slot: creating`);
      expect(body("blocked: no container instance")).toBe(`${limit}${sep}${cat["wsstart.blocked"]} — blocked: no container instance`);
      expect(body("task: pending")).toBe(`${limit}${sep}task: pending`);
      expect(body("")).toBe(limit);
    }
    setLocale("ja");
  });

  it("a start-deadline stop without a phase still says why", () => {
    setLocale("ja");
    const w = notificationWording({ kind: "start-deadline", displayName: "", payload: { limitMinutes: 30, phase: "" } });
    expect(w.body).toBe("30 分たっても起動中のままでした");
    expect(notificationWording({ kind: "start-deadline", displayName: "", payload: {} }).body).toBe(
      ja["notif.start_deadline.body_generic"],
    );
  });

  it("a kind that does have a session keeps displayName as its subtitle", () => {
    setLocale("ja");
    expect(notificationRowSubtitle({ kind: "answer-ready", displayName: "プラン検証", payload: {} })).toBe("プラン検証");
  });
});

// A program in the session raised an OSC 9 / 99 / 777 desktop notification (#1058). Its text is
// the whole message: without it on the row and in the OS notification, the reader only learns
// that *something* happened.
describe("terminal notification", () => {
  it("carries the program's text on the notification and the row", () => {
    setLocale("ja");
    const n = { kind: "terminal-notification", displayName: "ビルド係", payload: { proto: "osc777", title: "Build", body: "done" } };
    const w = notificationWording(n);
    expect(w.title).toBe("ビルド係 からの通知");
    expect(w.body).toBe("Build — done");
    expect(notificationRowSubtitle(n)).toBe("ビルド係 — Build — done");
    expect(notificationKindLabel("terminal-notification")).toBe("端末からの通知");
  });

  it("falls back to the session name when the payload has no text", () => {
    setLocale("en");
    const n = { kind: "terminal-notification", displayName: "builder", payload: { proto: "osc9" } };
    const w = notificationWording(n);
    expect(w.body).toBe("builder");
    expect(w.speech).toBe("builder sent a notification.");
    expect(notificationRowSubtitle(n)).toBe("builder");
    setLocale("ja");
  });
});

// #2427: an admin hears about a member's home or tool install; the row names the member, never a
// raw identifier, in both locales.
describe("member admin notices", () => {
  it("member-home-full names the member and the figures", () => {
    setLocale("en");
    const w = notificationWording({
      kind: "member-home-full", displayName: "w@acme.co.jp", payload: { used: 95 * 1024 ** 3, total: 100 * 1024 ** 3 },
    });
    expect(w.title).toBe("w@acme.co.jp's home is nearly full");
    expect(w.body).toBe("w@acme.co.jp: 95.0 GiB of 100.0 GiB used");
    expect(notificationKindLabel("member-home-full")).toBe(en["noti.kind_member_home_full"]);
    setLocale("ja");
    expect(notificationWording({ kind: "member-home-full", displayName: "山田", payload: {} }).title).toBe("山田 さんのホームの空きが少なくなっています");
    expect(notificationKindLabel("member-home-full")).toBe(ja["noti.kind_member_home_full"]);
  });

  it("member-agent-silent names the member in both languages and is a plain row", () => {
    setLocale("en");
    const n = { kind: "member-agent-silent", displayName: "w@acme.co.jp", payload: {} };
    expect(notificationWording(n).title).toBe("w@acme.co.jp's workspace is not responding");
    expect(notificationRowSubtitle(n)).toContain("w@acme.co.jp");
    setLocale("ja");
    expect(notificationWording(n).title).toBe("w@acme.co.jp さんのワークスペースが応答していません");
    expect(notificationKindLabel("member-agent-silent")).toBe(ja["noti.kind_member_agent_silent"]);
  });

  it("member-boot-install lists the tools", () => {
    setLocale("en");
    const w = notificationWording({ kind: "member-boot-install", displayName: "w@acme.co.jp", payload: { tools: ["claude", "codex"] } });
    expect(w.body).toBe("w@acme.co.jp: claude, codex");
    expect(notificationRowSubtitle({ kind: "member-boot-install", displayName: "w@acme.co.jp", payload: { tools: ["claude"] } })).toBe("w@acme.co.jp: claude");
  });
});

// A session's PR became ready to merge, or was merged or closed (#3131). The row and the OS
// notification name the PR, since one session can have opened several.
describe("pull request notifications", () => {
  it("names the session and the PR on the notification and the row", () => {
    setLocale("ja");
    const n = { kind: "pr-merged", displayName: "Bitbucket 対応", payload: { provider: "github", key: "o/r#7", number: 7 } };
    const w = notificationWording(n);
    expect(w.title).toBe("プルリクエストがマージされました");
    expect(w.body).toBe("Bitbucket 対応 — o/r#7");
    expect(w.speech).toBe("Bitbucket 対応 のプルリクエストがマージされました。");
    expect(notificationRowSubtitle(n)).toBe("Bitbucket 対応 — o/r#7");
    expect(notificationKindLabel("pr-ready")).toBe("マージできます");
  });

  it("words each kind apart, and falls back to the session without a key", () => {
    setLocale("en");
    expect(notificationWording({ kind: "pr-ready", displayName: "a", payload: { key: "o/r#1" } }).title).toBe("A pull request is ready to merge");
    expect(notificationWording({ kind: "pr-closed", displayName: "a", payload: {} }).speech).toBe("a's pull request was closed without merging.");
    expect(notificationRowSubtitle({ kind: "pr-closed", displayName: "a", payload: {} })).toBe("a");
    for (const k of ["pr-ready", "pr-merged", "pr-closed"]) expect(NOTIFICATION_KIND_LABELS[k]).toBeTruthy();
    setLocale("ja");
  });
});
