// English catalog — the **composition**. The entries live per domain in ./en/*.ts, each of
// which is typed Record<keyof typeof (the matching ja file), string>, so tsc still fails the
// build on any missing OR extra key. That per-domain check is the real completeness guard:
// a spread does NOT get excess-property checking, so putting the assertion only here would
// silently accept an en key that no longer exists in ja.
//
// ⚠️ Never write a key in this file — see ./ja.ts for why the catalog is split at all.
import type { ja } from "./ja.ts";
import { admin } from "./en/admin.ts";
import { aiassist } from "./en/aiassist.ts";
import { assistant } from "./en/assistant.ts";
import { chat } from "./en/chat.ts";
import { common } from "./en/common.ts";
import { errors } from "./en/errors.ts";
import { memo } from "./en/memo.ts";
import { mirror } from "./en/mirror.ts";
import { notifications } from "./en/notifications.ts";
import { ops } from "./en/ops.ts";
import { repos } from "./en/repos.ts";
import { schedules } from "./en/schedules.ts";
import { sessions } from "./en/sessions.ts";
import { settings } from "./en/settings.ts";
import { sharing } from "./en/sharing.ts";
import { tools } from "./en/tools.ts";
import { usage } from "./en/usage.ts";
import { viewer } from "./en/viewer.ts";
import { workitems } from "./en/workitems.ts";

export const en: Record<keyof typeof ja, string> = {
  ...common,
  ...errors,
  ...settings,
  ...tools,
  ...assistant,
  ...aiassist,
  ...sessions,
  ...mirror,
  ...chat,
  ...repos,
  ...viewer,
  ...admin,
  ...ops,
  ...usage,
  ...notifications,
  ...workitems,
  ...sharing,
  ...schedules,
  ...memo,
};
