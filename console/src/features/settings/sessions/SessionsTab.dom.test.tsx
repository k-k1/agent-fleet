// Settings > Sessions (ADR 0138 D16): the session block that used to open the Agents page. Every
// row is a local pref the Agent reads through ui-prefs, so the page needs no workspace and the
// move must keep each control writing the same key.
import { describe, it, expect, afterEach } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { SessionsTab } from "./SessionsTab.tsx";
import { setSettings, settingsDefaults, getSettings, STOPPED_ARCHIVE_NEVER } from "../../../lib/settings.ts";
import { t } from "../../../lib/i18n/index.ts";
import { SPEND_CAP_DEFAULTS } from "../../sessions/spendBudget.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<SessionsTab />);
  });
}

const rowOf = (label: string): HTMLElement => {
  const row = Array.from(host!.querySelectorAll<HTMLElement>(".ds-row")).find(
    (r) => r.querySelector(".ds-label")?.textContent === label,
  );
  if (!row) throw new Error("no row " + label);
  return row;
};
const buttonsOf = (label: string) => Array.from(rowOf(label).querySelectorAll<HTMLButtonElement>(".seg-btn"));
const pick = async (label: string, text: string) => {
  const b = buttonsOf(label).find((x) => x.textContent === text);
  if (!b) throw new Error(`no ${text} on ${label}`);
  await act(async () => b.click());
};

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  setSettings(settingsDefaults());
});

describe("停止中セッションのアーカイブ期間（#982）", () => {
  const label = () => t("agents.stopped_archive");
  const active = () => buttonsOf(label()).filter((b) => b.classList.contains("seg-btn--active")).map((b) => b.textContent);

  // "Default" stores 0, not 7: the Agent then defers to the deployment's AF_SESSION_STOPPED_TTL,
  // which a stored 7 would silently override for every user who never touched the row.
  it("未設定なら「既定」が選ばれ、保存値は 0", async () => {
    setSettings(settingsDefaults());
    await mount();
    expect(active()).toEqual([t("agents.stopped_archive_default")]);
    expect(getSettings().sessionStoppedArchiveDays).toBe(0);
  });

  // The Agent applies a value no button produces as Default, so the row has to show Default
  // rather than light nothing.
  it("選択肢に無い保存値は「既定」として表示する", async () => {
    setSettings({ sessionStoppedArchiveDays: 2 });
    await mount();
    expect(active()).toEqual([t("agents.stopped_archive_default")]);
  });

  it("「しない」は -1、日数はその数を保存する", async () => {
    await mount();
    await pick(label(), t("agents.stopped_archive_never"));
    expect(getSettings().sessionStoppedArchiveDays).toBe(STOPPED_ARCHIVE_NEVER);
    const days = buttonsOf(label()).find((b) => b.textContent?.startsWith("14"))!;
    await act(async () => days.click());
    expect(getSettings().sessionStoppedArchiveDays).toBe(14);
  });
});

describe("the session switches write the prefs the Agents page wrote", () => {
  it("peer messaging, steering, PR feedback and session search", async () => {
    setSettings(settingsDefaults());
    await mount();
    for (const [label, key] of [
      ["agents.peer_messaging", "peerMessaging"],
      ["agents.fleet_spawn", "sessionFleetSpawn"],
      ["agents.pr_feedback", "prFeedback"],
      ["agents.session_search", "sessionSearch"],
    ] as const) {
      await pick(t(label), t("common.on"));
      expect(getSettings()[key]).toBe(true);
      await pick(t(label), t("common.off"));
      expect(getSettings()[key]).toBe(false);
    }
  });

  it("shows the child limit only with steering on, and stores the chosen number", async () => {
    setSettings({ ...settingsDefaults(), sessionFleetSpawn: false });
    await mount();
    expect(host!.textContent).not.toContain(t("agents.spawn_child_limit"));
    await pick(t("agents.fleet_spawn"), t("common.on"));
    const other = buttonsOf(t("agents.spawn_child_limit")).find((b) => !b.classList.contains("seg-btn--active"))!;
    await act(async () => other.click());
    expect(getSettings().sessionSpawnChildLimit).toBe(Number(other.textContent));
  });

  it("stores the default spend budget", async () => {
    await mount();
    await act(async () => buttonsOf(t("agents.spend_cap_default"))[1].click());
    expect(getSettings().sessionSpendCapUsd).toBe(SPEND_CAP_DEFAULTS[1]);
    expect(SPEND_CAP_DEFAULTS[1]).toBeGreaterThan(0);
  });
});

// ADR 0108: the Agent Fleet memory switch lives on the Agent memory page (#1735); this page only
// says where it is, beside the other af tool switches.
describe("Agent Fleet memory switch", () => {
  it("is not a toggle here, only a pointer to the Agent memory page", async () => {
    await mount();
    expect(host!.textContent).toContain(t("agents.note_agent_memory_moved"));
    const labels = Array.from(host!.querySelectorAll(".ds-label")).map((l) => l.textContent);
    expect(labels).not.toContain(t("mem.af_switch"));
  });
});
