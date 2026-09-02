// 日本語 カタログ / ドメイン: common
// キー接頭辞: common, ui, theme, region_theme, color, surface, surface_color, font, iconset, out_lang, state, exit, popout, swipe, pane, topbar, wset, onb, wsstart
//
// ⚠️ 追記は**自分のドメインのファイルだけ**に行う（ADR 0067 決定 4）。分割前は 4,700 行の
// 1 ファイルで、フロントの並列セッションが全員ここへ追記＝毎回確実に衝突していた。
// ja が正本。新しいキーはここに足し、同じキーを ../en/ の同名ファイルにも足す。
export const common = {
  // --- 共通 / 設定ラベル ---
  "common.just_now": "たった今",
  "theme.dark": "ダーク",
  "theme.light": "ライト",
  "region_theme.inherit": "アプリに合わせる",

  // --- 共通トグル / フォント名（controls.tsx / lib/settings.ts のフォント配列）---
  "common.on": "オン",
  "common.off": "オフ",
  "common.move_up": "上へ",
  "common.move_down": "下へ",
  "font.sys_mono": "システム等幅",
  "font.sys": "システム",
  "font.serif": "セリフ",
  "font.mincho": "明朝",
  "font.gothic": "ゴシック",
  "font.cjk_auto": "自動",
  "font.cjk_off": "欧文優先",

  // --- アイコンセット（lib/settings.ts ICON_SETS）---
  "iconset.vscode": "VS Code Icons（カラー）",
  "iconset.material": "Material（カラー）",
  "iconset.devicon": "Devicon（カラー）",
  "iconset.seti": "Seti（単色・タイプ別着色）",

  // --- サーフェス色（lib/settings.ts SURFACE_COLORS）---
  "surface_color.default": "デフォルト",
  "surface_color.slate": "スレート",
  "surface_color.blue": "ブルー",
  "surface_color.green": "グリーン",
  "surface_color.purple": "パープル",
  "surface_color.warm": "ウォーム",
  "surface_color.teal": "ティール",
  "surface_color.rose": "ローズ",
  "surface_color.pink": "ピンク",
  "surface_color.indigo": "インディゴ",
  "surface_color.mono": "グラファイト",

  // --- サーフェス対象（lib/settings.ts SURFACE_TARGETS。short=外観ポップ, long=設定行）---
  "surface.topbar.short": "上部バー",
  "surface.topbar.long": "上部バーの背景",
  "surface.leftpane.short": "左ペイン",
  "surface.leftpane.long": "左ペインの背景",
  "surface.viewer.short": "ビューア",
  "surface.viewer.long": "ファイルビューアの背景",
  "surface.session.short": "セッション",
  "surface.session.long": "セッションの背景",
  "surface.shared.short": "共有セッション",
  "surface.shared.long": "共有セッションの背景",
  "surface.assistant.short": "アシスタント",
  "surface.assistant.long": "アシスタントの背景",

  // --- 共通 ---
  "common.close": "閉じる",

  // --- 共通（ロード/起動）---
  "common.loading": "読み込み中…",
  "common.starting": "起動中…",
  // --- 共通（保存）---
  "common.save_failed": "保存に失敗しました",

  // --- 共通（削除/キャンセル）---
  "common.cancel": "キャンセル",
  "common.delete": "削除",
  "common.delete_confirm": "削除する",

  // --- 端末の色（lib/termcolor.ts SSM_HOST_COLORS）---
  "color.auto": "自動",
  "color.red": "レッド",
  "color.orange": "オレンジ",
  "color.yellow": "イエロー",
  "color.green": "グリーン",
  "color.teal": "ティール",
  "color.blue": "ブルー",
  "color.purple": "パープル",
  "color.pink": "ピンク",
  "color.gray": "グレー",

  // --- 共通（既定）---
  "common.default": "既定",

  // --- 共通（保存/戻る）---
  "common.save": "保存",
  "common.back": "戻る",
  "common.save_failed_msg": "保存に失敗: {msg}",

  // --- アシスタント回答言語（lib/settings.ts OUTPUT_LANGUAGES）---
  "out_lang.auto": "入力に合わせる",
  "out_lang.ja": "日本語",
  "out_lang.en": "English",

  // === P2: 複数形インフラの例（tCount）＋ <Trans> の例。ja は単一形なので _one/_other は同値。===
  "common.days_left_one": "あと{count}日",
  "common.days_left_other": "あと{count}日",
  "common.count_ken_one": "{count}件",
  "common.count_ken_other": "{count}件",

  // === P2 共有: セッション状態チップ（lib/sessionview.ts の stateInfo）===
  "state.folder_missing": "フォルダ無し — 再開不可",
  "state.stopped": "停止中",
  "state.stopped_question": "停止中・質問あり",
  "state.stopped_plan": "停止中・承認待ち",
  "state.stopped_permission": "停止中・許可待ち",
  "state.running": "起動中",
  "state.compacting": "圧縮中…",
  "state.working": "進行中…",
  "state.question": "質問あり",
  "state.plan": "プランあり",
  "state.permission": "許可待ち",
  "state.blocked": "上限で停止 — 操作が必要",
  // 利用上限のリセット待ち（docs/log/47 §4-9）。blocked と違い人の操作は要らないので、
  // 「操作が必要」とは言わずに、いつ動くか（予約済みの自動再開時刻）だけを添える。
  // 支出・残高の上限（docs/log/47 §4-10）。待っても解けないので「制限解除待ち」とは別物にする。
  "state.spend_limit": "残高上限 — 増枠が必要",
  "state.rate_limited": "制限解除待ち",
  "state.rate_limited_at": "制限解除待ち · {at}",
  "state.auth_expired": "認証切れ — 再認証が必要",
  "state.idle_bg": "入力待ち · BG実行中",
  // 何が走っているかまで分かったときの文言（backgroundBusyReason）。理由が付かない／
  // 知らない値のときは上の汎用文言に落ちる。
  "state.idle_bg_subagent": "入力待ち · サブエージェント実行中",
  "state.idle_bg_shell": "入力待ち · BGコマンド実行中",
  "state.idle": "入力待ち",

  // === P2 共有: 異常終了ラベル（lib/sessionview.ts の exitLabel。hint はツールチップ）===
  "exit.oom.text": "メモリ不足で終了",
  "exit.oom.hint":
    "メモリ不足でプロセスが強制終了されました（OOM kill / exit {code}）。ワークスペースのメモリ上限に達した可能性があります。",
  "exit.killed.text": "強制終了",
  "exit.killed.hint":
    "プロセスが SIGKILL で強制終了されました（signal {signal}）。ホスト全体のメモリ逼迫などが原因の可能性があります。",
  "exit.crashed.text": "異常終了",
  "exit.crashed.hint_signal": "プロセスが signal {signal} で異常終了しました。",
  "exit.crashed.hint_code": "プロセスが異常終了しました（exit code {code}）。",
  // 起動中ダイアログ（WsStartingDialog・docs/log/35 §35.9-9）
  "wsstart.title": "ワークスペースを起動中",
  "wsstart.generic": "起動しています…",
  "wsstart.blocked": "起動できません。このまま待っても進みません",
  "wsstart.installing_clis": "エージェント CLI を導入中…（初回のみ・数分かかることがあります）",
  "wsstart.fetching_tool": "追加ツールを取得中…",
  "wsstart.toolchain": "ツールチェーンを導入中…",
  "wsstart.slot_making_room": "空いているマシンを片付けて、あなたに合う大きさのものを用意しています…（この経路がいちばん時間がかかります）",
  "wsstart.slot_creating": "実行するマシンを用意しています…（新しく起動するので数分かかります）",
  "wsstart.slot_waking": "マシンを起こしています…",
  "wsstart.slot_booting": "マシンの起動を待っています…",
  "wsstart.home_creating": "home のディスクを作成しています…（初回のみ）",
  "wsstart.home_restoring": "退避してあった home を復元しています…",
  "wsstart.home_attaching": "home のディスクを接続しています…",
  "wsstart.hint": "進捗は agent.log にも記録されます。このダイアログは閉じても起動は続きます。",

  // === P2 TopBar（app/TopBar.tsx）===
  "topbar.nav_toggle": "左パネル: クリックで開閉 / ダブルクリックで表示切替（Push⇄オーバーレイ）",
  "topbar.tts.stop_off": "読み上げを停止して OFF",
  "topbar.tts.on": "音声読み上げ: ON（クリックで OFF）",
  "topbar.tts.off": "音声読み上げ: OFF（クリックで ON）",
  "topbar.tts.generating": "音声を生成中",
  "topbar.tts.speaking": "読み上げ中",
  "topbar.fullscreen_exit": "全画面解除",
  "topbar.fullscreen_enter": "全画面表示",
  "topbar.reload": "再読み込み",
  "topbar.appearance_title": "外観（配置・テーマ・配色）",
  "topbar.appearance": "外観",
  "topbar.appearance_details": "詳細",
  "topbar.appearance_details_title": "表示設定を開く（フォント・詳細な配色など）",
  "topbar.tenant": "テナント",
  "topbar.menu": "メニュー",
  "topbar.user_guide": "利用ガイド",
  "topbar.guide": "はじめかたガイド",
  "topbar.settings": "設定",
  "topbar.tenant_settings": "テナント設定",
  "topbar.admin": "管理",
  "topbar.logout": "ログアウト",
  "topbar.build": "ビルド {label}",
  "topbar.server_version": "サーバー v{v}",
  "topbar.image_cp": "CP イメージ {ref}",
  "topbar.image_ws": "WS イメージ {ref}",
  "topbar.copy_version": "バージョン情報をコピー",
  "topbar.host_version": "Agent Fleet v{v}",
  "topbar.update_ready": "更新あり · v{v} を再起動で適用",
  "topbar.update_badge": "更新",
  "topbar.settings_title": "設定（表示 / ワークスペース / エージェント / Git / AWS SSM / MCP）",

  // === P2 共通の細かな語 ===
  "common.list_sep": "、",

  // === P2 モーダル・行 共通の頻出語（common.cancel/close/delete は既存を再利用）===
  "common.send": "送信",
  "common.delete_do": "削除する",
  "common.delete_failed": "削除に失敗しました",
  "common.send_failed": "送信に失敗しました",
  "common.copy_failed": "コピーに失敗しました",

  // === P2 LayoutMap（features/panes/LayoutMap.tsx）===
  "pane.map_aria": "ペイン配置",
  "pane.layout": "レイアウト",
  "pane.pane_n": "ペイン{ord}",
  "pane.no_session": "セッション未接続",
  "pane.empty": "空き",
  "pane.kind.file": "ファイル",
  "pane.kind.scm": "コミットグラフ",
  "pane.kind.changes": "変更",
  "pane.kind.commit": "コミット",
  "pane.kind.wtdiff": "ファイル差分",
  "pane.kind.doc": "ドキュメント",
  "pane.kind.diff": "差分",
  "pane.kind.chat": "チャット",
  "pane.kind.read": "朗読ビュー",
  "pane.kind.browser": "ブラウザ",
  "pane.kind.browser_attach": "Chromium操作画面",

  // === P2 共通（追加）===
  "common.approx": "約{v}",
  "common.focus_pane": "ペイン{ordinal}にフォーカス",

  // === スマホの ← スワイプ＝稼働中セッションのローテート（app/App.tsx）===
  // 画面が丸ごと入れ替わるので、着地点（何件中の何番目か）を短く返す。
  "swipe.rotated": "{n}/{total} {name}",
  "swipe.rotate_none": "ほかに稼働中のセッションはありません",

  // === P5 共通 ===
  "common.mid_dot": "・",
  // 約物はロケール別（keyHint.ts の hintSuffix と同じ流儀）: ja は全角括弧/コロン、
  // en は半角＋前スペース。件数サフィックスや詳細連結のハードコード全角を置き換える。
  "common.paren": "（{v}）",
  "common.detail_sep": "：",

  // === P5 オンボーディング/ターミナル（OnboardingCard/TerminalView/term） ===
  "onb.ws_first": "先にワークスペースを起動してください",
  "onb.start_ws": "ワークスペースを起動",
  "onb.start_ws_hint": "あなた専用のコンテナを立ち上げます",
  "onb.starting": "起動中…",
  "onb.start": "起動",
  "onb.connect_agent": "エージェントを接続",
  "onb.connect_agent_hint": "Claude / Codex / opencode のいずれかにサインイン",
  "onb.connect": "接続する",
  "onb.connect_git": "git プロバイダを接続",
  "onb.optional": "任意",
  "onb.connect_git_hint": "private リポジトリをクローン / push するなら接続します",
  "onb.clone_start": "リポジトリをクローンしてセッション開始",
  "onb.clone_start_hint": "クローンと起動は「はじめる」からまとめて行えます",
  "onb.get_started": "はじめる",
  "onb.which_start": "どちらから始めますか？ — あとから両方使えます",
  "onb.tile_chat_title": "AI に質問・翻訳を頼む",
  "onb.tile_chat_desc": "使い捨てのチャット。git もターミナルも不要で、そのまま使えます。",
  "onb.chat_needs_setup": "上の2ステップを済ませると使えます",
  "onb.start_chat": "チャットをはじめる",
  "onb.tile_dev_title": "リポジトリで開発する",
  "onb.tile_dev_desc": "git を接続し、リポジトリをクローンして AI セッションを起動します。",
  "onb.collapse_steps": "手順をたたむ",
  "onb.to_dev_setup": "開発のセットアップへ",
  "onb.welcome": "Agent Fleet へようこそ",
  "onb.welcome_sub": "まず2ステップ。そのあとは目的を選ぶだけです",
  "onb.later": "あとで",
  "onb.guide_title": "はじめかたガイド",
  "onb.guide_sub": "済んだ項目には自動でチェックが付きます",
  "onb.session": "セッション",
  "onb.session_disconnected": "セッション未接続",
  "onb.resuming": "再開中…",
  "onb.resume_this_session": "このセッションを再開",
  "onb.ws_stopped": "ワークスペース停止中",
  "onb.resume": "再開",
  "onb.paste_confirm_title": "ターミナルに貼り付けますか？",
  "onb.paste_chars": "クリップボードの {count} 文字",
  "onb.paste_lines": "（{lines} 行）",
  "onb.paste_suffix": " を貼り付けます。",
  "onb.paste_newline_warn": "改行を含むため、シェルではそのまま実行される場合があります。",
  "onb.paste_confirm": "貼り付け",
  // ターミナルのグリッドへ直接書く切断通知（term.ts）
  "onb.term_disconnected": "[切断されました]",
  "onb.rtt_unit": "ms",
  "onb.rtt_title":
    "端末の往復時間（中央値 {med}ms / 最大 {max}ms / 直近 {n} 回）。\n打鍵と同じ経路・同じフレームで測った、ブラウザ↔ワークスペース間の実測値です（PTY/tmux 自体は 1ms 未満なので、ほぼそのままエコーの遅れになります）。",
  "onb.term_session_stopped": "[このセッションは停止中です — 右下の「再開」で再開できます]",
  "wset.all": "すべて",
  "wset.bar_title": "作業グループで左ペインの表示を絞り込む",
  "wset.manage": "グループを管理…",
  "wset.menu_caption": "作業グループ",
  "wset.manage_title": "作業グループ",
  "wset.new_ph": "新しいグループ名",
  "wset.create": "作成",
  "wset.delete": "グループを削除",
  "wset.delete_title": "作業グループの削除",
  "wset.delete_confirm": "グループ「{name}」を削除しますか？中身（リポジトリ・会話・セッション）は消えません。",
  "wset.empty_hint": "作業グループはまだありません。作成すると、リポジトリ・会話・セッションを案件ごとに割り当てて、左ペインの表示を切り替えられます。",
  "wset.no_repos": "このグループのリポジトリはありません（行の右クリックで追加）",
  "wset.name_aria": "グループ名",
  "wset.row_counts": "リポジトリ {repos} / 会話 {convs} / セッション {sessions} / スケジュール {schedules}",
  "wset.none_hint": "作業グループはまだありません（左ペイン上部から作成）",
  "wset.no_schedules": "このグループのスケジュールはありません",
  "wset.derived_hint": "リポジトリ・会話の所属から自動的にこのグループに含まれています",

  // === P5 共有 UI（ui/* ・panes・App/TopBar・useUpdateCheck・agentModels・workspace・WhichKey） ===
  "ui.sep": "・",
  "ui.assistant": "アシスタント",
  "ui.repositories": "リポジトリ",
  "ui.files": "ファイル",
  "ui.starts_when_workspace_running": "ワークスペースを起動すると表示されます。",
  "ui.filter_models": "モデルを絞り込み…",
  "ui.filter_kind_models": "{kind} のモデルを絞り込み",
  "ui.kind_model": "{kind} のモデル",
  "ui.claude_registered_model": "登録したモデルから選択",
  "ui.select_from_count": "{count} 件から選択",
  "ui.no_matching_models": "一致するモデルなし",
  "ui.count_items": "{count} 件",
  "ui.cancel": "キャンセル",
  "ui.run": "実行",
  "ui.running": "実行中…",
  "ui.processing": "処理中…",
  "ui.close": "閉じる",
  "ui.ai_related": "AI 関連",
  "ui.secret": "機密",
  "ui.confirm_continue": "続行しますか？",
  "ui.pane_swap_hint": "ペイン{ordinal} — ドラッグして他のペインと入れ替え",
  "ui.drag_to_swap": "ドラッグして入れ替え",
  "ui.unwrap_lines": "折り返しを解除",
  "ui.wrap_lines": "行を折り返す",
  "ui.close_pane_hint": "このペインを閉じる（中クリック / Ctrl+クリックで直接閉じる）",
  "ui.close_tab_hint": "このタブを閉じる",
  "ui.popout_pane_hint": "別タブで開く（このペインは移動します）",
  "ui.popout_expand": "フルConsoleに展開",
  "popout.blocked": "別タブを開けませんでした。ブラウザのポップアップブロックを確認してください",
  "popout.stale_link": "このポップアウトリンクは無効です。通常画面を開きました",
  "popout.cannot": "このペインは別タブへ切り離せません",
  "ui.find_in_pane": "ペイン内を検索",
  "ui.find_prev": "前の一致（Shift+Enter）",
  "ui.find_next": "次の一致（Enter）",
  "ui.close_find": "検索を閉じる（Esc）",
  "ui.next_key": "次のキー",
  "ui.wk_groups": "サブメニュー",
  "ui.wk_actions": "アクション",
  "ui.wk_back": "1つ戻る",
  "ui.wk_cancel": "キャンセル",
  "ui.new_version_available": "新しいバージョンがあります",
  "ui.update_sessions_safe": "更新しても実行中のセッションは止まりません。",
  "ui.update_backend_note":
    "バックエンドも更新されています。反映には任意のタイミングでワークスペースの停止→起動が必要です（実行中のセッションは停止し、あとで再開できます）。",
  "ui.update": "更新",
  "ui.recreate_failed": "作り直しに失敗しました",
  "ui.cleanup_failed": "掃除に失敗しました",
  "ui.default": "既定",
  "ui.default_with": "既定（{effort}）",
  "ui.default_claude_xhigh": "既定（Claude Code = xhigh）",
};
