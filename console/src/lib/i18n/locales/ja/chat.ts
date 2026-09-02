// 日本語 カタログ / ドメイン: chat
// キー接頭辞: chat
//
// ⚠️ 追記は**自分のドメインのファイルだけ**に行う（ADR 0067 決定 4）。分割前は 4,700 行の
// 1 ファイルで、フロントの並列セッションが全員ここへ追記＝毎回確実に衝突していた。
// ja が正本。新しいキーはここに足し、同じキーを ../en/ の同名ファイルにも足す。
export const chat = {
  // === P2 ChatView（features/chat/ChatView.tsx）===
  "chat.not_found": "会話が見つかりません",
  "chat.load_failed": "会話の読み込みに失敗しました",
  "chat.new_title": "新しいチャット",
  "chat.create_failed": "会話の作成に失敗しました",
  "chat.image_paste_failed": "画像の貼り付けに失敗しました",
  "chat.image_paste_failed_net": "画像の貼り付けに失敗しました（通信エラー）",
  "chat.label": "チャット",
  "chat.tts_source_work": "チャット・作業過程",
  "chat.state_running": "進行中",
  "chat.state_idle": "待機中",
  "chat.compact_btn": "圧縮",
  "chat.compact_tip": "会話を要約し、要約だけを新しいセッションへ引き継いでコンテキストを圧縮します（この画面の履歴は残ります）",
  "chat.compact_confirm_title": "コンテキストを圧縮しますか？",
  "chat.compact_confirm_body":
    "これまでの会話を要約し、要約だけを新しいセッションへ引き継ぎます。この画面の会話履歴はそのまま残りますが、引き継がれるのは要約のみです。要約の作成に1ターン分のトークンを消費します。",
  "chat.compacting": "圧縮中…",
  "chat.compact_failed": "コンテキストの圧縮に失敗しました",
  "chat.switch_agent": "エージェントを切り替え",
  "chat.switch_agent_tip": "この会話を動かすエージェント（CLI）を切り替えます",
  "chat.switch_agent_note":
    "これまでの会話は次の送信でまとめて引き継がれます（その1ターンだけトークン消費が増えます）。モデルは設定 > アシスタントの各 CLI の行から解決し直します。",
  "chat.switch_agent_failed": "エージェントの切り替えに失敗しました",
  "chat.switch_agent_offline": "未接続の CLI です（設定 > 接続でログインすると選べます）",
  "chat.assistant_fallback": "アシスタント",
  "chat.greeting_empty": "メッセージを送って会話を始めましょう。",
  "chat.empty_hint":
    "メッセージを送って会話を始めましょう。Markdown 文書の翻訳や要約、質問への回答などを依頼できます。",
  "chat.you": "あなた",
  "chat.report_role": "セッション報告",
  // セッション報告カード（docs/log/28 P6）。カードは**事実だけ**で、オペレーターへの行動指示は
  // 含まない（指示は Agent がプロンプトを組む瞬間に生成する）。{display}/{name} は報告元の
  // セッション、それ以外は Agent が渡す引数。features/chat/report.ts が組み立てる。
  "chat.report.headline": "セッション「{display}」({name}) からの報告: ",
  "chat.report.answer_ready": "応答が完了し、入力待ちになりました。",
  "chat.report.turn_failed":
    "ターンがモデル／プロバイダ側のエラーで終了し、入力待ちに戻りました（応答は生成されていません）。",
  "chat.report.turn_aborted":
    "ターンが中断して入力待ちに戻りました（接続断や一時的なレート制限など、時間をおけば解消する原因で、回答は完成していません）。再送すれば続きから走れる中断です。",
  "chat.report.turn_aborted_capped":
    "ターンが中断して入力待ちに戻りました（接続断や一時的なレート制限など、時間をおけば解消する原因で、回答は完成していません）。再送すれば続きから走れる中断です。【自動再開の上限（{max}回）に達しています】",
  "chat.report.question": "質問（選択肢）を提示して停止しています。",
  "chat.report.plan_approval": "プランを提示して承認待ちで停止しています。",
  "chat.report.permission_request": "ツール実行の許可待ちで停止しています。許可は Console から行う必要があります。",
  "chat.report.reopened": "先の完了報告は早計でした — セッションはその後も作業を続けています。",
  "chat.report.reopen_capped":
    "先の完了報告は早計でしたが、完了判定が繰り返し揺れています（訂正の上限 {max} 回に達したため、これ以上の自動訂正は行いません）。",
  "chat.report.exit": "エージェントプロセスが異常終了しました: {label}。",
  "chat.report.unknown": "状態が変化しました（{kind}）。",
  "chat.report.exit_reason.oom": "OOM（メモリ不足で強制終了）",
  "chat.report.exit_reason.crashed": "クラッシュ",
  "chat.report.exit_reason.killed": "強制終了（SIGKILL）",
  "chat.report.note.rate_limit_resume":
    "【利用上限による停止です】{at}（上限が解ける時刻）に、このセッションへ続行を送る自動再開の予約が入っています。",
  "chat.report.note.fold": "（この報告は指示 {count} 件ぶんの完了です。投入: {ats}）",
  "chat.report.note.reopen_target": "（訂正の対象: {at} の完了報告）",
  // role==="notice" のカード本文（ADR 0033）。バックエンドはキーと引数だけを刻み、
  // 表示文はここが持つ — 保存済みの会話でも Lang の切替に追従する。キーは
  // workspace/agent/chat_notice.go と対（Go 側テストが欠落を検出する）。
  "chat.notice.ctx_pressure":
    "この会話のコンテキスト使用量が上限の約{pct}%（{tokens} / {window} トークン）に達しました。このまま続けると、応答の品質低下・ターンの失敗・トークン消費の増大につながります。ヘッダのコンテキストバー右にある「圧縮」で要約だけを新しいセッションへ引き継いで続行するか、区切りの良いところで新しいチャットを開くことを検討してください。",
  "chat.notice.ctx_overflow":
    "コンテキストが上限を超えたため、応答を生成できませんでした。この会話はこのままでは続行が難しい状態です。ヘッダのコンテキストバー右にある「圧縮」で要約だけを新しいセッションへ引き継いで続けるか、新しいチャットを開いて必要な要点だけを渡してください。",
  "chat.notice.auto_paused.head": "自動応答が連続 {limit} 回の上限に達したため、いったん停止しました。",
  "chat.notice.auto_paused.pending_one": "未処理のセッション報告が {count} 件残っています。",
  "chat.notice.auto_paused.pending_other": "未処理のセッション報告が {count} 件残っています。",
  "chat.notice.auto_paused.tail":
    "続ける場合は、このチャットにメッセージ（例:「続けて」）を送ってください。次のメッセージ送信で自動応答の回数がリセットされ、保留中の報告も引き継がれます。",
  "chat.notice.compact_manual":
    "コンテキストを圧縮しました。次の要約だけを新しいセッションへ引き継ぎ、続きはその上で応答します（この画面の会話履歴はそのまま残ります）。\n\n---\n\n{summary}",
  "chat.notice.compact_auto":
    "コンテキスト使用量が閾値を超えたため、自動で圧縮しました。次の要約だけを新しいセッションへ引き継ぎ、続きはその上で応答します（この画面の会話履歴はそのまま残ります）。\n\n---\n\n{summary}",
  "chat.notice.compact_recovery":
    "コンテキスト超過エラーからの自動復旧のため、圧縮しました。次の要約だけを新しいセッションへ引き継ぎ、続きはその上で応答します（この画面の会話履歴はそのまま残ります）。\n\n---\n\n{summary}",
  "chat.notice.agent_switched":
    "この会話のエージェントを「{agent}」に切り替えました。ここから先は {agent} が応答します。それまでの会話は次の送信時にまとめて引き継がれるため、最初の1ターンだけトークン消費が増えます。モデルは設定 > アシスタントの {agent} の行から解決し直しました。",
  "chat.notice.plan_updated":
    "作業計画を更新しました。以降の新しいセッションには、この計画を要約せず原文のまま引き継ぎます。\n\n---\n\n{plan}",
  "chat.plan.title": "作業計画",
  "chat.plan.toggle_tip": "作業計画を開く（圧縮しても要約されず、原文のまま新しいセッションへ引き継がれます）",
  "chat.plan.edit": "編集",
  "chat.plan.refresh": "更新",
  "chat.plan.refreshing": "更新中…",
  "chat.plan.refresh_tip": "直近の会話から計画を引き直します（壁打ちで計画が変わった直後に押してください）",
  "chat.plan.clear": "クリア",
  "chat.plan.clear_confirm_title": "作業計画をクリアしますか?",
  "chat.plan.clear_confirm_body":
    "この会話の作業計画を空にします。以降の新しいセッションへは計画が引き継がれなくなります（会話履歴と引き継ぎ要約はそのまま残ります）。",
  "chat.plan.placeholder": "## 制約\n## 前提\n## これからやること",
  "chat.plan.empty": "まだ計画がありません。「更新」で直近の会話から起こすか、「編集」で直接書けます。",
  "chat.plan.note":
    "計画は要約されず、原文のまま新しいセッションへ毎回引き継がれます。圧縮時には直近の会話に合わせて自動更新されます。",
  "chat.plan.failed": "作業計画の更新に失敗しました",
  "chat.working": "作業中…",
  "chat.thinking": "考え中…",
  "chat.suggest_ai": "AIに返信候補を出してもらう（直近の会話から）",
  "chat.suggest_none": "返信候補を作れませんでした（会話が浅いか、利用できるAIがありません）",
  "chat.suggest_failed": "返信候補の生成に失敗しました",
  "chat.remove": "削除",
  "chat.uploading": "アップロード中…",
  "chat.stop": "停止",
  "chat.send": "送信",
  "chat.prev_input": "前の入力",
  "chat.next_input": "次の入力",
  "chat.work_process": "作業過程",
  "chat.tool_count_one": "ツール {count}件",
  "chat.tool_count_other": "ツール {count}件",
  "chat.interim_count_one": "・途中応答 {count}件",
  "chat.interim_count_other": "・途中応答 {count}件",
  "chat.read_from_here": "ここから読み上げ",
  "chat.read_title": "カラオケ読み上げ",
  "chat.read": "読み上げ",
  "chat.pause": "一時停止",
  "chat.resume": "再開",
  "chat.copy_md_title": "Markdown をコピー",
  "chat.copied": "コピー済",
  "chat.copy": "コピー",
  "chat.preview_failed": "プレビューを取得できませんでした",
  "chat.click_to_zoom": "クリックで拡大",
  "chat.pasted_image_alt": "貼り付け画像",
  "chat.ph_mod_img": "メッセージを入力（Ctrl+Enter で送信 / Enter で改行 / 画像は貼り付け）",
  "chat.ph_enter_img": "メッセージを入力（Enter で送信 / Shift+Enter で改行 / 画像は貼り付け）",
  "chat.ph_mod": "メッセージを入力（Ctrl+Enter で送信 / Enter で改行）",
  "chat.ph_enter": "メッセージを入力（Enter で送信 / Shift+Enter で改行）",
  "chat.ph_loading": "読み込み中…",
  "chat.intro": "セッションの通知を Discord / Slack に送ります。通知の ON/OFF は 個人設定 › 通知 で切り替えます。",
  "chat.settings": "通知設定",
};
