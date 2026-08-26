import { describe, expect, it } from "vitest";
import { phaseKey } from "./WsStartingDialog.tsx";
import { ja } from "../lib/i18n/locales/ja.ts";
import { en } from "../lib/i18n/locales/en.ts";

// The dialog's whole job is to name the wait the user is actually in. On the EC2 pool
// runtime the first minutes are a new machine and a new home disk, and calling that
// "installing agent CLIs" is how a normal start reads as stuck (ADR 0045 / docs/64).
describe("phaseKey", () => {
  it("names the infrastructure waits of the EC2 pool runtime", () => {
    // ⚠️ 「片付けてから作る」は起動の中でいちばん長い経路（上限に張り付いたプールが、
    // この人が乗れない大きさの箱で埋まっている）。ここを generic に落とすと、
    // **最長の待ちだけが理由を名乗らない**ことになる。
    expect(phaseKey("slot: making room")).toBe("wsstart.slot_making_room");
    expect(phaseKey("slot: creating")).toBe("wsstart.slot_creating");
    expect(phaseKey("slot: waking")).toBe("wsstart.slot_waking");
    expect(phaseKey("slot: booting")).toBe("wsstart.slot_booting");
    expect(phaseKey("slot: joining the cluster")).toBe("wsstart.slot_booting");
    expect(phaseKey("home: creating")).toBe("wsstart.home_creating");
    expect(phaseKey("home: restoring")).toBe("wsstart.home_restoring");
    expect(phaseKey("home: attaching")).toBe("wsstart.home_attaching");
    expect(phaseKey("home: mounting")).toBe("wsstart.home_attaching");
  });

  it("still names the native rootfs boot-install waits", () => {
    expect(phaseKey("boot-install (pinned): claude")).toBe("wsstart.installing_clis");
    expect(phaseKey("boot-install rtk")).toBe("wsstart.fetching_tool");
    expect(phaseKey("install-jdk 21")).toBe("wsstart.toolchain");
  });

  // The complaint that produced this: the fallback claimed "the first start installs
  // agent CLIs" and appeared on a RESTART, where nothing is installed.
  it("does not let the fallback line name a cause it cannot know", () => {
    for (const s of [ja["wsstart.generic"], en["wsstart.generic"]]) {
      expect(s).not.toMatch(/CLI/i);
      expect(s).not.toMatch(/初回|first start/i);
    }
  });

  it("falls back to the generic line for anything it does not know", () => {
    expect(phaseKey("task: starting")).toBe("wsstart.generic");
    expect(phaseKey("something new the CP grew later")).toBe("wsstart.generic");
  });

  it("has both languages for every key it can return", () => {
    const keys = [
      "wsstart.slot_creating", "wsstart.slot_waking", "wsstart.slot_booting",
      "wsstart.home_creating", "wsstart.home_restoring", "wsstart.home_attaching",
    ] as const;
    for (const k of keys) {
      expect(ja[k], `ja is missing ${k}`).toBeTruthy();
      expect(en[k], `en is missing ${k}`).toBeTruthy();
    }
  });
});
