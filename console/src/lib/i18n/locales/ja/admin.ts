// 日本語 カタログ / ドメイン: admin
// キー接頭辞: admin, tenant, clean
//
// ⚠️ 追記は**自分のドメインのファイルだけ**に行う（ADR 0067 決定 4）。分割前は 4,700 行の
// 1 ファイルで、フロントの並列セッションが全員ここへ追記＝毎回確実に衝突していた。
// ja が正本。新しいキーはここに足し、同じキーを ../en/ の同名ファイルにも足す。
export const admin = {
  // --- 管理（features/settings/AdminTab.tsx。super_admin / tenant_admin）---
  "admin.title": "管理",
  "admin.forbidden": "権限がありません（super_admin のみ）。",
  "admin.mode_sessions": "セッション",
  "admin.mode_usage": "稼働時間",
  "admin.mode_audit": "監査",
  "admin.mode_egress": "通信",
  "admin.mode_mcp": "MCP 配布",
  "admin.mode_tts": "読み上げ",
  "admin.mode_brand": "見た目",
  "admin.brand_title": "この配備の色と名前",
  "admin.brand_note": "同じイメージを複数の環境に配ると、タブもスマホのホーム画面も同じアイコンになります。色とラベルを設定すると、favicon・PWA アイコン・アプリ名で見分けられます。表示だけの設定で、権限やデータには影響しません。",
  "admin.brand_color": "色",
  "admin.brand_label": "ラベル",
  "admin.brand_label_ph": "dev / staging など",
  "admin.brand_label_note": "アプリ名の先頭に「[dev] 」の形で入ります。タブもランチャーも末尾から削るため、前置きです。空にすると付きません。",
  "admin.brand_preview_none": "（なし）",
  "admin.brand_reset": "環境変数に戻す",
  "admin.brand_src_admin": "この画面の設定が有効です（環境変数 AF_BRAND_* の {color} / {label} より優先）。",
  "admin.brand_src_env": "いまは環境変数 AF_BRAND_* の値です。保存するとこの画面の設定が優先されます。",
  "admin.brand_src_default": "いまは既定（同梱の teal・ラベルなし）です。",
  "admin.brand_pwa_note": "保存するとこのタブにはすぐ反映されます。他のタブは再読み込み、インストール済みの PWA は入れ直すとアイコンと名前が変わります。",
  "admin.mode_pool": "スロット",
  "admin.mode_engines": "推論エンジン",
  "admin.engines_none": "この配備は自前の推論エンジンを動かしていません。",
  "admin.engines_state_prefix": "状態: ",
  "admin.engines_models_sep": " / モデル: ",
  "admin.engines_always_on_note": "常時稼働は GPU のインスタンスを止めません（時間単価はインスタンスクラスによります）。用が済んだらオンデマンドへ戻してください。",
  "admin.engines_note": "「無効」にすると、そのエンジンは起動メニューからも generate_image からも消え、要求は 503 で断られます。「オンデマンド」は要求が来たときだけインスタンスを買い、アイドルで自分で止まります。",
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
  "admin.engines_catalog_none_enabled": "有効なモデルがありません。1 つ有効にするまで、このエンジンは起動しません。",
  "admin.engines_model_started": "起動時に読み込む",
  // 状態はバッジで言う。ボタンの文言（「有効にする」）は押したら何が起きるかであって、
  // 今どちらであるかではない。行を薄くして表すのは disabled なコントロールと同じ見た目になる。
  "admin.engines_model_is_on": "有効",
  "admin.engines_model_is_off": "無効",
  "admin.engines_model_enable": "有効にする",
  "admin.engines_model_disable": "無効にする",
  "admin.engines_model_select": "これで起動する",
  // 🔴 選び直しても走っている箱は入れ替えない。抱えているのは起動時に決めた 1 つで、
  // ここで再デプロイすると生成中の要求を殺す（ADR 0072 決定 4）。だから先に言っておく。
  "admin.engines_model_next_start": "選び直しは次の起動から効きます。走っているエンジンは入れ替えません（生成中の要求を殺さないため）。",
  "admin.engines_model_window": "コンテキスト {c} / 出力 {o}",
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
  "admin.engines_model_forget_go": "消す",
  // P4 の取り込み（HF から取ってくる）ではなく、既にバケットに在るファイルを「これは
  // 何か」と書き留めるだけの口。種は役ごとに 1 行しか作らないので、これが無いと
  // 「CloudFormation を触らずに別のチェックポイントへ」の選び先が無い。
  "admin.engines_model_add": "バケットのファイルを登録する",
  "admin.engines_model_add_id": "id",
  "admin.engines_model_add_key": "キー",
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
  // CP は S3 を見られないので、同期の秒数を出せる唯一の出どころが宣言されたサイズ。
  "admin.engines_model_add_bytes": "サイズ",
  "admin.engines_model_add_go": "登録する",
  "admin.engines_model_add_note": "id は利用者が選ぶ名前、キーはバケットの中のパス、説明はエージェントが読む 1 行です。コンテキストウィンドウと出力上限は両方書いたときだけ効きます（片方だけは無視します——出力上限を書かないと 32,000 と読まれ、32k のモデルが 768 トークンになるため）。サイズ（バイト）は「同期 +N 秒」の推定に使うだけで任意です。CP は S3 を見ません（権限を持たせていません）ので、キーの打ち間違いは次の起動時に fetch のログで分かります。登録した行は無効の状態で作られます。",
  // --- 取り込み（ADR 0072 決定 6・P4）---
  // 🔴 解決してから受諾する。ライセンスも gated も見せる前に「同意」を出したら、それは
  // 同意ではない。gated はトークンが無ければここで断る（9 分後の 401 では遅い）。
  "admin.engines_ingest_open": "Hugging Face などから取り込む",
  "admin.engines_ingest_search": "探す",
  "admin.engines_ingest_search_go": "検索",
  "admin.engines_ingest_search_none": "見つかりませんでした。別の言葉で試すか、リポジトリ名を直接入力してください。",
  "admin.engines_ingest_source_hf": "Hugging Face",
  "admin.engines_ingest_source_civitai": "Civitai",
  "admin.engines_ingest_browse_go": "人気を見る",
  "admin.engines_browse_kind_gguf": "LLM（GGUF）",
  "admin.engines_browse_kind_checkpoint": "画像（checkpoint）",
  "admin.engines_browse_note": "閲覧だけです。取り込むには 60-engines を配備してエンジンを作る必要があります——ここで見ているのは Hugging Face の公開情報で、Control Plane はトークンもバケットも使っていません。",
  "admin.engines_ingest_sort_downloads": "DL数",
  "admin.engines_ingest_sort_trending": "話題",
  "admin.engines_ingest_sort_likes": "いいね",
  "admin.engines_ingest_hit_downloads": " DL",
  "admin.engines_ingest_hit_likes": " いいね",
  "admin.engines_ingest_hit_trending": " 話題度",
  "admin.engines_ingest_hit_gated": "gated",
  // 検索結果のカードから取り込みフォームへ入れる操作。「取り込む」ではない——埋めるだけで、
  // 調べる・同意する・取り込む はこの後もそのまま通る。
  "admin.engines_ingest_hit_pick": "これにする",
  "admin.engines_ingest_repo": "リポジトリ",
  "admin.engines_ingest_file": "ファイル名",
  "admin.engines_ingest_resolve": "調べる",
  // ファイル名を持っていない状態が普通なので、「調べる」は最初にリポジトリの中身を聞く。
  "admin.engines_ingest_pick": "選んでください",
  "admin.engines_ingest_no_files": "このリポジトリに、このエンジンが読み込めて sha256 のあるファイルがありません。",
  // 🔴 モデル側の上限であって、この配備で回せる窓ではない。30B は 262144 と申告するが
  // L4 には入らないので 32768 で走らせている。誰の数字かを言わずに出さない。
  "admin.engines_ingest_ctx_max": "モデルの上限 {n}",
  "admin.engines_ingest_go": "取り込む",
  "admin.engines_ingest_accept": "このモデルのライセンスに同意します（配備の全メンバーの代わりに引き受けることになります）",
  "admin.engines_ingest_gated": "gated のリポジトリです。運用者のアカウントで条項に同意済みのトークンを使って取り込みます。",
  "admin.engines_ingest_gated_no_token": "gated のリポジトリですが、この配備には Hugging Face のトークンがありません。下の「Hugging Face のトークン」で運用者のトークンを登録してください（読むのは取り込みタスクだけです）。",
  "admin.engines_hf_token": "Hugging Face のトークン",
  "admin.engines_hf_token_field": "トークン",
  "admin.engines_hf_token_save": "登録する",
  "admin.engines_hf_token_remove": "削除する",
  "admin.engines_hf_token_unset": "未登録です。gated ではないリポジトリだけ取り込めます。",
  "admin.engines_hf_token_set": "登録済み（{who} / {when}）。値は表示できません——Control Plane は書き込みだけができ、読み戻す権限を持ちません。",
  "admin.engines_hf_token_stack": "この配備のトークンは CloudFormation のパラメータで設定されています。Console からの登録・削除はできませんが、gated のリポジトリは取り込めます。",
  "admin.engines_hf_token_unsupported": "この配備のエンジンスタックにはトークンの置き場がありません。60-engines を更新すると Console から登録できるようになります。",
  "admin.engines_hf_token_note": "配備全体で 1 つです。値は暗号化して保存し、取り込みのたびに配備の秘密へ書き込みます——読むのは取り込みタスクだけで、エンジンの箱には渡りません。",
  // 🔴 この赤い行は FLUX 専用ではなくクラス全体に出る（engineCommercialUse が
  // non-commercial / -nc / cc-by-nc を含む名前すべてに `no` を返す）。CC-BY-NC と BFL の
  // 条項では何を縛るかが違い、生成物まで縛るかも一様ではないので、どちらが引き金かは
  // 名指ししない——2 つ挙げて、断定はライセンス本文に委ねる。
  // 元は「メンバーに有料で提供している配備では、入れると運用者自身の違反になります」。
  // 配備を有料で提供する形態が無いので起きない状況を警告していたうえ、S3 に置くこと自体は
  // 商用利用ではない（ADR 0072 決定 10 は「取り込みは拒まない」と決めている）。
  "admin.engines_ingest_noncommercial":
    "🔴 非商用ライセンスです。商用の場面での利用と、生成物の商用利用が制限されえます。有効にする前にライセンス本文を確認してください。",
  "admin.engines_ingest_note": "リポジトリ名（`owner/name`）でも、モデルページの URL を貼っても構いません。sha256・サイズ・ライセンスは CP がその API から読みます。ダウンロードするのは取り込みタスクで、CP は S3 にもトークンにも触りません。取り込めた行は無効の状態で作られます。",
  // 🔴 これは出来事の記録で、カタログではない。モデルを消してもジョブは残る（「この取り込みが
  // 走って完了した」は真であり続ける）ので、見出しと日時を付けて「履歴」と読めるようにする。
  // 日時が無いと、消えたモデルの隣の「完了」が現在の状態と読める。
  "admin.engines_ingest_jobs_head": "取り込みの履歴",
  "admin.engines_ingest_state_pending": "開始中",
  "admin.engines_ingest_state_running": "取り込み中",
  "admin.engines_ingest_state_done": "完了",
  "admin.engines_ingest_state_failed": "失敗",
  // The GPU rung this role buys (ADR 0074). The hourly figure comes from the ladder the
  // operator declared, never from a number written here: the box is selectable now.
  "admin.engines_class": "インスタンスクラス: ",
  "admin.engines_class_not_default": "既定と違います",
  "admin.engines_class_reset": "既定に戻す",
  "admin.engines_class_pending": "いま動いているのは {t} の箱です。選んだクラスは次に買う箱から効きます。入れ替えるとコールドスタート 1 回ぶん（llm 約 9 分・image 約 3 分）かかり、旧い箱が退場するまで新しい箱は起動しません。",
  "admin.engines_class_replace": "いま入れ替える",
  "admin.engines_class_vram_ok": "有効なモデルのうち最大は {id} で {n} MiB（{src}）、このクラスは {m} MiB です。",
  "admin.engines_class_vram_over": "有効なモデルのうち最大は {id} で {n} MiB（{src}）ですが、このクラスは {m} MiB です。載らない可能性があります。",
  "admin.engines_class_vram_unknown": "有効なモデルが必要とする VRAM は分かりません（誰も測っていません）。「収まる」という意味ではありません。",
  "admin.engines_vram_src_declared": "実測",
  "admin.engines_vram_src_floor": "重みだけの下限",
  "admin.engines_vram_src_unknown": "不明",
  "admin.engines_vram_confirm": "{id} は {n} MiB（{src}）を必要としますが、いま選んでいるクラスは {m} MiB です。CUDA は VRAM が足りないと遅くなるのではなく落ちます。量子化やオフロードで載ることもあるので、承知のうえなら続けてください。",
  "admin.engines_vram_confirm_go": "承知のうえで有効にする",
  "admin.engines_model_vram": "VRAM {n} MiB",
  "admin.engines_model_vram_floor": "VRAM 少なくとも {n} MiB（重みだけの下限）",
  // 推定であることを言う。S3 から箱へは実測 104〜147 MB/s で、遅いほうを使っている。
  // ルーターの役では有効なモデルを全部同期するので、これがそのまま次のコールドスタートに乗る。
  "admin.engines_model_sync": "同期 +{n} 秒（推定）",
  // いま VRAM に載っているモデルと、その入れ替わりの回数。1 度に 1 つしか抱えない設計の
  // 価格で、「warm」だけを出していると見えなくなる。
  "admin.engines_warm_model": "VRAM 上: ",
  "admin.engines_model_swaps": "この CP が起きてからのモデル交替: {n} 回",
  // 箱の実時刻とサービスの時刻は別物。前者は EC2 インスタンスが登録された時刻で、
  // 後者はデプロイの状態が最後に動いた時刻＝箱を買い直していなくても動く。
  "admin.engines_since_box": "インスタンスの起動 ",
  "admin.engines_since_service": "サービスの更新 ",
  "admin.engines_up_for": "{d} 経過",
  "admin.engines_stops_at": "自動停止 ",
  "admin.engines_stops_in": "あと {d}",
  "admin.engines_stops_due": "自動停止の時刻を過ぎています（次のコントローラの巡回で止まります）",
  // 秒読みが出せないとき（止まっている＝止めるものが無い）に、方針そのものを言う行。
  // 「10:47 に止まる」とは別の主張で、これが無いと停止中のエンジンの窓を見る手が無い。
  "admin.engines_idle_policy": "誰も使わなくなってから {d}で自動停止します",
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
    "約 {n} 秒ごとのサンプリングです。「インスタンスがあった時間」には、まだ応答できない起動中（実測 165〜197 秒）と、タスクが消えたあともインスタンスが残っている後始末（実測 427〜477 秒）が入ります——どちらも課金されますが、要求には答えていません。記録を始める前の時間は空白のままで、後から取ることはできません。金額ではありません。",
  // 左レールのグループ見出し（ルート）。テナント＝一覧と登録簿、デプロイ全体＝
  // デプロイに 1 つしかない面、横断で見る＝全テナントを跨いで数える面。
  "admin.group_tenants": "テナント",
  "admin.group_deployment": "デプロイ全体",
  "admin.group_across": "横断で見る",
  "admin.all_tenants_back": "すべてのテナント",
  // レールの項目名は短く（本文の見出しは admin.idp_register のまま）。
  "admin.tab_register": "サインイン方法の登録簿",
  "admin.destroy_ws": "Workspace を破棄",
  "admin.destroy_title": "{key} の Workspace を破棄しますか？",
  "admin.destroy_confirm": "破棄する",
  "admin.destroy_body": "home と、ランタイムがこの人のために作ったものを完全に削除します。取り消せません。再招待しても空の Workspace になります。",
  "admin.destroy_locks": "本人がかけた削除ロックも越えます。ロックは home の中にあり、停止中の Workspace からは読めないためです。",
  "admin.destroy_efs": "Fargate では home の実体（EFS のディレクトリ）は残り、課金も続きます。消せなかったものは実行後に表示し、監査ログにも記録します。",
  "admin.destroy_leftovers": "破棄しましたが、次のものは削除できませんでした: {list}",
  "admin.remove_purge": "Workspace と home も破棄する（取り消せません）",
  "admin.remove_purge_warn": "home と、ランタイムがこの人のために作ったものを削除します。再招待しても戻りません。",
  // 後始末の 3 段目（docs/log/61 §61.18）。Workspace を破棄し終えた行にだけ出る。
  "admin.delete_member_row": "メンバーを完全に削除",
  "admin.delete_member_row_title": "{key} を名簿から完全に削除しますか？",
  "admin.delete_member_row_confirm": "完全に削除する",
  "admin.delete_member_row_body": "外した記録ごと、この人の行を削除します。取り消せません。もう一度招待すると、まっさらな新しいメンバーとして始まります。",
  "admin.delete_member_row_gone": "消えるもの: 上限設定・アクセストークン・SSM の設定・定時実行・メモ・通知・セッション共有。",
  "admin.delete_member_row_kept": "残るもの: 監査ログ・クラウド費用・稼働時間。過去の記録と請求は書き換えません。",
  // テナントの削除（super_admin・空のテナントだけ）
  "admin.delete_tenant": "テナントを削除",
  "admin.delete_tenant_title": "テナントの削除",
  "admin.delete_tenant_hint": "空になったテナントだけ削除できます。メンバーが 1 人でも残っている、Workspace が残っている、内部 git リポジトリが残っている場合は拒否します——DB の行は、クラウドやディスクに残った実体への唯一の手掛かりだからです。",
  "admin.delete_tenant_repo_hint": "⚠️ 内部 git リポジトリは、メンバーが名簿に残っているうちに削除してください。最後の 1 人を外すと、リポジトリを削除する画面へ誰も入れなくなります。",
  "admin.delete_tenant_confirm_title": "テナント {slug} を削除しますか？",
  "admin.delete_tenant_confirm": "削除する",
  "admin.delete_tenant_body": "テナントの設定（上限・ログイン規則・接続元制限・サインイン方法・MCP 配布）と、外したメンバーの行を削除します。取り消せません。",
  "admin.delete_tenant_kept": "監査ログ・クラウド費用・稼働時間は残ります（テナント欄は空になります）。",
  // --- テナント配布 MCP（docs/log/48 P4・AdminTab の McpAdminView）---
  "admin.mcp_intro":
    "テナントの全メンバーへ配布する MCP サーバーです。配布できるのはリモート（Streamable HTTP）だけで、コマンドを起動する stdio は配布できません（管理者が全員のコンテナで任意のコマンドを実行できることと等価になるため）。",
  "admin.mcp_distributed": "配布中の MCP サーバー",
  "admin.mcp_none": "配布中の MCP サーバーはありません。",
  "admin.mcp_add": "配布する MCP サーバーを追加",
  "admin.mcp_edit_title": "配布を編集",
  "admin.mcp_save_add": "配布する",
  "admin.mcp_disabled": "無効",
  "admin.mcp_user_secret_badge": "値は各自入力",
  "admin.mcp_secret_policy": "資格情報の扱い",
  "admin.mcp_user_secret": "認証の値は各メンバーが入力する",
  "admin.mcp_headers_hint":
    "値は暗号化保存され、全メンバーへ配布されます。Bearer トークンは Authorization ヘッダに入れてください。",
  "admin.mcp_headers_names_hint":
    "配布するのはヘッダ名だけです。値は各メンバーが自分のワークスペースで入力します。",
  "admin.mcp_user_secret_hint":
    "配布するのは接続先とヘッダ名だけになり、値は各メンバーが自分のワークスペースに入力します。ここで値を配布すると、そのトークンは全メンバーのコンテナ内で平文で読めます。",
  "admin.mcp_url_hint": "MCP エンドポイントの URL。資格情報は URL ではなくヘッダに入れてください。",
  "admin.mcp_enabled_hint": "無効にすると定義は残したまま、どのメンバーにも配布されなくなります。",
  "admin.mcp_restart_note":
    "各メンバーのワークスペースは 5 分ごとに取得します。反映されるのは、その後に起動したセッションからです。",
  "admin.mcp_del_title": "配布を削除",
  "admin.mcp_del_body": "{name} の配布を削除します。各メンバーのワークスペースからは次回の取得時に消えます。",
  "admin.crumb_tenants": "テナント",
  "admin.tenant": "テナント",
  "admin.all_tenants": "全テナント",
  "admin.search": "検索",
  "admin.refresh": "更新",
  "admin.unknown": "(不明)",
  "admin.load_error": "読み込めません",
  "admin.search_ph_sessions": "ユーザー / ラベル / リポジトリ",
  "admin.running_count": "{n} 稼働中",
  "admin.no_running_sessions": "稼働中のセッションはありません。",
  "admin.no_matching_sessions": "一致するセッションがありません。",
  "admin.search_ph_audit": "操作 / 対象 / ユーザー",
  "admin.count_items": "{n} 件",
  "admin.no_audit": "監査ログはまだありません。",
  "admin.no_matching_audit": "一致するログがありません。",
  "admin.mode_label": "モード",
  "admin.egress_enforce_note": "enforce: 許可リスト外の通信を遮断します。先に log-only で実態を確認してから切り替えてください。",
  "admin.egress_logonly_note": "log-only: 観測のみで遮断しません。許可リストを固めてから enforce へ。",
  "admin.egress_proposed": "提案中（要承認）",
  "admin.approve": "承認",
  "admin.reject": "却下",
  "admin.egress_allowlist": "許可リスト（追加分）",
  "admin.egress_entry_ph": "host か .suffix.example.com",
  "admin.egress_reason_ph": "理由（任意）",
  "admin.add": "追加",
  "admin.egress_no_entries": "追加の許可エントリはありません（製品既定の許可のみ有効）。",
  "admin.retire": "取消",
  "admin.egress_observed": "観測された宛先",
  "admin.period": "期間",
  "admin.days_1": "1日",
  "admin.days_7": "7日",
  "admin.days_30": "30日",
  "admin.egress_no_records": "記録がありません（egress プロキシ未設定か、対象期間に通信なし）。",
  "admin.egress_allowed": "{n} 許可",
  "admin.egress_blocked": "遮断",
  "admin.egress_blocked_candidate": "遮断候補",
  "admin.tts_running": "稼働中",
  "admin.tts_starting": "起動中（準備中）",
  "admin.tts_running_waiting": "稼働中（応答待ち）",
  "admin.tts_stopped": "停止中",
  "admin.tts_stopped_or_off": "停止中/未起動",
  "admin.tts_stopping": "停止処理中",
  "admin.tts_engine_label": "VOICEVOX エンジン（ずんだもん）",
  "admin.tts_mode_off": "無効",
  "admin.tts_mode_ondemand": "オンデマンド",
  "admin.tts_mode_on": "常時稼働",
  "admin.enable": "有効",
  "admin.disable": "無効",
  "admin.tts_engine_prefix": "エンジン: ",
  "admin.tts_managed": "（ECS 管理）",
  "admin.tts_external": "（外部管理: 常駐 docker 等）",
  "admin.tts_polly_sep": " ／ Polly: ",
  "admin.tts_polly_ready": "利用可",
  "admin.tts_polly_unset": "未設定",
  "admin.tts_starting_note": "起動には 1〜2 分かかります。準備が整うまで、日本語の読み上げは Polly が代読します（Polly 未設定なら無音）。",
  "admin.tts_stopping_note":
    "無効にしました。読み上げはすでに Polly へ切り替わっています。エンジンの停止は約 1 分後です（押し間違いや、すぐ有効に戻す操作で、2GB の pull と 70〜80 秒の起動を払い直さないための猶予）。この間に有効へ戻せば、停止も再起動も起きません。",
  "admin.tts_ondemand_note":
    "オンデマンド: 読み上げの需要（5 分間で 2,000 文字）が溜まった時点で自動起動し、30 分だれも読み上げなければ自動停止します。起動が終わるまでの日本語は Polly が代読します。自動の起動・停止はすべて監査ログに残ります。",
  "admin.tts_no_engine":
    "この環境には VOICEVOX エンジンがありません（ECS 管理下でもないため、この画面から起動することもできません）。有効にしてもずんだもんへは一切流れないので、無効で固定しています。エンジンを用意すれば自動で操作できるようになります。",
  "admin.tts_disable_note":
    "無効にすると、読み上げはただちに Polly へ回り、AWS では約 1 分後に ECS の desired count が 0 になります（停止中コスト 0）。読み上げ自体はユーザー設定（音声読み上げ）側で ON/OFF します。",
  "admin.tts_dict_title": "テナント共通の読み仮名辞書",
  "admin.saving": "保存中…",
  "admin.tts_dict_ph": "表記=読み（1 行に 1 件）\n例）agent-fleet=エージェントフリート\n# コメント行",
  "admin.tts_dict_note":
    "全ユーザーの読み上げに適用される共通辞書です（1 行に 1 件「表記=読み」、# 始まりはコメント）。各ユーザーが設定（読み上げタブ）の読み仮名辞書に同じ表記を持つ場合は、そのユーザーの指定が優先されます。保存後、他のユーザーには Console の次回ロードから反映されます。",
  "admin.usage_load_error": "読み込みに失敗しました。",
  // --- クラウド費用（docs/log/67 + ADR 0048）---
  // ⚠️ わざと「使用量」と呼ばない。その名前は既に 3 か所で使っている
  //（エージェントのトークン、ワークスペース稼働時間が 2 か所）。ここは金額で、
  // しかも AWS の請求があるデプロイにしか存在しない。
  "admin.mode_cost": "クラウド費用",
  "tenant.tab_cost": "クラウド費用",
  "admin.usage_title": "稼働時間（ワークスペースの占有）",
  "admin.usage_intro":
    "インフラ占有＝ワークスペースが起動していた時間の集計です（Claude 利用料は各自のサブスクで、ここには含みません）。約 5 分ごとのサンプリングのため誤差があります。",
  "admin.from": "開始",
  "admin.to": "終了",
  "admin.apply": "適用",
  "admin.total_running": "合計稼働",
  "admin.members": "メンバー",
  "admin.range": "{from} 〜 {to}",
  "admin.usage_no_records": "この期間の稼働記録はありません。",
  "admin.tenants_list": "テナント一覧",
  "admin.new_tenant": "新規テナント",
  "admin.no_tenants": "テナントがありません。「新規テナント」から作成してください。",
  "admin.member_count_title": "メンバー数",
  "admin.person_count": "{n} 人",
  "admin.running_ws_title": "起動中のワークスペース",
  "admin.running_ws": "{n} 起動中",
  "admin.tenant_limits": "上限 — ワークスペース: {ws} / セッション: {ss}",
  "admin.create_failed": "作成に失敗: {msg}",
  "admin.slug_ph": "slug（英数字）",
  "admin.display_name_ph": "表示名（任意）",
  "admin.create": "作成",
  "admin.limits": "上限",
  "admin.zero_unlimited": "0 = 無制限",
  "admin.max_workspace": "最大ワークスペース数",
  "admin.max_workspace_note": "同時に動く数",
  "admin.max_session": "最大セッション数",
  "admin.max_repos": "最大 内部リポジトリ",
  "admin.max_lfs": "最大 LFS 容量",
  "admin.max_ws_mem": "ワークスペースごとのメモリ上限",
  "admin.per_container": "= {hint}／1 コンテナ",
  "admin.zero_no_tenant_cap": "0 = テナント上限なし",
  "admin.ws_mem_hint_1": "「ワークスペースごとのメモリ上限」は 1 コンテナに割り当て可能なメモリの天井（テナント内の各ユーザー設定はこの範囲に収まります）。0 = テナント上限なし（デプロイ既定 ",
  "admin.ws_mem_hint_2": " と、あればホスト天井 ",
  "admin.ws_mem_hint_3": " のみ）。個々の割当はメンバー詳細で設定し、",
  "admin.ws_mem_hint_bold": "次回のコンテナ起動／作り直しで反映",
  "admin.ws_mem_hint_4": "されます。",
  "admin.idle_autostop": "アイドル自動停止",
  "admin.idle_stop_in": "あと {left} で停止",
  "admin.idle_heading": "自動停止の見通し",
  "admin.idle_stop_at_paren": "（{at}）",
  "admin.idle_stopping_soon": "まもなく停止します（次のスイープ）。",
  "admin.idle_held": "次のものが停止を止めています:",
  "admin.idle_hold_working_row": "セッション {session} がターン実行中",
  "admin.idle_hold_background_row": "セッション {session} が背景作業中（run_in_background / サブエージェント）",
  "admin.idle_hold_repojob_row": "リポジトリを取り込み中（clone / svn checkout）",
  "admin.idle_hold_pin_row": "セッション {session} に「自動停止しない」ピン（残り {left}）",
  "admin.idle_hold_watching_row": "端末で操作中（打鍵または Console の操作が直近にある）",
  "admin.idle_observed": "{at} 時点の観測（最大でスイープ間隔ぶん古い場合があります）",
  "admin.idle_stop_at": "自動停止の予定: {at}（reaper の直近の観測）",
  "admin.idle_off": "自動停止 無効",
  "admin.idle_off_hint": "このテナントでは Workspace の自動停止が無効です（ws_idle_timeout = 0）。",
  "admin.idle_hold_working": "実行中で停止しない",
  "admin.idle_hold_background": "背景作業で停止しない",
  "admin.idle_hold_repojob": "リポジトリ取り込み中で停止しない",
  "admin.idle_hold_pin": "自動停止しないピン",
  "admin.idle_hold_watching": "操作中で停止しない",
  "admin.idle_hold_more": " ほか{n}件",
  "admin.empty_deploy_default": "空 = デプロイ既定に従う",
  "admin.session_halt": "セッション停止まで",
  "admin.ws_stop": "ワークスペース停止まで",
  "admin.interaction_halt": "判断待ちの halt まで",
  "admin.interaction_ph": "空 = 左に従う",
  "admin.interaction_hint": "「判断待ち」= 質問・計画の承認待ち・許可待ち・利用上限メニュー・認証切れ。答えが返るまでコンテナが動き続けるので、通常のアイドルとは別に決められます。畳んでも対話は失われません（再開後にミラーのカードから回答すると届きます）。",
  "admin.idle_ph_30m": "例 30m（空=デプロイ既定 1h）",
  "admin.idle_ph_60m": "例 60m（空=デプロイ既定 2h）",
  "admin.idle_hint_1": "放置された Claude セッションは「セッション停止まで」で停止中（再開可）になり、接続も稼働もないワークスペースは「ワークスペース停止まで」で停止します。書式は ",
  "admin.idle_hint_2": "。空欄はデプロイ既定（セッション 1h／ワークスペース 2h）に従い、",
  "admin.idle_hint_3": " で明示的に無効化します。",
  // home の退避（AF_RUNTIME=ecs-ec2 のみ・ADR 0045 決定 13-2）。ここだけが「利用者の home を
  // 自動で今の置き場から動かす」設定なので、可逆であることと初日が遅くなることを必ず書く。
  "admin.hibernate_title": "使われない home の退避",
  "admin.hibernate_after": "退避するまで",
  "admin.hibernate_ph": "例 720h＝30日（空=デプロイ既定）",
  "admin.hibernate_hint":
    "この期間だれも開かなかった home は snapshot にして、ディスクを解放します。次に起動したときに戻すので失われるものはありませんが、その回の起動は少し長くなり、戻した直後の数時間はディスクが遅くなります。",
  "admin.hibernate_warn":
    "自動で行うのは退避までで、破棄はしません。書式は時間まで（日は 24h の倍数で書きます）。0 と書くとこのテナントでは退避しません。",
  // home の予備（ADR 0045 決定 17）。AZ ごと失う話はこのランタイムにしか無いので、
  // 「なぜ要るのか」を先に書く。RPO の語は使わず「どれだけ巻き戻ってよいか」で言う。
  "admin.backup_title": "home の予備を取る",
  "admin.backup_every": "取る間隔",
  "admin.backup_ph": "例 24h（空=デプロイ既定）",
  "admin.backup_hint":
    "home は 1 つのアベイラビリティゾーンの中にあり、そのゾーンごと失われると home も失われます。予備はゾーンの外に置かれるので、そこから作り直せます。ここで決めるのは「最悪どれだけ巻き戻ってよいか」です。",
  "admin.backup_warn":
    "使用中のまま取るので、電源が落ちた直後と同じ状態の写しになります（起動時に自動では戻しません。戻すのは管理者の操作です）。0 と書くとこのテナントでは取りません。",
  "admin.term_log_title": "ターミナルログの保存",
  "admin.retention": "保持期間",
  "admin.retention_off": "無効（標準の短命履歴のみ）",
  "admin.term_log_hint":
    "有効にすると、端末に表示された出力をワークスペースの永続領域へ保存します。入力操作そのものは記録しませんが、コマンド出力に機密情報が含まれる可能性があります。変更は次回のワークスペース起動から反映されます。",
  "admin.agent_cli_update": "エージェント CLI の更新",
  "admin.allow_self_update": "メンバーがエージェント CLI と rtk を自分で最新へ更新するのを許可",
  "admin.allow_self_update_hint":
    "対象は claude / opencode / codex / Copilot / Antigravity（agy）/ rtk。OFF（既定）は全員がこのデプロイのイメージ版で固定。ON にすると各メンバーが自分の設定で「起動時に最新へ更新」を選べます（コンテナ内 in-place 更新・Stop → Start で反映／戻せます）。",
  "admin.saved": "保存しました",
  "admin.no_members": "メンバーがいません。下のフォームから追加してください。",
  "admin.add_failed": "追加に失敗: {msg}",
  "admin.add_member": "メンバー追加",
  "admin.or_user_key": "または内部識別子（user_key）",
  "admin.checking": "確認中…",
  "admin.running_state": "稼働中",
  "admin.stopped_state": "停止中",
  "admin.super_admin_deploy_title": "super_admin（デプロイ全体）",
  "admin.tenant_admin_paren": "（テナント管理者）",
  "admin.ws_resources": "ワークスペースのリソース",
  "admin.ws_stopped": "ワークスペースは停止中です{suffix}。",
  "admin.ws_stopped_disk_suffix": "（ディスク使用量のみ表示）",
  "admin.res_memory": "メモリ",
  "admin.res_disk": "ディスク",
  "admin.disk_home_sub": "（ホーム使用量）",
  "admin.cpu_sub": "1コア = 100%",
  "admin.sessions_heading": "セッション",
  "admin.no_sessions": "セッションなし",
  "admin.permissions": "権限",
  "admin.super_admin_note_1": "このユーザーはデプロイ全体の super_admin です（env ",
  "admin.super_admin_note_2": " で管理）。",
  "admin.tenant_admin_role": "テナント管理者（tenant_admin）",
  "admin.revoke_admin": "管理者権限を解除",
  "admin.make_admin": "このテナントの管理者にする",
  "admin.tenant_admin_hint_1": "テナント管理者は ",
  "admin.tenant_admin_hint_2": " 内のメンバー管理・リソース閲覧・ワークスペース強制停止・セッション上限設定ができます（テナント作成・上限変更・home掃除・権限付与は不可）。",
  "admin.operations": "操作",
  "admin.force_stop_ws": "ワークスペースを強制停止",
  "admin.set_limits": "上限を設定",
  "admin.clean_home": "home を掃除",
  "admin.ws_cpu": "ワークスペースの CPU",
  "admin.ws_disk": "ワークスペースの作業ディスク",
  "admin.ws_size_preset": "サイズ",
  "admin.ws_size_custom": "カスタム",
  "admin.zero_deploy_default_cpu": "0 = デプロイ既定",
  "admin.ws_disk_hint": "0 = 既定 20 GiB（無料枠）",
  "admin.ws_disk_warn": "作業ディスクは停止すると消えます。永続するのはホームだけです。",
  "admin.ws_cpu_vcpu": "= {n} vCPU",
  "admin.ws_mem_req": "ワークスペースのメモリ（必要量）",
  "admin.ws_slot_lands": "→ {type}（{spec}・専有）",
  "admin.ws_slot_zero": "0 = 最小スロット（{type}）",
  // 上限が入って以降、箱の容量とワークスペースが使える量は別の数になった（ADR 0045 決定 28）。
  // 本人が使えるのは後者なので、そちらを先に出し、箱は括弧で添える。
  "admin.ws_slot_usable": "{n}（箱 {box}）",
  "admin.ws_slot_note": "スロットは 1 人で専有し、タスクに予約を掛けないので箱を丸ごと使えます。この値は箱を選ぶだけです。",
  "tenant.machine_title": "既定のマシン種別",
  "tenant.machine_note":
    "このテナントのメンバーが、自分の指定を持たないときに載るマシンです。メンバー毎の指定はメンバー詳細から行い、そちらが優先されます。",
  "tenant.machine_deploy_default": "デプロイの既定",
  "tenant.machine_member_note":
    "自分で種類を選んでいるメンバーは、ここを変えても影響を受けません。反映は各メンバーの次回起動時です。",
  "admin.roster_spec": "{n} vCPU / {mem}",
  "admin.roster_disk": "ディスク {n}GB",
  "admin.ws_machine": "マシンの種類",
  "admin.ws_machine_tenant_default": "テナントの既定",
  "admin.ws_machine_arch_warn":
    "この種類は CPU の系統が変わります。次回起動時に、ホーム内のこの系統向けでない導入物（各エージェント CLI・node・Chromium など）を入れ直します（数分）。~/repos 配下の node_modules / target / .venv は消えませんが、そのままでは動かないので各自で入れ直してください。",
  "admin.ws_cpu_na": "このランタイムでは CPU を選べません（箱を丸ごと使うため）。",
  "admin.ws_disk_home": "ワークスペースの home（永続）",
  "admin.ws_disk_home_hint": "0 = デプロイ既定 {n} GiB。home の作成時にだけ反映され、あとから縮められません。",
  "admin.ws_disk_quota_hint": "0 = 制限なし。表示用の目安で、強制はされません。",
  "admin.ws_disk_work_hint": "0 = デプロイ既定 {n} GiB",
  "admin.limits_edit_title": "上限の設定",
  "admin.max_sessions_label": "最大セッション数",
  "admin.ws_memory": "ワークスペースのメモリ",
  "admin.eq_hint": "= {hint}",
  "admin.zero_deploy_default": "0 = デプロイ既定",
  "admin.mem_clamp_1": "メモリはテナント上限にクランプされ、",
  "admin.mem_clamp_2": "されます（実行中コンテナには即時反映されません）。",
  "admin.stop_ws_title": "{key} のワークスペースを停止",
  "admin.stop_confirm": "停止する",
  "admin.stop_body": "このメンバーの {slug} のワークスペースコンテナを停止します。",
  "admin.clean_title": "{key} の home を掃除",
  "admin.clean_confirm": "掃除する",
  "admin.clean_body": "このユーザーのワークスペースの home を掃除します。コンテナは停止されます。",
  "admin.clean_keep": "保持: 接続情報 / git 認証 / Claude・Codex ログイン",
  "admin.clean_delete": "削除: repos（未コミット含む）/ キャッシュ / その他 home 配下",
  "admin.grant_title": "{key} を {slug} の管理者にする",
  "admin.grant_confirm": "管理者にする",
  "admin.grant_body_1": "このメンバーに ",
  "admin.grant_body_2": " のテナント管理者権限を付与します。",
  "admin.grant_note": "付与後はこのテナント内のメンバー管理・リソース閲覧・ワークスペース強制停止・セッション上限設定ができるようになります（他テナントには影響しません）。",

  // --- テナント毎のログイン（docs/log/61 §61.9・P3）。3 つの規則は似て非なるもので、
  // とくに「招待できるドメイン」を「使えるドメイン」と読み違えると運用が壊れる。---
  "admin.login_rules": "ログイン規則",
  "admin.login_rules_note": "空欄 = 制限なし",
  "admin.auto_join_domains": "自動参加ドメイン",
  "admin.auto_join_domains_unit": "初回ログイン時にこのテナントへ自動で参加",
  "admin.invite_domains": "招待できるドメイン",
  "admin.invite_domains_unit": "メンバー追加時のガードのみ",
  "admin.login_rules_hint":
    "「招待できるドメイン」はメンバー追加時にだけ効きます。既にメンバーの人は、別ドメインでもそのまま使えます（外すには下のメンバー詳細から「メンバーを外す」）。" +
    "「自動参加ドメイン」は 1 ドメインにつき 1 テナントだけ設定できます。",
  "admin.login_url": "このテナント専用のログイン URL:",

  // --- デプロイの方式＝既定テナントの方式（docs/log/61 §61.17）。P7-0 で、テナントの
  // サインイン方法の一覧に「デプロイ共通」の行として並ぶようになった。表示名を主に、
  // id は <code> で添える（技術識別子を主役にしない）。---
  "admin.providers_none": "このデプロイにはサインイン方法が設定されていません（ログイン画面にボタンが出ません）。",
  // ★ 「0 件」と「読めなかった」を必ず別文言にする。以前は 403 を空配列に潰していて、
  // 権限の無い相手に「設定されていません」と嘘を表示していた（docs/log/61 §61.17.9 ②）。
  "admin.providers_unreadable": "サインイン方法の一覧を読み込めませんでした。権限が無いか、一時的に取得できていません。",

  // --- テナント定義の認証方式（docs/log/61 §61.11・P4）。子会社ごとに Entra が違う場合。
  // 作るのはテナント管理者、有効化はデプロイ管理者（決定 30）。この非対称が本体。---
  "admin.idp_title": "このテナントで使えるサインイン方法",
  "admin.idp_note": "自前の方式の有効化にはデプロイ管理者の承認が必要",
  "admin.idp_hint":
    "このテナントに入るのに使える方法の全部です。デプロイ共通の方式と、このテナント専用に登録した方式が並びます。" +
    "自社の IdP（Entra / Okta / Keycloak など）や GitHub の組織は「サインイン方法を追加」から登録でき、" +
    "登録した時点では「承認待ち」で、デプロイ管理者が承認するまでログイン画面にボタンは出ず、サインインもできません。",
  "admin.idp_none": "このテナント専用の方式はまだありません（上のデプロイ共通の方式は使えます）。",
  "admin.idp_add": "サインイン方法を追加",
  // --- 行ごとの 2 トグル（docs/log/61 §61.17.5）。DB は CSV 2 本のままで、画面だけが変わる。
  // ★ 「出す」は「受け入れる」の従属 — 受け入れていない方式は ON にしても出ない。---
  "admin.idp_accept": "受け入れる",
  "admin.idp_show": "ボタンに出す",
  "admin.idp_deployment_wide": "デプロイ共通",
  "admin.idp_accept_last":
    "最後の 1 つは外せません。すべて外すと「制限なし＝全部受け入れる」の意味になり、絞ったつもりで全開になります。",
  "admin.idp_show_last":
    "最後の 1 つは外せません。すべて隠すとボタンの無いログイン画面になるため、指定ごと無視されます。",
  "admin.idp_show_needs_accept": "受け入れていないので、ログイン画面には出ません。",
  "admin.idp_approve": "承認して有効化",
  "admin.idp_suspend": "停止する",
  "admin.idp_reapply": "承認を申請する",
  "admin.idp_state_pending": "承認待ち",
  "admin.idp_state_active": "有効",
  "admin.idp_state_suspended": "停止中",
  "admin.idp_state_broken": "承認済みだが設定に不備あり",
  "admin.idp_name": "名前",
  "admin.idp_name_hint": "テナント内で使う識別子（英小文字・数字・- _）。例: entra",
  "admin.idp_kind": "サインインの種類",
  "admin.idp_kind_hint":
    "自社の IdP（OIDC）か、GitHub の組織か。GitHub は世界で 1 つの発行元を全テナントで共有するため、" +
    "「どの組織のメンバーか」が自社の人である根拠になります。",
  "admin.idp_kind_oidc": "自社の IdP（Entra / Okta / Keycloak など）",
  "admin.idp_kind_github": "GitHub の組織",
  "admin.idp_orgs": "許可する GitHub 組織",
  "admin.idp_orgs_hint":
    "カンマ区切り（必須）。このいずれかに「アクティブなメンバー」として所属していることが、サインインの条件になります。" +
    "組織側でサードパーティ OAuth App を制限している場合は、組織の管理者がこの OAuth App を承認するまで全員が拒否されます。",
  "admin.idp_github_app_hint":
    "GitHub の設定でこのテナント用の OAuth App を作り、コールバック URL に {url} を登録してから、client_id と client_secret をここに入れてください。",
  "admin.idp_github_domains_note":
    "GitHub が渡すのは本人が検証済みのアドレス 1 件だけです。会社ドメイン以外のアドレスが主アドレスになっている人は、" +
    "ここで落としてください（通すと、その人は既存のワークスペースではなく新しいワークスペースに入ります）。",
  "admin.idp_issuer": "issuer（発行者 URL）",
  "admin.idp_issuer_hint": "IdP の issuer URL。Entra は自社テナントの GUID を含む URL を指定します（common / organizations は tid の指定が必須）。",
  "admin.idp_client_id": "client_id",
  "admin.idp_client_secret": "client_secret",
  "admin.idp_secret_hint": "保存時に暗号化され、画面に表示されることはありません。",
  "admin.idp_secret_kept": "空のままにすると、保存済みの値をそのまま使います。",
  "admin.idp_trust": "email の信頼方法",
  "admin.idp_trust_hint": "この IdP が名乗る email をなぜ信じてよいか。Entra は email_verified を出さないため「issuer 固定」を選びます。",
  "admin.idp_trust_issuer": "issuer が自社テナントに固定されている",
  "admin.idp_trust_email": "IdP が email_verified を返す",
  "admin.idp_domains": "受け入れるメールドメイン",
  "admin.idp_domains_hint":
    "この方式でサインインできるドメイン（必須）。空にはできません — この方式はデプロイ共通の許可リストを使わないため、空だと誰も入れなくなります。" +
    "同じドメインを 2 つのテナントが持つことはできません。",
  "admin.idp_tids": "許可する tenant id（Entra の tid・任意）",
  "admin.idp_tids_hint": "カンマ区切り。issuer が common / organizations の場合は必須です。",
  "admin.idp_link_claim": "同一アカウントの見分け方",
  "admin.idp_link_claim_none": "既定（sub で見分ける）",
  "admin.idp_link_claim_hint":
    "同じ発行元に別のアプリ登録がある場合に使います。Entra の sub はアプリ登録ごとに違う値になるため、本社のボタンとこの方式で同じ人が別アカウント扱いになります。oid を選ぶと同じ人として扱われます。選べるのは IdP が割り当てる値だけで、メールアドレスのように名乗れる値は選べません。変更すると承認のやり直しになります。",
  "admin.idp_label_ja": "ボタンの文言（日本語）",
  "admin.idp_label_en": "ボタンの文言（英語）",
  "admin.idp_repend_hint":
    "issuer / client_id / email の信頼方法・種類・同一アカウントの見分け方を変更したとき、または受け入れるドメイン・tid・GitHub 組織を追加したときは、" +
    "承認がやり直しになります（承認は「この発行元・この組織を、この範囲で信じてよい」に対して与えられたものなので）。",
  // ★ P7-1（docs/log/61 §61.17.6）で「素の /login には効かない」の運用回避は消えた。
  // 残すのは「隠した＝もう使えない」という誤読への一文だけ。
  "admin.hidden_still_accepted_note":
    "★ ボタンに出さない方式も、受け入れは続きます。その方式で入っている人（他テナントとの兼務など）は" +
    "そのまま入れて、このテナントのログイン画面にボタンが出なくなるだけです。",
  "admin.allowed_providers_shared_note":
    "★ 自テナントの方式だけに絞ると、他テナントの方式で入っている兼務の人は、このテナントに切り替えられなくなります" +
    "（同じアドレスでも、別の IdP のアカウントは別のログインとして扱われるため）。" +
    "その人が使う方式は「受け入れる」のままにして「ボタンに出す」だけ外せば、このテナントのログイン画面には出ません。" +
    "受け入れても入れる人が増えるわけではありません — 誰がこのテナントに入れるかを決めるのは名簿です。",
  "admin.login_rules_methods_moved":
    "★ どのサインイン方法を受け入れるか・ログイン画面のボタンに出すかは、「サインイン方法」の面で行ごとに切り替えます。",
  // ★ 停止の順序ガード（docs/log/61 §61.17.4）。拒否ではなく確認 — 停止は「漏れた IdP を
  // 止める」手段でもあるので、常に始めるより速くあってよい。人数は CP の文言を出す。
  "admin.idp_suspend_title": "{name} を停止する",
  "admin.idp_suspend_body":
    "先に、その人たちに別のサインイン方法を紐づけてもらってください（設定 → 個人設定 → アカウント）。" +
    "停止したあとでは、本人が自分で足すことはできません — 紐づけにはサインインが必要で、そのサインインに使うのがこの方式だからです。",
  "admin.idp_suspend_members":
    "この方式しか使ったことのない現役メンバーが {n} 人います。停止するとその人たちが締め出されます。",
  "admin.idp_delete_title": "{name} を削除する",
  "admin.idp_delete_body":
    "このサインイン方法を削除します。この方式で入っていた人はサインインできなくなりますが、" +
    "ワークスペース・home・保存済みの認証情報は残ります。",
  "admin.idp_register": "テナント定義のサインイン方法",
  "admin.idp_pending_count": "承認待ち {n} 件",
  "admin.idp_register_none": "テナントが定義したサインイン方法はまだありません。",
  "admin.idp_register_hint":
    "各テナントが登録した IdP の一覧です。承認は一度きりの点検ですが、IdP 側の設定（セルフサインアップの有効化など）は承認後にも変わり得ます。" +
    "承認済みのものもここに残るので、定期的に issuer と受け入れドメインを見直してください。承認・停止はこの一覧から行えます。",
  "admin.member_removed": "外れています（名簿から削除済み）",
  "admin.remove_member": "メンバーを外す",
  "admin.remove_title": "{key} を {slug} から外す",
  "admin.remove_confirm": "外す",
  "admin.remove_body": "このメンバーを {slug} の名簿から外します。次のリクエストからアクセスできなくなります。",
  "admin.remove_keeps": "ワークスペース・home・保存済みの認証情報は残ります（消すには先に「home を掃除」）。",
  "admin.remove_undo": "戻すには、同じメールアドレスでもう一度「メンバー追加」してください。",

  // --- テナント設定モーダル（テナント管理者の面）。管理モーダル＝デプロイ全体、
  // 個人設定＝自分、に対してここは「自分が管理しているテナント」。移設してきた
  // パネル本体の文言は admin.* のまま（キー改名は移設と別に行う）。---
  "tenant.title": "テナント設定",
  "tenant.back": "テナント設定一覧",
  "tenant.group_tenant": "テナント",
  "tenant.tab_limits": "上限・自動停止",
  "tenant.group_login": "ログイン",
  "tenant.tab_signin": "サインイン方式",
  "tenant.tab_rules": "ログイン規則",
  "tenant.tab_network": "接続元の制限",
  "tenant.net_title": "接続元ネットワーク",
  "tenant.net_on": "制限あり",
  "tenant.net_off": "制限なし",
  "tenant.net_allowed": "許可するネットワーク",
  "tenant.net_allowed_unit": "CIDR か単独アドレスをカンマ区切りで（IPv4/IPv6）。空 = 制限なし。",
  "tenant.net_your_ip": "このデプロイから見えているあなたのアドレス",
  "tenant.net_your_ip_unit": "規則はこの値と照合されます（ブラウザが自分で思っているアドレスではありません）。",
  "tenant.net_ip_unknown": "判定できません",
  "tenant.net_ip_unknown_hint": "この要求の送信元をコントロールプレーンが特定できないため、規則を適用できません。AF_TRUSTED_PROXY_HOPS の設定を運用者に確認してください。",
  "tenant.net_proxy_not_configured": "コントロールプレーンの手前にプロキシがありますが、デプロイがそれを申告していません（AF_TRUSTED_PROXY_HOPS）。このままだと全員がそのプロキシから来ているように見えるため、保存を止めています——絞ったつもりで全員を通す設定になってしまいます。運用者に連絡してください。",
  "tenant.net_scope_hint": "制限されるのはテナントの「利用」で、サイトへの到達ではありません。ログイン画面はどこからでも開けますしサインインも通りますが、一覧に無いネットワークからはこのテナントの中身を開けません。",
  "tenant.net_exempt_hint": "対象外: MCP と内蔵 Git です（本人のワークスペースの中から呼ばれるので、人がどこにいるかを表しません）。これらを止めるにはメンバーシップを無効化してください。デプロイ管理者はこの規則の対象外で、設定を間違えても必ず戻せます。",
  "tenant.net_layers_hint": "これはアクセス制限であってネットワーク防御ではありません（要求はコントロールプレーンまで届き、セッションを検証したあとで拒否されます）。届く前に止めるには、運用者がロードバランサ側で絞ります。",
  // 連携（docs/log/71）— 外部サービス側にテナントが用意した資格情報の登録。
  "tenant.group_integrations": "連携",
  "tenant.tab_git_oauth": "連携アプリの OAuth",
  "tenant.git_oauth_intro": "メンバーの画面に出る「OAuth で接続」が、どの OAuth アプリを使うかを決めます（GitHub / Bitbucket は 接続 > Git、Jira は 接続 > 課題管理）。アプリは各社の GitHub org / Bitbucket ワークスペース / Atlassian に作るものなので、登録するのはテナント管理者です。保存した時点で有効になります（承認は要りません）。",
  "tenant.git_oauth_optional": "未登録でも接続はできます（メンバーがトークンを貼り付ける方式）。ここに登録すると、そのプロバイダに「OAuth で接続」が出るようになります。",
  "tenant.git_oauth_on": "登録済み",
  "tenant.git_oauth_off": "未登録",
  "tenant.git_oauth_client_id": "client_id（Bitbucket は Key）",
  "tenant.git_oauth_client_secret": "client_secret（Bitbucket は Secret）",
  "tenant.git_oauth_secret_kept": "保存済み（空のままなら変更しません）",
  "tenant.git_oauth_secret_unit": "保存時に暗号化され、二度と表示されません。変更するときだけ入力してください。",
  "tenant.git_oauth_redirect": "プロバイダ側のアプリ登録に、このコールバック URL を設定してください:",
  "tenant.git_oauth_no_base_url": "このデプロイには PUBLIC_BASE_URL が設定されていないため、登録するコールバック URL を組み立てられません。登録しても「OAuth で接続」は失敗します（コードグラントの戻り先が無いため）。運用者に PUBLIC_BASE_URL の設定を依頼してください。",
  "tenant.git_oauth_jira_access": "アプリ作成時の Access type は Resource-level を推奨します（認可したサイト 1 つだけに権限が限られます）。Account-level はアカウント内の全サイトに恒久的な権限を渡すことになります。",
  "tenant.git_oauth_bb_scopes": "Bitbucket は認可 URL にスコープを載せないので、コンシューマの Permissions がそのまま権限になります。Account: Read と Repositories: Read/Write（clone / push 用）に加え、課題管理レールに PR を出すなら Pull requests: Read も入れてください。後から足した場合、既に接続済みのメンバーは接続し直しが必要です（古い権限がトークンに焼かれているため）。",
  "tenant.git_oauth_jira_scopes": "Jira は Atlassian の 3LO アプリです（Bitbucket のコンシューマとは別に登録します）。Permissions に Jira API を追加し、read:jira-work / read:jira-user / write:jira-work の 3 つを許可してください（write は「作業の報告をコメントする」に要ります）。offline_access は Permissions の一覧には出てきません —— OAuth 側のスコープで、af が認可 URL に付けるので設定は不要です。",
  "tenant.git_oauth_jira_sharing": "アプリの Distribution で Sharing を有効にしてください。3LO アプリは既定で「開発中」で、そのままだと作成者本人しか認可できません —— 他のメンバーは Atlassian の「You don't have access to this app」で止まり、af には何も返らないので無言で未接続のままになります。有効化には Vendor name・Contact link・Privacy policy URL の入力が要り、これらは認可するメンバーに見えます（個人名や私用アドレスではなく、会社名と問い合わせ窓口を入れてください）。Marketplace には載りません。",
  "tenant.git_oauth_gh_device": "GitHub はデバイスフローを使うため secret もコールバックも不要です。ただしアプリ側で「Enable Device Flow」を有効にしてください（無効だと接続開始で失敗します）。",
  "tenant.git_oauth_where": "アプリの登録先:",
  "tenant.git_oauth_remove": "登録を削除",
  "tenant.summary_note": "テナント全体の上限を決めるのはデプロイ管理者です",
  "tenant.group_manage": "運用",
  "tenant.tab_members": "メンバー",
  "tenant.tab_sessions": "セッション",
  "tenant.tab_usage": "稼働時間",
  "tenant.tab_audit": "監査",
  "tenant.tab_mcp": "MCP 配布",
  "tenant.picker": "テナント",
  "tenant.none": "管理しているテナントがありません。",
  "tenant.forbidden": "このテナントの設定を見る権限がありません。",
  "tenant.rules_readonly_note": "変更できるのはデプロイ管理者だけです",
  "tenant.rules_hint":
    "「自動参加ドメイン」は 1 ドメインにつき 1 テナントだけ設定できます。" +
    "これらの規則そのものを変えるには、デプロイ管理者に依頼してください。",
  "tenant.rules_unset": "未設定（制限なし）",
  "tenant.rules_autojoin_note": "このドメインのメールアドレスの人は、初回ログインでこのテナントに参加します。",
  "tenant.rules_invite_note": "メンバーを追加するときだけ効くガードです。既にメンバーの人には影響しません。",

  // === 掃除パネル（features/sessions/CleanupModal.tsx・docs/log/32）===
  "clean.title": "掃除",
  "clean.open": "掃除を開く（点検・整理）",
  "clean.subtitle": "溜まった停止中セッション・不要な worktree・マージ済みブランチを点検して片付けます。",
  "clean.loading": "点検中…",
  "clean.empty": "片付けられるものはありません。",
  "clean.reload": "再点検",
  "clean.tab_candidates": "掃除候補",
  "clean.tab_archives": "ごみ箱（復元）",
  "clean.stage1_title": "① セッションを整理（停止中 → アーカイブ / shell・ssm → 削除）",
  "clean.stage1_run": "まとめて整理",
  "clean.stage1_run_title": "停止中のセッションをすべて整理します（アーカイブは後からアーカイブ一覧で復帰できます）",
  "clean.stage1_empty": "整理する停止中セッションはありません。",
  "clean.stage1_confirm_title": "停止中セッションをまとめて整理",
  "clean.stage1_confirm_body": "{parts}します。アーカイブしたセッションはアーカイブ一覧から復帰できます。",
  "clean.stage2_title": "② 作業コピー・ブランチを削除",
  "clean.stage2_run": "安全なものを一括削除",
  "clean.stage2_run_title": "「安全」（マージ済み・クリーン等）と判定されたものだけを削除します",
  "clean.stage2_empty": "削除できる作業コピー・ブランチはありません。",
  "clean.stage2_confirm_title": "作業コピー・ブランチを一括削除",
  "clean.stage2_confirm_body": "「安全」と判定された {count} 件を削除します。ブランチとセッションは削除前にごみ箱へ退避されます。worktree の削除は取り消せません。",
  "clean.shelf_n": "アーカイブ済みセッションが {count} 件あります（掃除の対象外）。",
  "clean.open_shelf": "アーカイブ一覧を開く",
  "clean.safety_safe": "安全",
  "clean.safety_review": "要確認",
  "clean.safety_keep": "保持",
  "clean.type_session": "セッション",
  "clean.type_worktree": "worktree",
  "clean.type_branch": "ブランチ",
  "clean.col_target": "対象",
  "clean.col_reason": "理由",
  "clean.select_all_safe": "安全なものを全選択",
  "clean.clear_selection": "選択を解除",
  "clean.selected_n": "{count} 件を選択中",
  "clean.run_selected": "選択したものを片付ける",
  "clean.keep_hint": "保持（稼働中・未コミット/未push）。片付けるには停止か push、または Console で強制削除してください。",
  "clean.collapse_all": "たたむ",
  "clean.expand_all": "ひろげる",
  "clean.group_main": "（本体）",
  "clean.group_other": "その他",
  "clean.group_safe_n": "安全 {count}",
  "clean.action_archive_session": "アーカイブ",
  "clean.action_delete_session": "削除（会話ごと・復元可）",
  "clean.action_delete_worktree": "worktree を削除",
  "clean.action_delete_branch": "ブランチを削除",
  "clean.confirm_title": "選択した {count} 件を片付けますか？",
  "clean.confirm_body": "削除するセッション・ブランチは復元用にごみ箱へ退避されます。worktree の削除は取り消せません（未コミット/未pushは保護されます）。",
  "clean.confirm_do": "{count} 件を片付ける",
  "clean.run_done": "{done} 件を片付けました。{failed} 件は失敗しました。",
  "clean.run_done_ok": "{done} 件を片付けました。",
  "clean.archives_empty": "ごみ箱は空です。",
  "clean.archive_reason_delete_session": "セッション削除",
  "clean.archive_reason_delete_branch": "ブランチ削除",
  "clean.archive_sessions_n": "セッション {count} 件",
  "clean.archive_branches_n": "ブランチ {count} 件",
  "clean.restore": "復元",
  "clean.purge": "完全に削除",
  "clean.restored": "復元しました。",
  "clean.restore_failed": "復元できませんでした。",
  "clean.purge_title": "ごみ箱のアーカイブを完全に削除しますか？",
  "clean.purge_body": "このアーカイブは元に戻せなくなります（容量を回収します）。",
  "clean.purge_do": "完全に削除",
  // 掃除候補の「理由」（Agent は clean.reason.* のキーだけを返す・ADR 0033）。
  "clean.reason.locked": "ロック中（削除保護。解除するまで掃除対象外）",
  "clean.reason.archived": "アーカイブ済み（自動prune対象外。削除で回収可・復元可）",
  "clean.reason.stopped": "停止中（再開可能）。完了していればアーカイブで一覧から整理",
  "clean.reason.ephemeral": "停止中の shell/ssm（残す会話なし）。削除で片付きます",
  "clean.reason.orphan_pane": "orphan（メタ無しの実行中ペイン）。Console でアタッチ/整理",
  "clean.reason.wt_live": "稼働中のセッションがある（先に停止が必要）",
  "clean.reason.wt_locked_session": "削除ロックされたセッションが残っている（解除するまで削除不可）",
  "clean.reason.wt_dirty": "未コミット/未pushの変更あり（push か Console で強制削除）",
  "clean.reason.wt_merged": "マージ済み・クリーン（親に取り込み済み）",
  "clean.reason.wt_unmerged": "クリーンだが未マージ（固有コミットあり。削除でブランチは残るが要確認）",
  "clean.reason.branch_merged": "マージ済みローカルブランチ（親に取り込み済み。削除しても復元可）",
  // 同じ理由の「状態バッジ＋補足」分解版（行の2行目表示用。badge が無いキーは全文へフォールバック）。
  "clean.reason_badge.locked": "ロック中",
  "clean.reason_hint.locked": "削除保護。解除するまで掃除対象外",
  "clean.reason_badge.archived": "アーカイブ済み",
  "clean.reason_hint.archived": "自動prune対象外。削除で回収可・復元可",
  "clean.reason_badge.stopped": "停止中（再開可能）",
  "clean.reason_hint.stopped": "完了していればアーカイブで一覧から整理",
  "clean.reason_badge.ephemeral": "shell/ssm",
  "clean.reason_hint.ephemeral": "残す会話なし。削除で片付きます",
  "clean.reason_badge.orphan_pane": "orphan",
  "clean.reason_hint.orphan_pane": "メタ無しの実行中ペイン。Console でアタッチ/整理",
  "clean.reason_badge.wt_live": "稼働中",
  "clean.reason_hint.wt_live": "セッションが動作中。先に停止が必要",
  "clean.reason_badge.wt_locked_session": "ロック中セッションあり",
  "clean.reason_hint.wt_locked_session": "削除ロックされたセッションが残っています。解除するまで削除不可",
  "clean.reason_badge.wt_dirty": "未コミット/未push",
  "clean.reason_hint.wt_dirty": "push か Console で強制削除",
  "clean.reason_badge.wt_merged": "マージ済み",
  "clean.reason_hint.wt_merged": "クリーン・親に取り込み済み",
  "clean.reason_badge.wt_unmerged": "未マージ",
  "clean.reason_hint.wt_unmerged": "クリーンだが固有コミットあり。削除でブランチは残るが要確認",
  "clean.reason_badge.branch_merged": "マージ済みブランチ",
  "clean.reason_hint.branch_merged": "親に取り込み済み。削除しても復元可",
};
