// Strings that tell the member to press a button or open a tab by name must quote the label the
// Console actually renders. These pin each such string to the current value of the label key it
// names, so renaming the tab or button fails here instead of silently stranding the hint.
import { describe, it, expect } from "vitest";
import { admin as enAdmin } from "./locales/en/admin.ts";
import { admin as jaAdmin } from "./locales/ja/admin.ts";
import { common as enCommon } from "./locales/en/common.ts";
import { common as jaCommon } from "./locales/ja/common.ts";
import { sessions as enSessions } from "./locales/en/sessions.ts";
import { sessions as jaSessions } from "./locales/ja/sessions.ts";
import { tools as enTools } from "./locales/en/tools.ts";
import { tools as jaTools } from "./locales/ja/tools.ts";

const langs = [
  {
    lang: "en",
    admin: enAdmin,
    common: enCommon,
    sessions: enSessions,
    tools: enTools,
  },
  {
    lang: "ja",
    admin: jaAdmin,
    common: jaCommon,
    sessions: jaSessions,
    tools: jaTools,
  },
];

describe.each(langs)(
  "UI label references ($lang)",
  ({ admin, common, sessions, tools }) => {
    // Tenant settings dialog title › rail group › tab (tenantScope.tsx, TenantDialog.tsx).
    const oauthPath = `${admin["tenant.title"]} › ${admin["tenant.group_integrations"]} › ${admin["tenant.tab_git_oauth"]}`;

    it.each([
      "git.oauth_unregistered",
      "git.github_oauth_unconfigured",
      "git.bitbucket_oauth_unconfigured",
    ] as const)("%s names the Git OAuth tab by its on-screen path", (key) => {
      expect(tools[key]).toContain(oauthPath);
    });

    it("onb.clone_start_hint names the action-bar Start button", () => {
      expect(common["onb.clone_start_hint"]).toContain(
        sessions["wsbar.start_here"],
      );
    });

    it("clean.stage2_confirm_body names the cleanup modal's Restore button", () => {
      expect(admin["clean.stage2_confirm_body"]).toContain(
        admin["clean.restore"],
      );
    });
  },
);
