// Japanese catalog / domain: engineadmin (the admin Inference engines pane and its model catalog,
// features/settings/admin/)
// Key prefixes: admin.engines_, admin.catalog_, admin.fit_, admin.repo_
// (every other admin.* key stays in admin.ts; engines.ts is the topbar indicator's engine.*)
//
// Add keys only to your own domain's file (ADR 0067 decision 4): one file every parallel session
// appends to conflicts on every merge. ja is the canonical catalog: add a new key here and the same
// key to the file of the same name in ../en/.
export const engineadmin = {
  "admin.engines_none": "この配備は自前の推論エンジンを動かしていません。",
  // 各行の「モデルカタログ」ボタンで開くペイン（テナント設定側で tenant.tab_engines が
  // 権限のあるテナント管理者に開くのと同じモデル画面）。
  "admin.engines_open_catalog": "モデルカタログ",
  "admin.engines_ops_super_only": "エンジンの起動と停止・GPU の選択は配備管理者（super_admin）の担当です。モデルの取り込みと確認は各行の「モデルカタログ」ボタンからできます。",
  // 役割のタブ。配備にエンジンが 1 本しか無ければタブは出さず、名前だけを出す。
  "admin.engines_role_llm": "文章",
  "admin.engines_role_image": "画像",
  "admin.engines_tab_models": "モデル",
  "admin.engines_tab_loras": "LoRA",
  // LoRA が 0 件なのは普通の状態。赤くすると、要らない配備で毎回壊れて見える。
  "admin.engines_loras_empty": "このエンジンに LoRA はありません。",
  "admin.engines_models_label": "モデル",
  "admin.engines_always_on_note": "常時稼働は GPU のインスタンスを止めません（時間単価はインスタンスクラスによります）。用が済んだらオンデマンドへ戻してください。",
  "admin.engines_provider_unserved": "この行は images provider「{p}」として設定されていますが、この build はその provider のクライアントを実装していません。行を comfy か openai-compat に向け直すまで、ここで generate_image は動きません。",
  "admin.engines_tenant_scope":
    "ここではモデルの取り込みと、取り込んだ行の確認ができます。モデルの有効化・エンジンの起動と停止・GPU の選択・行の削除は配備管理者（super_admin）の担当なので、この画面には出ません。カタログは配備に 1 つで、取り込んだモデルの id はどのテナントからも見えます。",
  "admin.engines_note": "「無効」にすると、そのエンジンは起動メニューからも generate_image からも消え、要求は 503 で断られます。「オンデマンド」は要求が来たときだけインスタンスを買い、アイドルで自分で止まります。",
  // --- 外部管理のエンジン（ADR 0076）---
  // 🔴 TTS の「常駐 docker 等」は流用しない。VOICEVOX はそれで合っているが、LAN の
  // ComfyUI は誰かの PC で動いていることもある。両者に共通する主張は「この配備は
  // 起動も停止も持たない」で、運用者が読むべきなのもそれだけである。
  "admin.engines_external": "（外部管理: この配備は起動も停止もしません）",
  "admin.engines_url_label": "接続先",
  "admin.engines_note_external": "外部管理のエンジンは、この配備が指している URL です。「無効」は経路を閉じるだけで、向こう側の停止はしません（起動も停止もこの配備は持ちません）。URL の変更は Control Plane の再起動です。",
  // --- 借用エンジン（ADR 0079）---
  // 🔴 「外部管理」を流用しない。向こうが LAN の箱なら「誰も起こさない」で合っているが、
  // 借用行の向こうに居るのは自分の管理画面を持った別の Agent Fleet で、起動も停止も
  // カタログの編集もそちらの仕事である（決定 10）。「外部管理」は正しいが、運用者が
  // 次に何をすればよいかを一つも言わない。
  "admin.engines_remote": "（借用: 別のフリートが起動します）",
  // 接続先ではなく「借用元」。値は向こうのフリートのベース URL で、これがこの画面で
  // 「このモデルはどこの GPU で動いているのか」に答える唯一の情報である。
  "admin.engines_remote_url_label": "借用元",
  // 🔴 借用行の warm は、この配備の観測ではなく向こうのカタログが公開している観測の写しで
  // ある（決定 10）。RUNNING のような状態はそもそも来ないので、無条件の「読み込み済」は
  // 持っていない箱について断定することになる。
  "admin.engines_remote_model_loaded": "（借用元が最後に見た時点で読み込み済）",
  "admin.engines_remote_model_declared": "（宣言。借用元はまだ読み込みを見ていません）",
  "admin.engines_note_remote": "借用エンジンは、別の Agent Fleet のゲートウェイ越しに使っています。起動と停止・GPU の選択・カタログの編集は、いずれも借用元の配備の管理画面で行います。「無効」はこちら側の経路を閉じるだけで、向こうのインスタンスは止めません。",
  "admin.engines_remote_catalog": "このカタログは借用元の配備のものを写したものです。この画面からは変更できません（取り込み・有効化・削除は借用元で行います）。借用元:",
  // --- 借用トークンの発行（ADR 0079 決定 3・貸す側）---
  // エンジンを持っている側の super_admin が、借りる配備の要る発行トークン 1 本を見せる導線。
  // これが無かったときの手順は「向こうの IdP に専用アカウントを招く → 一度サインインする →
  // そのワークスペースの環境変数を読む → 止める」の 4 手で、未解決 6 はこの重さの話だった。
  //
  // 🔴 ここの文言は飾りではない。値は署名マスタから決定的に導出されるので「1 本だけ無効に
  // する」という操作がそもそも存在せず、CP 側もそれを拒めない（「このメンバーシップは人か」に
  // 正直に答えられる列が無い）。だから警告と失効方法を落とすと、運用者が自分のトークンを
  // 使い回してフリート全体を人質に取る事故になる。
  //
  // ⚠️ サーバは warning / revoke を英語の生文でも返すが、この画面はそれを出さず、
  // deterministic / has_workspace / env_var / opens という構造から描いている。いちばん
  // 読まれなければならない 2 文が、日本語の画面の中の英語の段落になってはいけない。
  "admin.engines_issue_head": "借用トークンを発行して見せる",
  "admin.engines_issue_intro": "別の Agent Fleet がこの配備のエンジンを借りるとき、向こうは発行トークン（afei_…）を 1 本だけ必要とします。借用用のメンバーシップのテナントと user_key を入れて押すと、そのトークンをこの画面に表示します。",
  "admin.engines_issue_before": "押すと出るのは、そのメンバーシップ自身のエンジン資格情報です。ここで新しく作るのではなく、この配備の署名マスタから決定的に導出された同じ値が、何度押しても出てきます。借りる側ごとに違う値を出すことはできません。",
  "admin.engines_issue_before_only": "⚠️ 他に何にも使っていないメンバーシップにだけ発行してください。人（あなた自身を含む）のメンバーシップのトークンは貸さないこと。",
  "admin.engines_issue_tenant": "テナント",
  "admin.engines_issue_tenant_ph": "テナントのスラッグ",
  "admin.engines_issue_user_key": "user_key",
  "admin.engines_issue_user_key_ph": "借用用メンバーシップの user_key",
  "admin.engines_issue_submit": "発行して表示する",
  "admin.engines_issue_working": "発行中…",
  "admin.engines_issue_for": "対象: {t}/{k}（役割 {role}・メンバーシップ {id}）",
  "admin.engines_issue_reveal": "表示",
  "admin.engines_issue_hide": "隠す",
  "admin.engines_issue_copy": "トークンをコピー",
  "admin.engines_issue_copy_env": "{v}= の形でコピー",
  "admin.engines_issue_copied": "コピーしました",
  "admin.engines_issue_clear": "画面から消す",
  "admin.engines_issue_autoclear": "この値は {m} 分で画面から自動的に消えます。読み終わったら「画面から消す」を押してください。同じ値はもう一度発行すればまた出ます。",
  "admin.engines_issue_env_label": "借りる側で入れる環境変数",
  "admin.engines_issue_opens_label": "このトークンが開ける経路",
  "admin.engines_issue_opens_only": "開くのはこの経路だけです。git・MCP・メモ・API は、このトークンでは開きません。",
  "admin.engines_issue_deterministic": "⚠️ この値は署名マスタから決定的に導出されるため、何度発行しても同じ値です。1 本だけ無効にすることはできません。",
  "admin.engines_issue_revoke_label": "失効のしかた",
  "admin.engines_issue_revoke": "{t}/{k} のメンバーシップを削除してください。メンバーシップは要求ごとに解決されているので、次の要求から 401 になります。",
  "admin.engines_issue_revoke_only": "⚠️ これ以外の失効手段は署名マスタの回転だけです。git・メモ・スケジュールのトークンも同じマスタから出ているので、回すとこの配備の全員がログアウトします。",
  "admin.engines_issue_has_workspace_tag": "ワークスペースあり",
  "admin.engines_issue_has_workspace": "⚠️ このメンバーシップにはワークスペースがあります。人が使っているメンバーシップの形です。人の発行トークンを貸すと、取り消すには、その人のメンバーシップを削除して本人も使えなくするか、この配備の全員がログアウトする署名マスタの回転をするしかありません。借用専用のメンバーシップを別に作ってから発行してください。",
  "admin.engines_issue_no_workspace": "このメンバーシップにワークスペースはありません（借用専用のメンバーシップとして期待される形です）。",
  // --- エンジンの現況（features/settings/admin/adminEngines.tsx の EngineStatus）---
  // ⚠️ ここの文言は「分からないことは書かない」で通っている。CP が答えを持たない行は
  // そもそも出さないので、「不明」「0」「予定なし」といった穴埋めの語を足さないこと。
  // 出てしまうと、空白＝未知だったものが断定として読まれ、そのまま GPU を止める判断に使われる。
  "admin.engines_model_loaded": "（読み込み済）",
  "admin.engines_model_declared": "（宣言。まだ読み込まれていません）",
  // --- モデルカタログ（ADR 0072）---
  // 「有効/無効」と「これで起動する」は別の問い。前者は配備が使ってよいか、後者は
  // sd-server が抱える 1 つのチェックポイント（llm なら model 未指定の既定）。
  "admin.engines_catalog_empty": "このエンジンのカタログは空です。モデルを取り込むまで、要求は 503 で断られ、インスタンスも起動しません。",
  "admin.engines_model_started": "起動時に読み込む",
  // 状態はバッジで言う。ボタンの文言（「有効にする」）は押したら何が起きるかであって、
  // 今どちらであるかではない。行を薄くして表すのは disabled なコントロールと同じ見た目になる。
  "admin.engines_model_is_on": "有効",
  "admin.engines_model_is_off": "無効",
  "admin.engines_model_enable": "有効にする",
  "admin.engines_model_disable": "無効にする",
  "admin.engines_model_select": "これで起動する",
  // 🔴 「削除」ではなく「登録を消す」。CP に `s3:DeleteObject` は無く、足すつもりも無い
  // （決定 7。消すのは取り込みタスクの仕事で P4）。ファイルはバケットに残る。
  "admin.engines_model_forget": "登録を消す",
  // 🔴 「登録を消す」と「ファイルを消す」は別の行為である。前者だけだと、バケットに誰も
  // 到達できないバイト列が残って課金され続ける（実測 2026-09-09: 491 MB のファイルが行より
  // 長生きした）。後者は取り込みタスクの MODE=delete が行う——CP に s3:DeleteObject は無い
  // （決定 7）。押す前に選ばせる。
  "admin.engines_model_forget_purge": "バケットのファイルも削除する（取り消せません）",
  "admin.engines_model_forget_note": "カタログの行だけ消します。バケットのファイルはそのまま残り、保管費もかかり続けます。",
  "admin.engines_model_forget_purge_note": "行を消し、続けてバイト列を削除するタスクを起こします。削除はタスクが行うので、完了までに少しかかります。",
  "admin.engines_model_add_id": "id",
  "admin.engines_model_add_family": "ファミリー",
  // ADR 0072 追記（ネガティブプロンプト）。「何を描かせないか」には 3 か所が口を出す——この行・
  // 要求・配備——そしてそれらは足し合わされる。どのラベルも「唯一のネガティブ」と読めてはいけない。
  "admin.engines_model_negative": "描かせないもの",
  // ADR 0081 決定 5。チェックポイントの「描かせないもの」と対になる、アダプタ側の 1 行。
  // 起動語の無い LoRA は読み込んでも絵が変わらず、取り込みの失敗と見分けが付かない。
  "admin.engines_model_trigger": "起動語",
  "admin.engines_negative_label": "全画像から除外する語",
  "admin.engines_negative_placeholder": "キーワードをカンマ区切りで",
  "admin.engines_negative_save": "保存",
  // 🔴 これをコンテンツフィルタと読ませないための 1 文。これはネガティブプロンプト——
  // サンプラーへの傾きであって門ではなく、蒸留系のファミリーには席すら無い。
  "admin.engines_negative_note":
    "このエンジンが作る全画像のネガティブプロンプトに、モデル自身のものと要求のものに足して加えられます。フィルタではなく誘導です。ネガティブを持たないチェックポイントのファミリーが 2 つあり、その要求では警告にそう出ます。",
  "admin.engines_negative_too_long": "長すぎます。ここはキーワードの列挙であって、方針の文書ではありません。",
  "admin.engines_model_add_lora_base": "土台のモデル",
  "admin.engines_model_add_lora_base_pick": "微調整の対象を選んでください",
  "admin.engines_model_add_family_pick": "選んでください",
  "admin.engines_model_add_part_whole": "チェックポイント（単一ファイル）",
  // --- 発見ボタン（ADR 0082 決定 6・7）----------------------------------------
  // LAN の ComfyUI が実際に持っているファイル名を読み、候補として出す。族は
  // ファイル名から分かる場合の「提案」でしかなく、行を作るのは常に人の「足す」の押下。
  "admin.engines_discover_button": "このエンジンのファイルを調べる",
  "admin.engines_discover_busy": "調べています…",
  "admin.engines_discover_checkpoints": "チェックポイント",
  "admin.engines_discover_loras": "LoRA",
  "admin.engines_discover_vaes": "VAE",
  "admin.engines_discover_vae_hint": "これらは行そのものではなく、チェックポイントの行を作るときに `--vae` として使うファイル名です。",
  "admin.engines_discover_add": "この名前で行を足す",
  "admin.engines_discover_added": "足しました",
  "admin.engines_discover_empty": "このエンジンにチェックポイント・LoRA・VAE のファイルは見つかりませんでした。",
  // 空いている枠が無い／埋まっている枠が無い。押せない理由をボタンの横で言う。
  "admin.engines_wizard_cannot": "この配備ではこのファイルを取り込めません（上の理由）。",
  "admin.engines_wizard_need_id": "id を入れてください。",
  "admin.engines_wizard_need_family": "ファミリーを選んでください。このエンジンはファミリーでワークフローを選び、推測はしません。",
  "admin.engines_model_files_missing_tag": "不足: {f}",
  "admin.engines_model_add_desc": "説明",
  // 🔴 コンテキストウィンドウは「両方か、どちらも書かないか」。context だけだと opencode は
  // 出力上限 0 を 32,000 と読み、32k のモデルが使えるウィンドウ 768 トークンになる
  // （ADR 0072 決定 3）。
  //
  // ラベルは「窓」だった。数字を打ち込む欄の名前としては、その語を既に知っている人にしか
  // 読めない直訳で、英語側も割れていた（フォームが window、メタ行が context）。密度の要る
  // メタ行だけ「コンテキスト」と短くする。
  "admin.engines_model_add_ctx": "コンテキストウィンドウ",
  "admin.engines_model_add_out": "出力上限",
  "admin.engines_ingest_search": "検索",
  "admin.engines_ingest_search_go": "検索",
  "admin.engines_ingest_searching": "検索しています…",
  "admin.engines_ingest_search_none": "見つかりませんでした。別の言葉で試すか、リポジトリ名を直接入力してください。",
  "admin.engines_ingest_source_hf": "Hugging Face",
  "admin.engines_ingest_source_civitai": "Civitai",
  "admin.engines_ingest_source_civitai-red": "Civitai Red",
  "admin.engines_ingest_browse_go": "人気を見る",
  "admin.engines_browse_kind_gguf": "LLM（GGUF）",
  "admin.engines_browse_kind_checkpoint": "画像（checkpoint）",
  "admin.engines_browse_note": "閲覧だけです。取り込むには 60-engines を配備してエンジンを作る必要があります。ここで見ているのは Hugging Face の公開情報で、Control Plane はトークンもバケットも使っていません。",
  "admin.engines_ingest_sort_downloads": "DL数",
  "admin.engines_ingest_sort_trending": "話題",
  "admin.engines_ingest_sort_likes": "いいね",
  "admin.engines_ingest_sort_updated": "更新が新しい",
  "admin.engines_ingest_sort_newest": "新着",
  "admin.catalog_title": "モデルカタログ",
  "admin.catalog_image_title": "画像モデルカタログ",
  "admin.catalog_llm_title": "文章モデルカタログ",
  "admin.catalog_opening": "モデルカタログを開いています…",
  "admin.catalog_role_label": "エンジンの役割",
  "admin.catalog_view_label": "カタログ表示",
  "admin.catalog_view_search": "検索",
  "admin.catalog_view_registered": "登録済み",
  "admin.catalog_family_all": "すべて",
  "admin.catalog_sort": "並び順",
  "admin.catalog_checkpoint": "チェックポイント",
  "admin.catalog_family": "ファミリー",
  "admin.catalog_context": "コンテキスト",
  "admin.catalog_output": "出力",
  "admin.catalog_registered_title": "登録済みモデル",
  "admin.catalog_registered_search": "登録済みモデルを絞り込む",
  "admin.catalog_sort_name": "名前",
  "admin.catalog_sort_enabled": "有効を先に",
  "admin.catalog_sort_default": "既定を先に",
  "admin.catalog_edit": "編集",
  "admin.catalog_parts_unknown": "このモデルにはファイルキーが記録されていません。",
  "admin.catalog_no_description": "説明なし",
  "admin.catalog_file_present": "あり",
  "admin.catalog_file_missing": "不足",
  "admin.catalog_file_unknown": "未確認",
  "admin.catalog_file_uploading": "取り込み中",
  "admin.catalog_file_failed": "取り込み失敗",
  "admin.catalog_edit_window_invalid": "コンテキストと出力は整数で、両方を設定するか両方を 0 にしてください。",
  "admin.catalog_edit_vram_invalid": "実測 VRAM は整数で入力してください。",
  "admin.catalog_gate_accept": "条項への同意が必要",
  "admin.catalog_civitai_newest_note": "Civitai の新着順です。厳密な最終更新順ではありません。",
  "admin.catalog_storage_checking": "保存済みのモデルファイルを確認しています…",
  "admin.catalog_storage_unavailable": "保存状態を取得できませんでした。再読み込みしてください。",
  "admin.catalog_saved_none": "この配布元の保存済みファイルはありません",
  "admin.catalog_saved_unknown": "この配布元の保存状態を確認できません",
  "admin.catalog_manual": "URL・リポジトリを指定",
  "admin.catalog_more": "さらに読み込む",
  "admin.catalog_preview": "作例を拡大",
  "admin.catalog_source_page": "配布元を見る",
  "admin.catalog_saved_count": "この配布元の保存済みファイル {count} 件",
  "admin.catalog_add": "追加",
  "admin.catalog_inspect": "調べる",
  "admin.catalog_version": "バージョン",
  "admin.catalog_file": "ファイル",
  "admin.catalog_checksum": "SHA-256",
  "admin.catalog_pick_file": "ファイルを選ぶ",
  "admin.catalog_size_unknown": "サイズ未取得",
  "admin.catalog_advanced": "詳細設定",
  "admin.catalog_need_source": "配布元を入力してください。",
  "admin.catalog_need_version": "バージョンを選んでください。",
  "admin.catalog_need_file": "ファイルを選んでください。",
  "admin.catalog_need_checksum": "ファイルの SHA-256 を入力してください。",
  "admin.catalog_need_license": "配布元のライセンスを読み、同意してください。",
  "admin.catalog_registered_present": "保存先: {present}/{total} ファイルあり",
  "admin.catalog_registered_partial": "保存先: {present}/{total} ファイルあり（一部不足）",
  "admin.catalog_registered_missing": "保存先: 登録ファイルがありません",
  "admin.catalog_registered_unknown": "保存先: 存在を確認できていません",
  // --- 名前と作例、そしてカテゴリ（ADR 0088）。id は鍵であって名前ではない。---
  // 🔴「分類なし」であって「不明」ではない。画像の行ならファミリー未設定＝CP が有効化を拒む行で、
  // 手を打つべきものが先頭に来る。
  "admin.catalog_family_none": "分類なし",
  "admin.catalog_meta": "配布元から名前を取る",
  "admin.catalog_meta_all": "名前と作例をまとめて取る（{n} 件）",
  "admin.catalog_meta_busy": "配布元のページを読んでいます… {n}/{m}",
  "admin.catalog_meta_done": "{n} 件に名前が入りました（取れなかったもの {f} 件）。",
  // --- 収まるかどうか（ADR 0089）。判定は「重み＋KV キャッシュ」だけで、計算バッファと
  // CUDA コンテキストは数えられない。だから 99% を「収まります」と言わない。
  "admin.fit_fits": "収まります",
  "admin.fit_tight": "ぎりぎり（{p}%）",
  "admin.fit_over": "このクラスでは入りません",
  "admin.fit_over_next": "このクラスでは入りません（{c} なら収まります）",
  "admin.fit_card": "このクラスの VRAM {c} MiB",
  "admin.fit_estimate_note": "重みと KV キャッシュの見積もりです。計算バッファと CUDA コンテキストは含みません。",
  "admin.fit_no_kv": "KV キャッシュは読めなかったので、重みだけで比べています。",
  // チェックポイントには KV キャッシュが無い。同じ文を出すと、無いものを見積もったことになる。
  "admin.fit_estimate_note_weights": "重みの大きさだけの見積もりです。生成中に要る作業領域は含みません。",
  "admin.engines_refit_done": "この段に合わせて窓を書き換えました:",
  "admin.engines_refit_blocked_header": "ヘッダをまだ読めていません（有効化すると読みます）",
  "admin.engines_refit_blocked_weights": "重みの大きさが分からない（ファイルがバイト数を申告していない）か、重みだけでこの段に入りません",
  "admin.engines_refit_blocked_measured": "VRAM を手で宣言してある行なので、こちらでは動かしません",
  "admin.engines_refit_stored_unknown": "通信が切れたため、保存されたかどうか確認できません:",
  "admin.engines_refit_failed": "書き換えが完了しなかったモデル:",
  "admin.engines_refit_stored_unpublished": "窓は保存されましたが、エンジンへの反映指示が失敗しました:",
  "admin.engines_refit_unchanged": "設定は元のままです:",
  "admin.engines_refit_next_start": "反映は次にエンジンが起動したときからです（稼働中のインスタンスは起動時の窓のままです）。",
  "admin.engines_refit_stuck": "この段に合わせられなかったモデル:",
  "admin.catalog_edit_window_fit": "このクラスに収まる最大 {n} にする",
  "admin.fit_kv_from": "KV は {f} のヘッダから読みました（同じ配布元でも版によって少し違います）。",
  "admin.engines_ingest_ctx_ceiling": "上限 {n}",
  // --- 配布元のカード（ADR 0089）。量子化の梯子は 1 押しで開く（開くたびに上流を読むため）。
  "admin.repo_held_count": "取り込み済み {n} 件",
  "admin.repo_ladder_open": "この配布元の他の量子化を見る",
  "admin.repo_ladder_close": "閉じる",
  "admin.repo_ladder_loading": "配布元を読んでいます…",
  "admin.repo_ladder_empty": "この配布元に取り込めるファイルがありません。",
  "admin.repo_ladder_held": "取り込み済み",
  // --- 登録済みの行から「他にどんな形で出ているか」を開く。画像は版、文章はサイズ。---
  "admin.catalog_other_versions": "別バージョン…",
  "admin.catalog_other_sizes": "別サイズ…",
  "admin.catalog_other_note": "取り込むと新しい行になります。入れ替えるときは、新しい行で「これで起動する」を押してから、古い行を「登録を消す」で消してください。",
  "admin.catalog_versions_empty": "この配布元は他のバージョンを出していません。",
  // --- 揃える（ADR 0085 決定 3）。主語は行で、部品ではない。---
  "admin.catalog_complete": "揃える",
  "admin.catalog_complete_busy": "揃えています…",
  "admin.catalog_complete_none": "この行に足りないものはありません。",
  "admin.catalog_complete_attached": "配備がすでに持っていたファイルから宣言しました。ダウンロードは発生していません。",
  // 🔴 ダウンロードとは別の言葉で言う。バケツの中のサーバ側コピーなので転送は起きず、
  // 「取り込み中」と書くと出てこない通信を待たせることになる。
  "admin.catalog_complete_moving": "この行のファイルをバケットの中で移しています（ダウンロードはありません）。完了するとローダーが読めるようになります。",
  "admin.catalog_complete_started": "不足ファイルを取り込んでいます。完了すると自動でこの行に付きます。",
  "admin.catalog_complete_unknown": "このファミリーが読む残りの役割は、この配備には部品表がありません。ファイルを取り込んで足してください。",
  "admin.catalog_complete_note": "この行が読むファイルを、バケットにあるもので埋めます。足りないものだけを取り込みます。",
  "admin.catalog_complete_pick": "使うファイルを選ぶ",
  "admin.catalog_complete_keep": "今のまま",
  "admin.catalog_complete_file_declare": "バケットにあるので宣言します",
  "admin.catalog_complete_file_move": "バケットの中で移します（ダウンロードなし）",
  "admin.catalog_complete_file_download": "取り込みます",
  "admin.catalog_complete_file_choose": "候補が複数あります",
  "admin.catalog_complete_file_unknown": "この役割のファイルが分かりません",
  "admin.catalog_files": "ファイル",
  // --- 計画カード（ADR 0085 決定 4）。役割セレクタも鍵欄も部品チェックも出さない。---
  "admin.catalog_plan_title": "取り込む",
  "admin.catalog_plan_building": "取り込む内容を組み立てています…",
  "admin.catalog_plan_files": "取り込むファイル",
  "admin.catalog_plan_whole": "本体",
  "admin.catalog_plan_action_reuse": "取得なし（配備が持っています）",
  "admin.catalog_plan_action_move": "取得なし（バケットの中で移します）",
  "admin.catalog_plan_action_unknown": "この配備には部品表がありません",
  "admin.catalog_plan_at": "いまの場所 {k}",
  "admin.catalog_plan_total": "ダウンロード合計 {n}",
  "admin.catalog_plan_total_none": "ダウンロードは発生しません。",
  // 計画は見積りで、押すことが購入。押した時点で内容が変わっていたら作り直して見せ直す
  // （ライセンスの同意もやり直し——同意したものと取り込むものが違うため）。
  "admin.catalog_plan_stale": "配布元の内容が変わったので、取り込む計画を作り直しました。もう一度確認してから押してください。",
  "admin.catalog_commercial_yes": "商用可",
  "admin.catalog_commercial_unknown": "商用可否は不明",
  "admin.catalog_started": "取り込みを開始しました。進み具合は「登録済み」タブのバケットで見られます。",
  // 再利用と移設はネットワークを渡らない。「ダウンロード中」と書くと、出てこない通信を待たせる。
  "admin.catalog_started_no_download": "配備がすでに持っているバイト列から作りました（ダウンロードはありません）。「登録済み」タブのバケットで確認できます。",
  // --- バケツ（ADR 0085 決定 2・7）。S3 が持っているものそのもの。---
  "admin.catalog_ledger_title": "バケット",
  "admin.catalog_ledger_note": "このエンジンのプレフィックスにあるオブジェクトです。どの行も宣言していないもの（孤児）と、ローダーが一覧できない場所にあるもの（誤配置）を先に並べます。",
  "admin.catalog_ledger_note_acts": "部品に単体のボタンはありません。付け直すのは、それを読むモデル行の「揃える」です。",
  "admin.catalog_ledger_checked": "確認 {t}",
  "admin.catalog_ledger_empty": "このエンジンのプレフィックスにオブジェクトはありません。",
  "admin.catalog_ledger_unavailable": "バケットの一覧を取得できませんでした。再読み込みしてください。",
  "admin.catalog_ledger_misplaced": "誤配置",
  "admin.catalog_ledger_state_present": "あり",
  "admin.catalog_ledger_state_uploading": "取り込み中",
  "admin.catalog_ledger_state_failed": "失敗",
  "admin.catalog_ledger_state_missing": "バイト列がありません",
  // 🔴 CP は s3:DeleteObject を持たない（ADR 0072 決定 7）。「消す」はタスクの開始で、
  // 実際に消えるまでバケツは同じオブジェクトを返し続ける。押しても何も変わらないように
  // 見えたのがこの言葉が要る理由（af-sandbox 実測）。
  "admin.catalog_ledger_state_deleting": "削除中",
  // 🔴 既にバイト列があるキーに上書きで取り込むと、台帳の state は present のままで job だけが
  // uploading になる。行の操作を state だけで決めると、取り込み中のキーに 登録 が出て 409
  // `already declared by` を返す（af-sandbox 実測）。
  "admin.catalog_ledger_uploading_note": "取り込みのタスクが実行中です。完了するとこの行に宣言が付きます。",
  "admin.catalog_ledger_deleting_note": "削除のタスクが実行中です。完了するとこの行は一覧から消えます（数分かかることがあります）。",
  "admin.catalog_ledger_missing_note": "{m} がこのキーを指していますが、バケットにバイト列がありません。その行を「揃える」と取り直します。",
  "admin.catalog_ledger_orphan": "どの行も宣言していません",
  "admin.catalog_ledger_declared": "宣言: {m}",
  "admin.catalog_ledger_register": "登録",
  "admin.catalog_ledger_delete": "消す",
  "admin.catalog_ledger_delete_note": "このオブジェクトをバケットから削除します。取り消せません。どれかの行が宣言しているものは断られます。",
  "admin.catalog_ledger_registered": "{id} として登録しました。",
  // --- 拒否（ADR 0085 決定 5）。押さえ手と、次に押すものを必ず書く。---
  "admin.catalog_holder": "押さえているのは {k}: {i}",
  "admin.catalog_holder_row": "登録済みの行",
  "admin.catalog_holder_job": "取り込みジョブ",
  "admin.catalog_holder_object": "バケットのオブジェクト",
  "admin.catalog_holder_task": "実行中のタスク",
  "admin.catalog_next_register": "登録",
  "admin.catalog_next_complete": "揃える",
  "admin.catalog_next_replace": "置き換える",
  "admin.catalog_next_forget_row": "行の登録を消す",
  "admin.catalog_next_dismiss_job": "ジョブを消す",
  "admin.catalog_next_wait": "読み直す",
  "admin.engines_ingest_hit_downloads": " DL",
  "admin.engines_ingest_hit_likes": " いいね",
  "admin.engines_ingest_hit_trending": " 話題度",
  "admin.engines_ingest_hit_gated": "gated",
  // 🔴 上流が「できない」と言っていることを、取り込む前に出す。コードは CP が決めた閉じた
  // 集合で、語はここにしか無い（engineRestrictLabel）。
  // ログイン必須は Civitai のメタデータからは分からず、CP が DL URL を HEAD して初めて出る
  // 事実なので、「分からなかった」は空欄——「誰でも DL 可」とは別物として扱う。
  "admin.engines_hit_login_required": "要ログイン",
  "admin.engines_hit_login_note": "このファイルは Civitai のアカウントでログインした人にしか配られません。取り込みは登録済みの Civitai アカウントとして実行されます。登録がなければ 401 で失敗し、あっても投稿者の条件を満たしているかは検索の時点では分かりません。",
  "admin.engines_limit_gated_auto": "gated（規約に同意）",
  "admin.engines_limit_gated_manual": "gated（作者の承認待ち）",
  "admin.engines_limit_noncommercial": "商用不可",
  "admin.engines_limit_credit": "要クレジット表記",
  "admin.engines_limit_no_derivatives": "派生不可",
  "admin.engines_limit_same_license": "同一ライセンスのみ",
  "admin.engines_limit_paid": "有料",
  "admin.engines_limit_early_access": "早期アクセス（期限付き有料）",
  "admin.engines_limit_private": "非公開",
  "admin.engines_limit_generate_only": "サイト内生成専用（DL 不可）",
  "admin.engines_limit_nsfw": "NSFW",
  "admin.engines_limit_poi": "実在人物",
  "admin.engines_limit_minor": "未成年",
  "admin.engines_limit_unscanned": "ウイルススキャン未通過",
  "admin.engines_limit_pickle": "pickle 警告",
  // LoRA のトリガ語。これを入れずに使うと、読み込まれてはいるのに絵が変わらない。
  "admin.engines_hit_trigger": "トリガ語",
  // 公開日と更新日は別の問いに答える（「新しいモデルか」と「まだ手が入っているか」）ので、
  // 同じ行に両方・それぞれラベル付きで出す。片方だけだと、1 年前に公開されて先週まで
  // 更新されているモデルと、先週公開されたモデルの区別がつかない。
  "admin.engines_ingest_hit_published": "公開",
  "admin.engines_ingest_hit_updated": "更新",
  // 元のページへ。CP が組み立てた URL をそのまま開く（Console 側では組まない）。
  "admin.engines_ingest_hit_open_hf": "HF で開く",
  "admin.engines_ingest_hit_open_civitai": "CivitAI で開く",
  // Civitai 自身の年齢制限レベル。どのタブで見つかったかに関係なく全カードに出す——
  // 「Civitai」タブの既定の検索でも 0 でない値が返ることがある（実測）。
  "admin.engines_ingest_hit_nsfw_level": "NSFW {n}",
  // 🔴 モデル側の上限であって、この配備で回せる窓ではない。30B は 262144 と申告するが
  // L4 には入らないので 32768 で走らせている。誰の数字かを言わずに出さない。
  "admin.engines_ingest_ctx_max": "モデルの上限 {n}",
  // --- 推奨パラメータ（作者の説明文から拾ったもの）---
  // 🔴 正規表現が他人の散文から拾った値である。だから引用した一文を必ず隣に出し、押す前に
  // 人が読んで直せるようにする。保存されるのは押したときだけ。
  "admin.engines_params_note": "空欄はファミリーの既定のままです。埋めた欄だけがこのモデルで置き換わります。",
  "admin.engines_params_hint_found": "作者の記述から拾いました（未検証）:",
  "admin.engines_params_hint_apply": "この値を入れる",
  "admin.engines_params_none": "生成パラメータ: 宣言なし（ファミリーの既定のまま）",
  "admin.engines_params_steps": "ステップ数",
  "admin.engines_params_cfg": "CFG",
  "admin.engines_params_sampler": "サンプラー",
  "admin.engines_params_scheduler": "スケジューラ",
  "admin.engines_params_clip_skip": "Clip skip",
  "admin.engines_params_guidance": "Guidance（FLUX.1）",
  "admin.engines_params_shift": "Shift（Anima）",
  "admin.engines_params_weight": "既定の強さ",
  // 表示名（"DPM++ 2M Karras"）でも入る。CP が ComfyUI の語彙へ直し、直せないものは捨てる。
  "admin.engines_params_name_ph": "dpmpp_2m",
  "admin.engines_params_clear": "宣言を消す",
  "admin.engines_params_edit": "パラメータ",
  "admin.engines_params_save": "保存",
  // 🔴 flux1 / klein の CFG は別の摘み（FluxGuidance・蒸留パス）なので、当てても効かない。
  "admin.engines_params_cfg_ignored": "このファミリーでは CFG は使われません（ガイダンスが別の入力です）。",
  "admin.engines_params_clip_skip_note": "clip skip が適用されるのは SD1.5 と SDXL だけです。SDXL は最初から 2 相当なので、絵が変わるのは 3 以上です。",
  // ファミリーの推定。決定 2 のとおり宣言するのは運用者なので、入れておくだけで、外せる。
  "admin.engines_ingest_go": "取り込む",
  // 🔴 押す前に「載るかどうか」を言う。実機で借りた llm は L4（24 GB）に重み 17 GB を載せた
  // あと KV キャッシュ 16 GB で `cudaMalloc failed: out of memory` で落ちた——GPU を買って
  // 4 分後、つまり実費。画面が出していたのはファイル名とサイズだけだった。
  // 重みだけでカードを超える候補は一覧の時点で印を付ける（KV は解決するまで分からないので、
  // 印が無いことは「載る」ではなく「ここでは否定できない」）。
  "admin.engines_ingest_fit_weights": "重み {n} MiB",
  // 🔴 要素型（-ctk/-ctv）は CloudFormation のパラメータでエンジンの表に届かないので読めない。
  // f16 と仮定していることを言い切る（量子化 KV の配備では過大評価＝安全側）。
  "admin.engines_ingest_fit_kv": "KV キャッシュ {n} MiB（{c} トークン・f16 と仮定）",
  "admin.engines_ingest_fit_kv_unread": "KV キャッシュは読めませんでした（この数字は重みだけです）",
  "admin.engines_ingest_fit_card": "合計 {n} MiB / このカード {c} MiB",
  // 🔴 上の「合計」はファイルの足し算で、分割ファミリーでは実際の使用量より大きく出る
  // （テキストエンコーダは encode のあと退避される）。誰かがエンジンの /system_stats を
  // 読んだ族はその数字を出し、運用者が入れる——機械は書かない（ADR 0094 決定 8）。
  // 入れないと梯子は合計で段を選ぶ＝一段高い箱を買う。
  "admin.engines_vram_measured": "この族の実測は {n} MiB（{c} で、{s}・batch {b}・参照 {i} 枚／{f} を測定）",
  "admin.engines_vram_measured_use": "実測値 {n} を入れる",
  "admin.engines_vram_measured_after": "取り込んだあと、行の「編集」で実測 VRAM に入れてください。",
  "admin.engines_ingest_accept": "このモデルのライセンスに同意します（配備の全メンバーの代わりに引き受けることになります）",
  "admin.engines_ingest_gated_no_token": "gated のリポジトリですが、この配備には Hugging Face のトークンがありません。下の「Hugging Face のトークン」で運用者のトークンを登録してください（読むのは取り込みタスクだけです）。",
  // 🔴 Hugging Face の gated とは別物で、こちらには鍵が無い。CivitAI のメタデータは誰にでも
  // 200 を返し、ダウンロードの可否だけが投稿者ごとに分かれる（実機で 5 資産が 200/401/403）。
  // トークン欄を作らない判断なので、「別の資産を選ぶ・手で置いて登録する」を言い切る。
  "admin.engines_ingest_civitai_account_first": "この資産は投稿者がダウンロードを制限しています。資産の検索は匿名ですが、取り込みは登録済みの Civitai アカウントとして実行されます。そのアカウントが投稿者の条件を満たしているかはここでは確かめられません。満たしていない場合は、ダウンロードが 401 で返り、ジョブにその旨が出ます。",
  "admin.engines_ingest_civitai_login": "この資産は、投稿者がログイン済みのアカウントからのダウンロードだけを許しています。この配備には Civitai のトークンが登録されていないため、取り込めません。「APIトークン」から登録するか、別の資産を選んでください。",
  "admin.engines_ingest_gated_accept_first": "gated のリポジトリです。トークンは登録済みですが、そのアカウントがこのリポジトリの条項に同意しているかは Control Plane からは確かめられません（匿名で調べているため）。未同意だと取り込みは 403 で失敗するので、先に Hugging Face のモデルページで同意しておいてください。",
  "admin.engines_comfy_lan": "LAN の ComfyUI（画像エンジン）",
  "admin.engines_comfy_lan_source_panel": "使用中: {url}（このパネルで設定）",
  "admin.engines_comfy_lan_source_env": "使用中: {url}（Control Plane の環境変数 AF_COMFY_URL）",
  "admin.engines_comfy_lan_source_table": "使用中: {url}（この配備のエンジン表）",
  "admin.engines_comfy_lan_source_remote": "使用中: {url}（別の Agent Fleet から借用）",
  "admin.engines_comfy_lan_source_none": "画像エンジンは設定されていません",
  "admin.engines_comfy_lan_unavailable": "画像エンジンはこの配備のエンジンスタックが管理しており、このパネルより優先されます。ネットワーク上の ComfyUI を使うには、スタックからこの役割を外してください。",
  "admin.engines_comfy_lan_url": "URL",
  "admin.engines_comfy_lan_key": "Bearer キー（任意）",
  "admin.engines_comfy_lan_key_keep": "空欄なら保存済みのキーをそのまま使います",
  "admin.engines_comfy_lan_key_set": "キーあり（{who} / {when}）。キーは表示できません。",
  "admin.engines_comfy_lan_key_unset": "キーなし（{who} / {when}）。Authorization ヘッダーは送りません。",
  "admin.engines_comfy_lan_not_saved": "このパネルには何も保存されていません",
  "admin.engines_comfy_lan_save": "接続先を保存",
  "admin.engines_comfy_lan_clear_key": "キーを削除",
  "admin.engines_comfy_lan_remove": "パネルの設定を削除",
  "admin.engines_comfy_lan_fallback_env": "ここで保存した URL は AF_COMFY_URL より優先されます。削除すると画像は {url}（AF_COMFY_URL）に戻ります。",
  "admin.engines_comfy_lan_fallback_table": "ここで保存した URL を削除すると、画像はエンジン表の外部の行 {url} に戻ります。",
  "admin.engines_comfy_lan_fallback_remote": "ここで保存した URL を削除すると、画像は別の Agent Fleet から借りた {url} に戻ります。",
  "admin.engines_comfy_lan_fallback_borrow": "ここで保存した URL を削除すると、AF_REMOTE_ENGINE_URL の借用先が画像エンジンを提供していればそちらが引き継ぎます。画像生成が止まるとは限りません。",
  "admin.engines_comfy_lan_fallback_none": "画像向けにほかの設定が無いため、ここで保存した URL を削除すると画像エンジンは無くなります。",
  "admin.engines_comfy_lan_note": "変更は Control Plane を再起動せずにすぐ反映されます。キーは暗号化して保存され、ここで保存した URL にだけ送られます（環境変数の AF_COMFY_API_KEY は使いません）。",
  "admin.engines_hf_token": "Hugging Face のトークン",
  "admin.engines_hf_token_field": "トークン",
  "admin.engines_hf_token_save": "登録",
  "admin.engines_hf_token_remove": "削除",
  "admin.engines_hf_token_unset": "未登録です。gated ではないリポジトリだけ取り込めます。",
  "admin.engines_hf_token_set": "登録済み（{who} / {when}）。値は表示できません。Control Plane は書き込みだけができ、読み戻す権限を持ちません。",
  "admin.engines_hf_token_stack": "この配備のトークンは CloudFormation のパラメータで設定されています。Console からの登録・削除はできませんが、gated のリポジトリは取り込めます。",
  "admin.engines_hf_token_unsupported": "この配備のエンジンスタックにはトークンの置き場がありません。60-engines を更新すると Console から登録できるようになります。",
  "admin.engines_hf_token_note": "配備全体で 1 つです。値は暗号化して保存し、取り込みのたびに配備の秘密へ書き込みます。読むのは取り込みタスクだけで、エンジンのインスタンスには渡りません。",
  "admin.engines_civitai_token": "Civitai のトークン",
  "admin.engines_civitai_token_field": "トークン",
  "admin.engines_civitai_token_save": "登録",
  "admin.engines_civitai_token_remove": "削除",
  "admin.engines_civitai_token_unset": "未登録です。ログイン必須の資産は取り込めません。",
  "admin.engines_civitai_token_set": "登録済み（{who} / {when}）。値は表示できません。Control Plane は書き込みだけができ、読み戻す権限を持ちません。",
  "admin.engines_civitai_token_unsupported": "この配備のエンジンスタックにはトークンの置き場がありません。60-engines を更新すると Console から登録できるようになります。",
  "admin.engines_civitai_token_note": "配備全体で 1 つで、上の Hugging Face のトークンとは別物です。値は暗号化して保存し、取り込みのたびに配備の秘密へ書き込みます。読むのは取り込みタスクだけで、エンジンのインスタンスには渡りません。",
  "admin.engines_civitai_red": "Civitai Red（NSFW の姉妹ドメイン）",
  "admin.engines_civitai_red_show": "モデル検索に Civitai Red のタブを出す",
  "admin.engines_civitai_red_note":
    "civitai.red は Civitai が NSFW 表示のために分けた姉妹ドメインで、このタブだけが nsfw=true を付けて検索します。作例画像はぼかさずに並びます。内容の絞り込みではありません。既定の Civitai タブにも nsfwLevel が 0 でないモデルは含まれます。取り込みフォームへの civitai.red の URL の貼り付けは、この設定に関わらず通ります。",
  "admin.engines_civitai_red_unavailable": "この配備には Civitai Red がありません。Control Plane に AF_ENGINE_CIVITAI_RED を設定すると、ここで切り替えられるようになります。",
  // 🔴 この赤い行は FLUX 専用ではなくクラス全体に出る（engineCommercialUse が
  // non-commercial / -nc / cc-by-nc を含む名前すべてに `no` を返す）。CC-BY-NC と BFL の
  // 条項では何を縛るかが違い、生成物まで縛るかも一様ではないので、どちらが引き金かは
  // 名指ししない——2 つ挙げて、断定はライセンス本文に委ねる。
  // 元は「メンバーに有料で提供している配備では、入れると運用者自身の違反になります」。
  // 配備を有料で提供する形態が無いので起きない状況を警告していたうえ、S3 に置くこと自体は
  // 商用利用ではない（ADR 0072 決定 10 は「取り込みは拒まない」と決めている）。
  "admin.engines_ingest_noncommercial":
    "🔴 非商用ライセンスです。商用の場面での利用と、生成物の商用利用が制限される場合があります。有効にする前にライセンス本文を確認してください。",
  // The GPU rung this role buys (ADR 0074). The hourly figure comes from the ladder the
  // operator declared, never from a number written here: the box is selectable now.
  "admin.engines_class": "インスタンスクラス: ",
  "admin.engines_class_not_default": "既定と違います",
  "admin.engines_class_reset": "既定に戻す",
  // 提案（ADR 0075 決定 1・8）。梯子に購入形態の欄が付いたもので、**宣言順が試す順**である
  // ——CP は価格で並べ替えないので、パネルも並べ替えない。選択は「既定と違う段」ではなく
  // 「固定」になった: 未選択が自動（次の提案へ落ちる）で、id を選ぶとその提案だけを使う。
  "admin.engines_class_auto": "自動（既定）",
  "admin.engines_class_pinned": "自動ではなく固定されています",
  "admin.engines_class_unpin": "自動に戻す",
  "admin.engines_offers_head": "提案（宣言された順に、上から試します）",
  "admin.engines_offer_buy_od": "オンデマンド",
  "admin.engines_offer_buy_spot": "Spot",
  "admin.engines_offer_now": "いまの提案: ",
  "admin.engines_offer_trail": "試した順: ",
  // 決定 5 の失敗コード。「要求の設定で買えず」と「在庫なし」は待ち方が違う——前者は待っても
  // 直る形をしていないので次の提案へ即座に移り、後者は残りの予算だけ待つ。
  "admin.engines_offer_result_active": "取れた",
  "admin.engines_offer_result_unfulfillable": "要求の設定で買えず",
  "admin.engines_offer_result_insufficient": "在庫なし",
  "admin.engines_offer_result_quota": "クォータ",
  "admin.engines_offer_result_budget": "予算切れ",
  "admin.engines_offer_result_unusable": "要求できない",
  "admin.engines_offer_result_interrupted": "取り上げられた",
  // 中断の受け入れ（ADR 0077 決定 9 の改訂）。運用者が提案を宣言し、管理者がここで
  // 「取り上げられてもよい」と言う。🔴 文面が「インスタンスが止まってもよい」で終わらないのは、
  // 実際に同意しているのが 2 つだから——生成中の応答が失われることと、次の要求がコールド
  // スタートを待つこと。その 2 つを読んだ人だけがチェックを入れられる文面にする。
  "admin.engines_spot_allow": "中断を許容する（Spot のインスタンスを買ってよい）",
  "admin.engines_spot_note":
    "Spot のインスタンスは AWS に取り上げられることがあります。取り上げられると、その時点で処理中の応答は失われ、次の要求はコールドスタート（llm 約 9 分・image 約 3 分）を待ちます。同じ提案が続けて 2 回取り上げられたら、その提案はその回の起動では飛ばされるので、最後は VRAM の合うオンデマンドが使われます。",
  "admin.engines_spot_blocked": "中断の許容が要ります",
  "admin.engines_spot_pin_ignored":
    "固定している提案は Spot なので、いまは使われません（自動選択になっています）。中断を許容するか、別の提案を選んでください。",
  "admin.engines_spot_running":
    "いま動いているインスタンスは Spot です。チェックを外しても取り上げられなくなるわけではありません。次に買うインスタンスから反映されます。",
  "admin.engines_class_pending": "いま動いているのは {t} のインスタンスです。選んだクラスは次に買うインスタンスから反映されます。入れ替えるとコールドスタート 1 回ぶん（llm 約 9 分・image 約 3 分）かかり、旧いインスタンスが退場するまで新しいインスタンスは起動しません。",
  "admin.engines_class_replace": "いま入れ替える",
  // 🔴 The choice is SAVED before it is applied, so a failed apply leaves the picker showing a
  // class the capacity provider does not hold — and picking it again is no change at all. The
  // retry has to be a button of its own, or the only way out is a detour through another class.
  "admin.engines_class_apply_failed": "クラスは保存しましたが、キャパシティプロバイダへの書き込みに失敗しました。次に買うインスタンスはまだ前のクラスのままです: {m}",
  "admin.engines_class_apply_retry": "もう一度適用する",
  "admin.engines_class_vram_ok": "有効なモデルのうち最大は {id} で {n} MiB（{src}）です。このクラスは {m} MiB です。",
  "admin.engines_class_vram_over": "有効なモデルのうち最大は {id} で {n} MiB（{src}）ですが、このクラスは {m} MiB です。載らない可能性があります。",
  "admin.engines_class_vram_unknown": "有効なモデルが必要とする VRAM は分かりません（誰も測っていません）。「収まる」という意味ではありません。",
  // ⚠️ 上の比較は「和」ではなく「最大」である（VRAM に載るのは 1 つずつだから）。llm と
  // sd-server には正確だが、comfy は要求ごとに選んで読み込み済みを保持するので保守的すぎる。
  // 数字を和に変えるのではなく、この 1 文を足す——和にすると毎回警告が出て誰も読まなくなる。
  "admin.engines_class_vram_many": "このエンジンは要求ごとにチェックポイントを選び、読み込んだものを VRAM に保持します。余裕があれば複数が同時に載るので、上の数字はそのうち最大の 1 つです。",
  "admin.engines_vram_src_declared": "実測",
  "admin.engines_vram_src_floor": "重みだけの下限",
  // CP はこの出所も返す（engine_class.go）。どちらのカタログにも語が無く、確認ダイアログは
  // キーを連結して作るので、存在しないキーをそのまま描いていた。
  "admin.engines_vram_src_weights_kv": "重み＋KV キャッシュの下限",
  "admin.engines_vram_src_unknown": "不明",
  "admin.engines_vram_confirm": "{id} は {n} MiB（{src}）を必要としますが、いま選んでいるクラスは {m} MiB です。CUDA は VRAM が足りないと遅くなるのではなく失敗します。量子化やオフロードで載ることもあるので、承知のうえなら続けてください。",
  "admin.engines_vram_confirm_go": "承知のうえで有効にする",
  "admin.engines_model_vram": "VRAM {n} MiB",
  "admin.engines_model_window_context": "コンテキスト",
  "admin.engines_model_window_output": "最大出力",
  "admin.engines_model_vram_edit": "実測 VRAM（MiB）",
  // 行が持ちうるライセンスの事実（ADR 0072 決定 10）。「記録なし」は空白にせず書く——seed の
  // 行はライセンスを知りようがなく、手登録のフォームは訊いていない。取り込んだ行が必ず名前を
  // 出している中の空白は「制限なし」と読めてしまう。
  "admin.engines_model_noncommercial": "非商用",
  // いま VRAM に載っているモデルと、その入れ替わりの回数。1 度に 1 つしか抱えない設計の
  // 価格で、「warm」だけを出していると見えなくなる。
  "admin.engines_warm_model": "VRAM 上: ",
  "admin.engines_model_swaps": "この CP が起きてからのモデル交替: {n} 回",
  // The instance's clock and the service's are different facts: the first is when the EC2
  // instance joined the cluster, the second is when the deployment last changed — which moves
  // without a new instance being bought.
  "admin.engines_since_box": "インスタンスの起動 ",
  "admin.engines_since_service": "サービスの更新 ",
  "admin.engines_up_for": "{d} 経過",
  "admin.engines_stops_at": "自動停止 ",
  "admin.engines_stops_in": "あと {d}",
  "admin.engines_stops_due": "自動停止の時刻を過ぎています（次のコントローラの巡回で止まります）",
  // 秒読みが出せないとき（止まっている＝止めるものが無い）に、方針そのものを言う行。
  // 「10:47 に止まる」とは別の主張で、これが無いと停止中のエンジンの窓を見る手が無い。
  "admin.engines_idle_policy": "誰も使わなくなってから {d}で自動停止します",
  "admin.engines_idle_label": "自動停止まで",
  "admin.engines_idle_minutes": "分",
  "admin.engines_idle_hint": "最後の利用から停止するまでの時間です。起動中の停止を避けるため {m} 分以上にしてください。",
  "admin.engines_recent": "直近 {m} 分の要求: {n} 件",
  // 🔴 数えているのは CP のプロセス内のメモリだけ。入れ替わると 0 に戻るので、
  // 窓ぶん数え切っていないときは必ずそう書く（「最後の要求」は永続化されていて残る）。
  "admin.engines_recent_partial": "※ この CP が数えているのは直近 {d}ぶんだけです",
  "admin.engines_last_demand": "最後の要求 ",
  "admin.engines_history": "稼働実績（14 日）",
  "admin.engines_metric_label": "濃さが表すもの",
  "admin.engines_metric_running": "応答できた時間",
  "admin.engines_metric_up": "インスタンスがあった時間",
  "admin.engines_state_down": "停止",
  "admin.engines_ro_detail": "応答 {run} ・ 起動中 {start} ・ 後始末 {drain}",
  "admin.engines_col_running": "応答",
  "admin.engines_col_starting": "起動中",
  "admin.engines_col_draining": "後始末",
  "admin.engines_dur_hm": "{h} 時間 {m} 分",
  "admin.engines_dur_m": "{m} 分",
  "admin.engines_uptime_none": "この期間に動いていた記録はありません。",
  "admin.engines_uptime_error": "稼働実績を読み込めませんでした。",
  "admin.engines_uptime_note":
    "約 {n} 秒ごとのサンプリングです。「インスタンスがあった時間」には、まだ応答できない起動中（実測 165〜197 秒）と、タスクが消えたあともインスタンスが残っている後始末（実測 427〜477 秒）が入ります。どちらも課金されますが、要求には答えていません。記録を始める前の時間は空白のままで、後から取ることはできません。金額ではありません。",
};
