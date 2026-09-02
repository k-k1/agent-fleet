// 日本語 カタログ / ドメイン: assistant
// キー接頭辞: asst, assistant
//
// ⚠️ 追記は**自分のドメインのファイルだけ**に行う（ADR 0067 決定 4）。分割前は 4,700 行の
// 1 ファイルで、フロントの並列セッションが全員ここへ追記＝毎回確実に衝突していた。
// ja が正本。新しいキーはここに足し、同じキーを ../en/ の同名ファイルにも足す。
export const assistant = {
  // --- ビルトインアシスタント（docs/log/28 P3。id は assistants.go の固定集合。Agent は
  //     和文を返すが、表示は builtin のとき Console カタログが解決する）---
  "assistant.af.name": "Agent Fleet アシスタント",
  "assistant.af.desc":
    "こんにちは。Agent Fleet の使い方を案内します。操作手順や、今のワークスペースの状態（動いているセッションなど）を実際に確認しながらお答えします。",
  "assistant.operator.name": "フリート・オペレーター",
  "assistant.operator.desc":
    "フリートの司令塔です。走っているセッションを俯瞰し、必要ならセッションに指示を出したり新しいセッションを起こして作業を進めます（引き継ぎ・壁打ちからのタスク開始も可）。メモキューの確認・追加・一括送信もできます。専門的な判断は他のアシスタントにも相談します。実行前に内容を確認します。",
  "assistant.sre.name": "SRE アシスタント",
  "assistant.sre.desc":
    "インシデント対応・監視運用の相談相手です（読み取り専用）。PagerDuty・Grafana・CloudWatch を接続しておくと、開いているインシデントやメトリクス・ログを実際に確認しながら、状況整理・原因の仮説出し・対外報告の草稿を手伝います。",

  // --- アシスタント設定（features/settings/AssistantTab.tsx）---
  "assistant.title_suggest": "タイトルのAI提案",
  "assistant.note_title_suggest":
    "チャットの名前変更ダイアログにある「AIに提案してもらう」ボタンの有効/無効です。セッションのタイトル自動提案は「エージェント」タブで設定します。",
  "assistant.output_language": "回答言語",
  "assistant.note_output_language":
    "アシスタント・チャットの回答言語です。「入力に合わせる」は、渡した文章や質問の言語に合わせて返します。日本語／English を選ぶと、他言語の文章でもその言語で回答します（翻訳アシスタントは対象外）。",
  "assistant.agent_order": "エージェント優先順位",
  "assistant.note_agent_order":
    "アシスタント・チャットと補助生成を動かす CLI の優先順位です。上から順に見て、最初に接続済みの CLI が使われます（未接続のものは飛ばされます）。モデルは下で CLI ごとに指定します。反映はビルトインアシスタントの新しい会話から（カスタムアシスタントの明示設定が優先）。",
  "assistant.models": "アシスタントのモデル",
  "assistant.note_models":
    "ビルトインアシスタントが新しい会話で使うモデルです。優先順位で別の CLI に切り替わった場合も、その CLI の行で選んだモデルを使います。「推奨」は接続中のモデルから安全な既定を選び、現在の解決結果も表示します。",
  "assistant.utility_models": "タイトル・サジェストのモデル",
  "assistant.note_utility_models":
    "セッション／チャットのタイトル、ブランチ名、AI返信候補に使う短時間モデルです。CLIの選択は上の優先順位に従います。「推奨」は利用可能な高速・低コストモデルを選び、見つからなければCLI既定へ安全に縮退します。",
  "assistant.recommended_now": "推奨（現在: {model}）",
  "assistant.auto_turn": "セッション報告への自動応答",
  "assistant.note_auto_turn":
    "フリート・オペレーター等（AF 書き込み許可のアシスタント）が起こした・指示したセッションから完了報告が届いたとき、アシスタントが自動で 1 ターン動いて後続を処理します。暴走防止のため、あなたの発言なしで動ける自動ターンは会話ごとに下の上限回数までです（発言でリセット）。",
  "assistant.auto_turn_limit": "自動応答の上限回数",
  "assistant.note_auto_turn_limit":
    "あなたの発言なしで連続実行できる自動応答の回数です（既定 10・最大 50）。上限に達すると一時停止のお知らせが届き、あなたが次のメッセージを送ると再開します。無制限にはできません。",
  "assistant.auto_turn_model": "自動応答のモデル",
  "assistant.auto_turn_model_default": "会話のモデルのまま",
  "assistant.note_auto_turn_model":
    "セッション報告への自動応答だけを別のモデルで実行します。報告の確認と要約は定型作業のため、haiku などの軽量モデルに切り替えるとトークン消費を大きく減らせます。対象は claude の会話のみで、あなたのメッセージへの回答やコンテキスト圧縮の要約は会話本来のモデルのままです。",
  "assistant.auto_turn_delay": "自動応答の束ね時間",
  "assistant.auto_turn_delay_off": "即時",
  "assistant.note_auto_turn_delay":
    "完了報告が届いてもすぐには自動応答せず、この時間の間に届いた他のセッションの報告をまとめて 1 回で処理します（自動応答は毎回会話全体を読み直すため、回数を減らすとトークン消費が下がります）。報告カードと通知はすぐに届きます — 遅れるのはアシスタントの追撃だけです。",
  "assistant.quiet_completion": "静かな完了報告",
  "assistant.note_quiet_completion":
    "正常な完了報告では自動応答を実行せず、報告カードと通知センターへの配信だけにします。報告の内容は、次にあなたがメッセージを送ったときにまとめてアシスタントに引き継がれます。中断・失敗・異常終了の報告と、質問・プラン承認への対応は従来どおり自動で行われます。",
  "assistant.auto_pilot": "自動走行（質問・プランの自動対応）",
  "assistant.note_auto_pilot":
    "ON にすると、指示したセッションが質問（選択肢）で止まったときはセッションの推奨からオペレーターが自動で回答し、プラン承認待ちで止まったときは別セッションにレビューさせて、フィードバック→承認まで自動で進めます。判断内容は毎回チャットで共有されます。推奨が不明瞭な質問や、破壊的・不可逆な操作を含む選択・プランは自動対応せず確認が届きます。既定 OFF。",
  "assistant.auto_resume": "中断時の自動再開",
  "assistant.note_auto_resume":
    "指示したセッションのターンが接続断や一時的なレート制限で途中で切れたとき、オペレーターが「続けて」とだけ送って再開させます（送信はセッションが使っている言語に合わせます）。再開したことは毎回チャットで共有されます。原因が自然には解消しない中断（利用上限・残高切れ・プロンプト長超過など）は再送しても同じ結果になるため自動再開せず、対処の相談が届きます。連続 2 回まで自動再開し、それでも中断が続く場合はあなたに報告します。既定 ON。",
  "assistant.auto_compact": "コンテキストの自動圧縮",
  "assistant.note_auto_compact":
    "チャットのコンテキスト使用率が 90% を超えたまま次のやり取りが始まるとき、先に会話を要約して新しいセッションへ自動で引き継ぎます（要約の作成に 1 ターン分のトークンを消費）。80% の時点で届くお知らせから手動の「圧縮」で先に整理することもできます。",
  "assistant.auto_compact_tokens": "自動圧縮の閾値（トークン）",
  "assistant.note_auto_compact_tokens":
    "コンテキストの使用量がこのトークン数を超えたまま次のやり取りが始まるとき、使用率 90% に達していなくても自動で圧縮します。チャットは毎ターン全コンテキストを読み直すため、この値がそのままターンあたりのトークン消費の上限になります。小さいほど節約になりますが、圧縮（要約の作り直し）の頻度が上がります。既定 150k。",
  "assistant.output_tail": "セッション出力の取得上限",
  "assistant.note_output_tail":
    "オペレーターがセッションの出力を確認するとき（get_session_output）に読み込む量の上限です（末尾から）。読んだ内容は会話に蓄積されて以降の全ターンで読み直されるため、上限が大きいほどトークン消費が増えます。全文はミラーでいつでも確認できます。既定 32 KiB。",
  "assistant.appearance": "外観",
  "assistant.note_appearance":
    "「継承」はアプリのテーマに従います。テーマ・背景色はこの端末のみで保存されます（他の端末には同期されません）。",

  // === P5 アシスタント UI（AssistantModal/AssistantSection） ===
  "asst.tool_none": "なし",
  "asst.tool_none_help": "外部ツールなし。チャットで直接回答します。",
  "asst.tool_read": "AF 読み取り",
  "asst.tool_read_help": "自分のワークスペースのセッション一覧・状態・出力を読み取れます（書き込みは不可）。",
  "asst.tool_write": "AF 書き込み",
  "asst.tool_write_help": "読み取りに加え、セッションへプロンプトを送信できます（作業の代行）。信頼できる用途にのみ許可してください。",
  "asst.voice_auto": "自動（キャラプールから）",
  "asst.edit_assistant": "アシスタントを編集",
  "asst.create_assistant": "アシスタントを作成",
  "asst.name": "名前",
  "asst.name_ph": "例: リリースノート翻訳",
  "asst.desc_label": "説明（会話開始時の挨拶）",
  "asst.desc_hint": "会話を始める前に表示される自己紹介です。何ができるかを一言で。",
  "asst.desc_ph": "例: 文章を渡してください。日本語↔英語を翻訳します。",
  "asst.icon": "アイコン",
  "asst.agent": "エージェント",
  "asst.model": "モデル（任意）",
  "asst.model_ph": "既定モデル",
  "asst.voice_label": "読み上げの声（任意）",
  "asst.voice_hint": "このアシスタントの回答を読み上げるときの声。「自動」は読み上げ設定のキャラプールから固定で割り当てます（「セッションごとに声を変える」が ON のとき。OFF なら設定の話者）。",
  "asst.persona_label": "ペルソナ（システムプロンプト）",
  "asst.persona_hint": "アシスタントの役割・口調・制約を書きます。空欄なら汎用アシスタントとして動きます。",
  "asst.persona_ph": "例: あなたは技術文書の翻訳者です。訳文のみを返してください。",
  "asst.tools_label": "ツール許可",
  "asst.mcp_label": "MCP サーバー（任意）",
  "asst.mcp_hint": "このアシスタントのチャットに接続する MCP サーバーを選びます。組み込み連携（PagerDuty / Grafana / CloudWatch）と、設定＞MCP サーバー で登録したものが並びます。",
  "asst.mcp_empty": "接続できる MCP サーバーがありません。設定＞MCP サーバー で登録するか、運用・監視 タブで連携を接続してください。",
  "asst.mcp_disabled": "無効化中（接続されません）",
  "asst.mcp_not_ready": "設定が未完了（接続されません）",
  "asst.mcp_out_of_scope": "{agent} は対象外（接続されません）",
  "asst.knowledge_label": "知識ディレクトリ（任意・1行に1つ）",
  "asst.knowledge_hint": "参照させたいドキュメントのあるディレクトリ（コンテナ内の絶対パス）を指定すると、会話で読み取れます。",
  "asst.cancel": "キャンセル",
  "asst.save": "保存",
  "asst.create": "作成",
  "asst.update_failed": "アシスタントの更新に失敗しました",
  "asst.create_failed": "アシスタントの作成に失敗しました",
  "asst.delete_assistant": "アシスタントを削除",
  "asst.delete_confirm": "「{name}」を削除します。作成済みの会話は残ります。",
  "asst.delete": "削除",
  "asst.delete_failed": "アシスタントの削除に失敗しました",
  "asst.remove_failed": "削除に失敗しました",
  "asst.rename": "表示名を変更",
  "asst.rename_failed": "名前の変更に失敗しました",
  "asst.title_rename_title": "タイトルを変更",
  "asst.title_label": "タイトル",
  "asst.title_ph": "例: 請求APIのリファクタ相談",
  "asst.ai_suggest": "AIに提案してもらう",
  "asst.proposal": "提案",
  "asst.adopt": "この案にする",
  "asst.suggest_fetch_failed": "提案の取得に失敗しました（通信エラー）",
  "asst.copy_id": "ID（{id}）をコピー",
  "asst.id_copied": "チャットID「{id}」をコピーしました",
  "asst.new_chat": "新規チャット",
  "asst.builtin_badge": "常設",
  "asst.edit": "編集",
  "asst.section_title": "アシスタント",
  "asst.empty": "チャットはまだありません。＋ から開始できます。",
  "asst.in_progress": "進行中",
  "asst.waiting": "待機中",
  "asst.focus_pane": "ペイン{n}にフォーカス",
  "asst.lock": "削除ロックをかける",
  "asst.unlock": "削除ロックを解除する",
  "asst.locked_hint": "削除ロック中です。先にロックを解除してください。",
  "asst.lock_failed": "削除ロックの変更に失敗しました",
  "asst.locked_on": "削除ロックをかけました",
  "asst.locked_off": "削除ロックを解除しました",
  "asst.delete_chat": "このチャットを削除",
  "asst.open_new_pane": "新しいペインで開く",
};
