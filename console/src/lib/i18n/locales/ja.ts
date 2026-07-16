// 日本語カタログ（ソース・正本）。キーは他ロケールの網羅チェックの基準になる（en.ts は
// Record<keyof typeof ja, string> で全キー必須）。値の {name} 等は t() が vars で置換する。
// docs/28-i18n.md。P0 は中央集約 sink 3 種（errText / notifications wording / 設定ラベル定数）分のみ。
// 以降のフェーズで features 単位に追記していく。
export const ja = {
  // --- 共通 / 設定ラベル ---
  "common.just_now": "たった今",
  "settings.language": "言語",
  "theme.dark": "ダーク",
  "theme.light": "ライト",
  "region_theme.inherit": "アプリに合わせる",

  // --- API エラー（core/api/client.ts の ERR_TEXT ＋ インライン fallback）---
  // Go 側の安定コード（control-plane/errcodes.go, workspace/agent/errcodes.go）と対。
  "err.ssm_search_forbidden":
    "AWS上のインスタンスを検索する権限がありません。AWS管理者に ssm:DescribeInstanceInformation の付与を依頼してください。",
  "err.quota_sessions":
    "同時に稼働できるセッション数の上限に達しています。稼働中のセッションをどれか停止してから作成してください。",
  "err.sessions_running":
    "この作業コピーでは稼働中のセッションがあります。切り替えると足元の作業ツリーが入れ替わり壊れるため、ここでは切り替えできません。ブランチは別の作業コピーとして開いてください。",
  "err.sessions_running_delete":
    "この作業コピーでは稼働中のセッションがあります。削除すると足元の作業ディレクトリが消えて壊れるため、先にそれらのセッションを停止してください。",
  "err.worktree_dirty":
    "この worktree には未コミット/未pushの変更があります。強制削除すると失われます。",
  "err.has_worktrees":
    "この作業コピーには派生した worktree がぶら下がっています。先に worktree 側を削除してください。",
  "err.worktree_remove_failed": "worktree の削除に失敗しました。",
  "err.question_pending":
    "エージェントが質問への回答を待っています。質問カードから回答してから送信してください。",
  "err.not_running": "セッションが停止しています。再開してから送信してください。",
  "err.driver_unavailable": "このエージェント種別ではまだ managed ドライバを利用できません。",
  "err.runtime_failed": "エージェントの共有 runtime に接続できませんでした。",
  "err.send_failed": "送信に失敗しました",
  "err.network": "通信エラー",
  "err.settings_change_failed": "設定を変更できませんでした",

  // --- 通知（features/notifications/store.ts の wording。speech は読み上げ用の別形）---
  "notif.default_name": "セッション",
  "notif.answer_ready.title": "回答が返ってきました",
  "notif.answer_ready.speech": "{name} の回答が返りました。",
  "notif.question.title": "質問が来ています",
  "notif.question.speech": "{name} が確認を求めています。",
  "notif.plan_approval.title": "プランの承認待ちです",
  "notif.plan_approval.speech": "{name} がプランの承認を求めています。",
  "notif.permission_request.title": "権限の確認が必要です",
  "notif.permission_request.speech": "{name} が権限の確認を求めています。",
  "notif.usage_reset.title": "{source} の制限がリセットされました",
  "notif.usage_reset.body": "{window}がリセットされました。",
  "notif.usage_reset.speech": "{source}の{window}がリセットされました。",
  "notif.window.5h": "5時間枠",
  "notif.window.week": "週間枠",

  // --- キーボード操作体系（features/keys・docs/29）。コマンド/グループ名は表示と
  // コマンドパレットの日英マッチ両方に使う。{n} はペイン序数。---
  "keys.grp.pane": "ペイン / レイアウト",
  "keys.grp.session": "セッション",
  "keys.grp.workspace": "ワークスペース",
  "keys.app.leader": "リーダー（コマンドメニュー）",
  "keys.app.palette": "コマンドパレット",
  "keys.app.cheatsheet": "ショートカット一覧",
  "keys.cmd.paneFocus": "ペイン {n} へフォーカス",
  "keys.cmd.splitRight": "右に分割",
  "keys.cmd.splitDown": "下に分割",
  "keys.cmd.close": "ペインを閉じる",
  "keys.cmd.closeAll": "全ペインを閉じる",
  "keys.cmd.wrap": "行の折り返しを切替",
  "keys.cmd.focusLeft": "左のペインへ",
  "keys.cmd.focusDown": "下のペインへ",
  "keys.cmd.focusUp": "上のペインへ",
  "keys.cmd.focusRight": "右のペインへ",
  "keys.cmd.next": "次のペインへ",
  "keys.cmd.prev": "前のペインへ",
  "keys.cmd.regionNext": "次の領域へ（レール / メイン / バー）",
  "keys.cmd.regionPrev": "前の領域へ",
  "keys.cmd.sessionNew": "新規セッション（起動）",
  "keys.cmd.workspaceToggle": "ワークスペース 起動 / 停止",
  "keys.cmd.toggleRail": "左レールの表示切替",
  "keys.cmd.railMode": "左レールの表示モード切替",
  "keys.cmd.fullscreen": "アプリ全画面の切替",
  "keys.cmd.theme": "テーマ切替（ダーク / ライト）",
  "keys.cmd.settingsOpen": "設定を開く",
  "keys.cmd.paletteOpen": "コマンドパレット",
  "keys.cmd.guideOpen": "はじめかたガイド",
  "keys.cmd.cheatsheet": "キーボードショートカット一覧",
  "keys.palette.placeholder": "コマンド・セッションを検索…",
  "keys.palette.aria": "コマンド・セッションを検索",
  "keys.palette.empty": "該当なし",
  "keys.item.command": "コマンド",
  "keys.item.session": "セッション",
  "keys.cheat.title": "キーボードショートカット",
  "keys.cheat.filter": "絞り込み…",
  "keys.cheat.filterAria": "ショートカットを絞り込み",
  "keys.cheat.empty": "該当なし",
  "keys.cheat.aria": "キーボードショートカット一覧",
  "keys.cheat.secBasics": "基本",
  "keys.cheat.secLeader": "その他（リーダー）",
  "keys.cheat.secDirect": "アクセラレータ（直接キー）",
  "keys.cheat.whichkey": "コマンドメニュー（which-key）",
  "keys.cheat.palette": "コマンドパレット",
  "keys.cheat.cheatsheet": "このショートカット一覧",
  "keys.cheat.region": "領域を移動（レール / メイン / バー）",
  "keys.cheat.close": "閉じる / 戻る",
  "keys.kt.termPrioTitle": "端末入力の優先",
  "keys.kt.termPrioLabel": "端末フォーカス中はアプリより端末を優先",
  "keys.kt.termPrioNote":
    "オンにすると、ターミナルにフォーカスがある間は Ctrl 系のキーをすべて端末（シェル）へ渡します。アプリのショートカットはリーダー（下の「アプリ全体」で変更可）だけが生き、そこからコマンドメニュー／パレットで全操作に到達できます。tmux やエディタを端末内で使うときに便利です。",
  "keys.kt.assignTitle": "ショートカットの割り当て",
  "keys.kt.resetAll": "すべて既定に戻す",
  "keys.kt.assignNote":
    "リーダー配下のシーケンス（例: リーダー → p → r）は構造上の操作なので変更できません。ここでは直接キー（Alt+1 など）と 3 つのアプリ全体キーを変更できます。「?」でショートカット一覧を確認できます。",
  "keys.kt.secApp": "アプリ全体",
  "keys.kt.secRegion": "領域",
  "keys.kt.secOther": "その他",
  "keys.kt.change": "変更",
  "keys.kt.clear": "解除",
  "keys.kt.default": "既定",
  "keys.kt.cancel": "取消",
  "keys.kt.capture": "キーを押す…",
  "keys.kt.captureHint": "(Esc で取消)",
  "keys.kt.unset": "未設定",
  "keys.kt.conflict": "{names} と重複",
};
