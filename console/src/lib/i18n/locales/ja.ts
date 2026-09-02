// 日本語カタログ（ソース・正本）の**合成**。実体はドメイン別に ./ja/*.ts へ分かれている。
// キーは他ロケールの網羅チェックの基準になる（en.ts は Record<keyof typeof ja, string>）。
// 値の {name} 等は t() が vars で置換する。docs/log/28-i18n.md。
//
// なぜ分かれているか（ADR 0067 決定 4）。分割前はここが 4,700 行の 1 ファイルで、文言を
// 足す作業はフロントのほぼ全機能で発生する＝**並列セッションが毎回ここで衝突していた**。
// ドメイン別に切っておけば、各セッションは自分のドメインのファイルにだけ追記する。
//
// ⚠️ このファイルには**キーを 1 つも書かない**。ここに書くと、また全員が触る 1 ファイルに戻る。
// 新しいドメインを足すときだけ import と spread を 1 行ずつ増やす。
// ⚠️ キーの接頭辞とドメインの対応は各ファイル冒頭の「キー接頭辞」に書いてある。既存の
// 接頭辞は必ずその所属ファイルへ足すこと（同じキーが 2 ファイルに在ると後勝ちで無言に化ける）。
import { admin } from "./ja/admin.ts";
import { aiassist } from "./ja/aiassist.ts";
import { assistant } from "./ja/assistant.ts";
import { chat } from "./ja/chat.ts";
import { common } from "./ja/common.ts";
import { errors } from "./ja/errors.ts";
import { memo } from "./ja/memo.ts";
import { mirror } from "./ja/mirror.ts";
import { notifications } from "./ja/notifications.ts";
import { ops } from "./ja/ops.ts";
import { repos } from "./ja/repos.ts";
import { schedules } from "./ja/schedules.ts";
import { sessions } from "./ja/sessions.ts";
import { settings } from "./ja/settings.ts";
import { sharing } from "./ja/sharing.ts";
import { tools } from "./ja/tools.ts";
import { usage } from "./ja/usage.ts";
import { viewer } from "./ja/viewer.ts";
import { workitems } from "./ja/workitems.ts";

export const ja = {
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
