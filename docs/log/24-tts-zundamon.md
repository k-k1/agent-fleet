# 24. エージェント回答の音声読み上げ（TTS / ずんだもん・Polly）

- 状態: Phase 1〜2 実装済み（2026-07-10, feat/tts-zundamon）。実機での音出し・AWS 実環境（Polly/ECS）の検証は未
- 関連: [decisions/0013-tts-zundamon.md](../decisions/0013-tts-zundamon.md)（決定記録）/
  [decisions/0005-envelope-custodian.md](../decisions/0005-envelope-custodian.md)（秘密情報の封筒暗号）/
  [history/p3-7-aws-adapter.md](p3-7-aws-adapter.md)（ECS アダプタ）/
  [dev/03-control-plane.md](../dev/03-control-plane.md) / [dev/02-console.md](../dev/02-console.md)

チャット（assistant-chat）のエージェント回答テキストを、**ずんだもん（VOICEVOX）**の声で読み上げる。
将来 **AWS Polly** など他エンジンにも広げられるよう、TTS をプロバイダ抽象として設計する。

## 背景

現状 TTS/audio 系のコードは一切ない（`voicevox|tts|speech|audio` grep はゼロ、ヒットは
`speaker`=話者ラベル等の無関係語のみ）。エージェント回答は SSE でトークン単位に流れ、フロントの
単一箇所 `chatStream`（`console/src/core/api/client.ts:255-319`）が受信し、`ChatView` の
`onDelta`（`ChatView.tsx:242`, 増分テキスト蓄積）/ `onDone`（`:244`, 確定テキスト）/
`stop()`（`:259`, 中断）に集約される。読み上げはこの一点にフックできる。

「ずんだもんの声」は実質 **VOICEVOX エンジン**が必須（ブラウザの Web Speech API では出せない）。
エンジンは `POST /audio_query` → `POST /synthesis` で WAV を返す HTTP サーバ（既定 `:50021`、
ずんだもん・ノーマル = `speaker=3`）。

## ゴール / 非ゴール

- ゴール: 日本語のエージェント回答をずんだもんで読み上げる。プロバイダを差し替え可能にし、
  非日本語や engine 不在時は Polly が受け皿になる。句点区切りの逐次再生で低レイテンシ。
- 非ゴール: 音声入力（STT）、感情/抑揚の細かな制御、MirrorView（tmux 転写）の読み上げ、
  リップシンク等。Mirror は対象外、assistant-chat のみ。

## 決定の要旨（→ ADR 0013）

1. **TTS は CP-native**（Agent 非経由）。VOICEVOX / Polly は素の HTTP/AWS SDK 呼び出しで、
   CP の外向き通信は egress 制限外（`control-plane/oauth_google.go:215` が `http.DefaultClient`
   直叩き）。ワークスペース停止中でも動く。
2. **プロバイダ抽象**（`voicevox` / `polly`）を CP に置き、ルーティング（使い分け）も CP が担う。
3. **VOICEVOX エンジンは "CP が指す URL"**。物理配置（同居 docker / 専用箱 / ECS）は
   `AF_VOICEVOX_URL` の差し替えだけで変えられ、CP のハンドラは不変。
4. **Polly 認証は IAM インスタンス/タスクロール**（鍵の保存ゼロ）。CP 側の秘密金庫は新設しない。
5. **使い分けは `auto` 既定**（後述の表）。

## アーキテクチャ

```
ChatView(onDelta) ─buffer─▶ 句点スプリッタ ─sentence─▶ 合成キュー(in-flight 2〜3)
      │                                                    │  POST /api/tts/synthesize
      │                                                    ▼
 AudioContext 連番順で順次再生 ◀─ audio/wav ─ CP: registerTTSRoutes ─┬─ voicevox (cfg.voicevoxURL)
      │                                                            └─ polly    (AWS SDK / IAM role)
 onDone/stop で flush・中断
```

CP の TTS ハンドラは**環境非依存**（URL に投げて WAV を返すだけ）。変わるのは
「URL をどこから得るか / 今 engine が起きているか」だけ。

## プロバイダ抽象（CP: 新規 `control-plane/tts.go`）

`workspace/agent/chat_providers.go` の `chatProviders` map と同型で dispatch する。

```go
type ttsProvider interface {
    // wav = 音声バイト列, mime = "audio/wav" 等
    Synthesize(ctx context.Context, text string, o voiceOpts) (wav []byte, mime string, err error)
    Ready(ctx context.Context) bool // engine 到達性（VOICEVOX の /version 等）。Polly は常に true
}
type voiceOpts struct { voice string; speed float64; lang string }
var ttsProviders = map[string]ttsProvider{ "voicevox": ..., "polly": ... }
```

- `voicevox`: `cfg.voicevoxURL`（`main.go` の `config` 構造体に
  `envOr("AF_VOICEVOX_URL", "http://127.0.0.1:50021")` を追加。既定は dev の docker 公開先＝
  host loopback）へ `audio_query`→`synthesis`。認証なし。
- `polly`: AWS SDK 既定認証チェーン（IAM ロール）。`SynthesizeSpeech` で `OutputFormat=pcm/mp3`。
  日本語ニューラル音声（Takumi / Kazuha）ほか。egress allowlist は `.amazonaws.com` 既登録
  （`control-plane/egress_policy.go:18-31`）。

## API（CP）

`registerTTSRoutes(mux, cfg)` を `control-plane/routes.go:14` の `buildMux` に追加。

- `POST /api/tts/synthesize` — body `{ text, provider?, voice?, speed?, lang? }` → `audio/wav`
  バイト列（バイナリ応答の前例 = `control-plane/git_lfs.go:274` の octet-stream。base64 不要）。
  ステートレス・CP 直。レスポンスヘッダに `X-TTS-Provider`（実際に使ったプロバイダ）を付け、
  フォールバック発生を UI が表示できるようにする。
- `GET /api/tts/status` — `{ providers: { voicevox: {ready}, polly: {ready} } }`。フロントの
  トグル活性/「準備中」表示に使う。

## フロント：句点区切りの逐次再生（新規 `console/src/features/chat/tts.ts`）

- `onDelta`（`ChatView.tsx:242`）で受信文字列を buffer。`[。．！？!?\n]` で**確定した文のみ**
  切り出し、末尾の未完片は buffer に残す。`onDone`（`:244`）で残りを flush。短すぎる断片は結合。
- **合成 in-flight を 2〜3 に制限**（長文で数十並列にしない backpressure）。ただし
  **再生は到着順ではなく文の連番順**に固定（seq index を振り、`AudioContext` の `onended` で次を鳴らす）。
- **文間レイテンシ対策（2026-07-10 調整）**: 待機時間の主因は先読み不足ではなく
  ①VOICEVOX が WAV に焼き込む前後無音（pre/postPhonemeLength 既定 0.1s ずつ ≒ 毎境界 0.2s）と
  ②onended 駆動 start() のイベントループジッタ。対策として CP が audio_query の
  `prePhonemeLength=0.02` / `postPhonemeLength=0.05` を上書きし、フロントは準備済みバッファを
  `AudioContext` の時計で「前の終了時刻 + 間」に先行予約する。間は境界の種類で使い分け:
  句点・改行で確定した文の後は一拍 `SENTENCE_GAP`(0.3s)、読点などでの文中早出しの後は
  `CLAUSE_GAP`(0.08s)（チャット読み上げ
  経路のみ。朗読モードはカラオケハイライトが実再生開始に同期するため onended 駆動のまま）。
  なお LLM の生成が再生より遅い場合の待ち（テキスト律速）は原理的に残る。
  あわせて「submit 時に」のような**英単語と日本語の間の半角スペース**は CP の voicevox 経路で
  除去する（`collapseJaSpaces`。VOICEVOX はスペースをポーズとして合成し読みが途切れるため。
  英単語同士のスペースと全角スペースは残す）。
- **合成キャッシュ（2026-07-11）**: 同一文言＋同一合成条件（provider/voice/speed/enkana/lang）の
  復号済み AudioBuffer をフロントのメモリ内 LRU（`ttsCache.ts` の `makeAudioLru`）に持ち、
  再読み上げ・定型 announce の 2 回目以降は合成もネットワークもなしで即再生。上限はユーザー設定
  `ttsCacheSec`（合計再生秒数: なし/5分≒30MB/15分≒90MB/30分≒180MB、既定 5 分。AgentsTab
  「音声キャッシュ」）で、変更は getter 参照で即追随（0 で無効化＋保持分破棄）。リロードで消える
  （永続化しない）。
- 読み上げ用整形: Markdown 記法・コードブロック（`` ``` `` は読み飛ばし/「コード省略」）・URL 短縮。
  （`console/src/features/chat/MarkdownView.tsx` のレンダ経路とは別に、プレーン化ユーティリティを持つ）
- 中断: `stop()`（`ChatView.tsx:259`）と連動し in-flight fetch abort ＋ 現在 source stop ＋ キュー破棄。
  メッセージ横に 🔊 手動再生ボタンも用意。

## 使い分けルーティング（CP 側・`auto` 既定）

フロントは `providerPref` とテキストを送るだけ、**最終決定は CP**（engine の ready 状態を
知るのは CP のみ = 単一の真実源）。VOICEVOX とずんだもんは**非対称**：ずんだもんは日本語専用・
無料だが要起動、Polly は多言語・常時稼働・従量。**Polly は日本語も可能**なので、日本語での
フォールバックは「言語破綻」ではなく声色が変わるだけ。

| テキスト | engine ready | 選ばれる声（`ttsProvider=auto`） |
|---|---|---|
| 日本語 | ✅ | **ずんだもん**（VOICEVOX, speaker=3） |
| 日本語 | ❌（起動中/無効） | **Polly JP**（Takumi 等・自然にフォールバック、次の文でずんだもん復帰）|
| 非日本語 | — | **Polly**（ずんだもんは非対応）|

- ユーザー設定 `ttsProvider` = `auto`（既定）/ `voicevox` / `polly`。明示 `polly` なら日本語でも常に Polly。
- 言語判定は既存 `outputLanguage`（`workspace/agent/ui_prefs.go:55` `chatOutputLanguage()`）を再利用し、
  新規の言語検出器は持たない（混在時は文単位でヒューリスティック）。

## 設定（`console/src/lib/settings.ts` + `TtsTab.tsx`）

設定モーダルの専用タブ「**読み上げ**」（`TtsTab.tsx`、2026-07-11 に AgentsTab の「セッション」
セクションから分離 — 項目が増えて他のエージェント設定を圧迫したため。`ttsSessionNotify` も同居）。
`OnOff`/`Choice`（`./controls.tsx`）定型。localStorage（`af-display-settings`）＋
`PUT /api/env/ui-prefs` サーバミラー（`workspace/agent/ui_prefs.go`）に自動追随。

- `ttsEnabled`（bool, 既定 false）
- `ttsProvider`（`auto` | `voicevox` | `polly`, 既定 `auto`）
- `ttsVoiceVoicevox`（既定 `3` = ずんだもんノーマル）
- `ttsVoicePolly`（既定 `Takumi`）
- `ttsSpeed`（0.5〜2.0, 既定 1.0）
- `ttsUserDict`（ユーザー読み仮名辞書, 既定 空）: 1 行 `表記=読み`。読み上げ直前にテキストへ
  リテラル置換で適用（英語/日本語/記号どれでも。enkana の ON/OFF に依らず先に当たる）。
  クライアント完結（`ttsText.ts` の `parseUserDict`/`applyUserDict`、`tts.ts` の `submit` で適用）。

**テナント共通辞書（2026-07-11）**: ユーザー辞書と同じ書式の共通辞書を管理者が置ける。
保存先は CP の SettingsStore（キー `tts_dict`、`tts_engine` と同じ deployment-wide 流儀）。
編集＝管理モーダル「読み上げ」パネルの専用エディタ（super_admin、`PUT /api/admin/tts/dict`、
監査 `tts.dict`）。配布＝全ユーザーが `GET /api/tts/dict` で取得（`features/chat/ttsDict.ts` が
起動時に取得してキャッシュ、`effectiveDict()` がユーザー辞書と**マージ**して返す）。
**同じ表記はユーザー辞書が勝つ**（上書き。読みを空にした読み飛ばし上書きも可）。マージは
純関数 `mergeDicts`（`ttsText.ts`・表記長降順を維持・テスト有り）。適用は全読み上げ経路
（チャット/announce/朗読/ミラー）と abbrevCode の辞書優先判定に共通で効く。CP は配るだけで
合成ハンドラは触らない。

VOICEVOX エンジンの URL は**ユーザー設定ではなく CP config**（`AF_VOICEVOX_URL`, デプロイ管理）。

## デプロイ形態

VOICEVOX = 「config で指すバックエンド」＋「差し替え可能なライフサイクル」。CP ハンドラは不変。

| 環境 | エンジンの居場所 | ライフサイクル | URL |
|---|---|---|---|
| 開発 / 自ホスト | 常駐 docker（`voicevox/voicevox_engine:cpu-latest`, GPU 不要, 常駐 ~1GB。`deploy/local/run-voicevox.sh`）| 外部管理 | 既定 `http://127.0.0.1:50021` |
| AWS 本番 | ECS タスク | **管理者トグルで desired 0↔1** | Cloud Map の固定 DNS |

### AWS: 管理者トグルで ECS オンデマンド起動

- 起動/停止 = **ECS Service の desired count を 0↔1**（RunTask 単発より扱いやすい）。停止中コスト 0。
- IAM: CP のロールに `ecs:UpdateService` / `DescribeServices`（必要なら `DescribeTasks`）を追加。
  Polly と同じ CP ロールに相乗り。鍵保存はゼロ。[p3-7-aws-adapter](p3-7-aws-adapter.md) の ECS 構成に連なる。
- アドレッシング: タスク IP は動的 → **Cloud Map（private DNS）で固定名**（例 `voicevox.af.local`）。
  単一タスクなら内部 LB よりコストが軽い。CP はこの固定名を `voicevoxURL` に使う。
- コールドスタート: image pull + 起動で有効化から ready まで概ね **1〜2 分**。per-request でなく
  管理者トグル単位なので許容。CP は `/version`（`Ready()`）で ready まで「準備中」を返し、フロントも
  トグルを準備中表示にする。有効な間は warm 維持。
- 管理者トグルの置き場: 既存の admin 設定ストア（`control-plane/egress.go` の `SettingsStore` /
  `/api/admin/egress/allowlist` 系ルート）と同じ流儀で `tts.enabled` ＋ プロビジョニング状態を持つ。

## ライセンス

VOICEVOX / ずんだもんは利用時に**クレジット表記**（例「VOICEVOX:ずんだもん」）が必要。
TTS 設定画面かフッターに小さく常時表示する。Polly は AWS 利用規約に従う。

## フェーズ計画

- **Phase 1（自ホスト）** ✅ 実装済み: CP-native TTS ルート（`control-plane/tts.go`:
  `/api/tts/synthesize`・`/api/tts/status`、VOICEVOX 2 段呼び出し）、フロント句点逐次再生
  （`console/src/features/chat/tts.ts` + 整形 `ttsText.ts` + `ChatView` フック）、設定トグル
  （`lib/settings.ts` + `AgentsTab`: 有効/話者/速度）、クレジット表記、engine 起動ヘルパー
  （`deploy/local/run-voicevox.sh`）。テスト: `control-plane/tts_test.go`（httptest 偽エンジン）+
  `console/src/features/chat/tts.test.ts`（整形）。使い分けは Phase 1 では voicevox 固定
  （polly 指定は 501）。**残**: 実機 VOICEVOX での目視確認（音が出るか）は未実施。
  **ブラッシュアップ（2026-07-10）**: 読み上げ開始レイテンシ短縮のため、**最初の 1 文だけ**句点を
  待たず読点/一定長で早出しするようにした（`ttsText.ts` の純関数 `firstChunkCut`、`tts.ts` の
  `drain` から使用。2 文目以降は従来どおり句点粒度）。テスト `tts.test.ts` に追加。
  **ユーザー読み仮名辞書（2026-07-10）**: 設定 `ttsUserDict`（1 行 `表記=読み`）を読み上げ直前に
  リテラル置換で適用する汎用辞書を追加（英語/日本語/記号どれでも。長い表記優先・enkana より先に当たる）。
  クライアント完結（CP/API 変更なし）。UI は AgentsTab の TTS 設定内テキストエリア。純関数
  `parseUserDict`/`applyUserDict`（`ttsText.ts`）＋テスト。VOICEVOX のユーザー辞書と同じ発想。
- **Phase 1.5（再生制御・FileView 連携）** ✅ 実装済み: 再生をアプリ全体で 1 本に集約する
  グローバルストア `core/store/tts.ts`（`useTtsStore`: speaking/source/stop）。TopBar に
  「読み上げ中・〇〇」インジケータ兼**停止ボタン**を追加（再生中のみ表示、`app/TopBar.tsx`
  + `topbar.css`）。FileView の選択範囲を読み上げるピルを既存「送る」の隣に追加（`speakText()`、
  `ttsEnabled` 有効時のみ、`FileView.tsx` + `viewer.css`）。過去の回答フッターに読み上げ
  ボタンを追加（共用 `features/chat/TtsReadButton.tsx`、ChatView と MirrorView の各ターン
  フッター、assistant/非 user ターンかつ `ttsEnabled` 時のみ）。実機での音出し確認は未。
  **朗読ビュー ReaderView（2026-07-10）**: 「読む」ための専用ビュー（content kind `read`、
  `layout/types.ts`＋`Pane.tsx`＋`ops.ts`＋`migrate.ts`）を新設。ファイル本文を段落・**文**に分割して
  読みやすい版組で表示し、冒頭から順次読み上げつつ、いま読んでいる**文をカラオケ・ハイライト＋
  自動スクロール**で追従する。一時停止/再開（AudioContext suspend/resume）・停止、**縦書き/横書き
  トグル**（設定 `readerVertical`、`writing-mode: vertical-rl`。縦書き時はホイール↓を横スクロール
  ←＝後続の列へ変換。React onWheel は passive で preventDefault 不可のためネイティブ wheel を
  passive:false で処理）。Markdown も **txt も対応**。
  **原文忠実表示**＝改行・行頭スペースを `white-space: pre-wrap` で保持し、**なろう形式ルビ**
  （`｜漢字《かな》` の明示指定＋`漢字《かな》` の自動ルビ＝直前の漢字連続に付与。半角 `|` は md 表と
  衝突するので全角 `｜` のみ制御記号）を `<ruby>` で描画。読み上げはルビ箇所は**読み（かな）を音声化**。
  分割は純関数 `readerText.ts` の `parseRuby`/`buildReadUnits`（行内は文・行末で区切り、空行/コード
  フェンス内は表示のみ読まない。テスト有り）。**傍点（圏点）ルビ**＝ルビが `・` 等の強調記号だけの
  ときは読み上げは親文字を読む（`｜イ《・》｜カ《・》`→「イカ」）。**記号だけの行**（`＊`/`◇`/`---`
  等の視点切替・区切り）は読み上げ可能な文字を含まないので表示のみで読まない。**選択範囲から再開**＝
  本文を選択すると先頭に「ここから朗読」ピルが出て、その位置の文からカラオケを（再生中でも）再スタート。
  読み上げエンジンは `tts.ts` の
  `startNarration`（合成 `synthToBuffer` を startTts と共有・文 index を `onUnit` で通知）を流用。ReaderView
  は文を React 要素で描くのでハイライトは `activeIndex` state 駆動（旧 FileView 版の DOM クラス後付けを
  廃止）。開き方＝ファイル右クリック「朗読で開く」／FileView の「朗読」ボタン（`kind:read` を openTarget）。
  グローバル 1 本再生・TopBar 停止と相乗り。クライアント完結（CP 変更なし）。実機の音・追従・縦書きは未確認。
  ※初期に FileView へ inline 実装した朗読（`collectNarrationUnits`＋DOM ハイライト）は ReaderView へ集約し撤去。
- **Phase 1.6（バックグラウンドセッションの音声通知・Tier1）** ✅ 実装済み: 稼働中の複数
  セッションが回答/質問を返したら、セッション名を添えて短く音声でお知らせ（設計検討の結論＝
  直列音声なので「全文読み上げ」でなく「アナウンス方式」を採用）。既存の `useSessionNotifications`
  （app 常駐・全セッションの状態を 4s ポーリングし working→idle / →question を検知）に接続。
  多セッション衝突は tts.ts の**アナウンス直列キュー**（`announce()`, 何か再生中なら待つ、
  溜まりすぎは古いものを捨てる）で解消。設定「セッションの音声通知」(`ttsSessionNotify`)。
  現在は通知センターへ統合済み。履歴・既読・利用量リセット判定は membership 単位で
  Control Plane に保存し、OS/TTS の配信設定だけを端末ローカルに残す。再生目的も
  `reading` / `session-notification` / `usage-notification` / `manual` に分け、TOPバーの
  停止操作が該当チャネルだけを OFF にする。全体設計は
  [通知センター](../log/notification-center.md) を参照。
  制約: Console タブが可視の間のみ（状態ポーリングが `document.hidden` で止まるため）。
  未採用: 全セッションの回答**本文**の読み上げ（直列音声＋長文で遅延・混線するため、
  必要なら将来 Tier2=単一セッションのチューニング全文で対応）。実機確認は未。
- **Phase 1.7（英語をカタカナ読み・enkana）** ✅ 実装済み: 英単語を「カタカナ英語」に前処理して
  から VOICEVOX に渡し、ずんだもんの声のまま英語を "それっぽく" 読む。CP 側 `control-plane/enkana.go`:
  CMU 発音辞書（`assets/cmudict.dict.gz`, BSD-2, 遅延ロード）で英単語→ARPABET を引き、ARPABET→
  カタカナ・モーラへ写像（拗音・促音・長音対応、camelCase 分割、全大文字は英字名読み、辞書外は
  綴りのまま）。`/api/tts/synthesize` の `enkana` フラグで有効化。設定「英語をカタカナ読み」
  (`ttsEnglishKana`)。**性質**: CMUdict はアメリカ英語の音写なので、定着した和製カタカナ
  （コーヒー）ではなく音写（カフィー）になる＝“それっぽい”止まり。より自然な和製読みは GPL の
  alkana/bep-eng.dic が持つが Apache-2.0 の本リポジトリと非互換のため不採用。
  **AWS/開発ジャルゴン対応**: CMUdict は技術語を網羅できない（EC2 は数字が読まれず、Dao は辞書外で
  綴りのまま等）ため、手キュレーションのオーバーライド辞書 `control-plane/enkana_dict.go`（`techKana`,
  CMUdict より優先）を併設。加えて「略語＋数字」ルールで EC2→イーシーツー・S3→エススリー（英字塊は
  英字名読み、数字塊は英語数字読み。単独数字は日本語読みのまま）。語を足すには `techKana` に 1 行追加。
  テスト `control-plane/enkana_test.go`（一般語＋技術語）。NOTICE に CMUdict 帰属を記載。実機の音は未確認。
  **辞書拡充（2026-07-10）**: このコンテナの過去セッション記録（`/var/lib/af/claude/projects` の
  transcript）から英単語頻度を抽出し、CMUdict/`techKana` と突合して「高頻度なのに未カバー（綴りママ/
  英字読み止まり）」だった語を洗い出し、一般的な開発語・外部プロダクト名・小文字略語（config/grep/
  tmux/worktree/opencode/codex/voicevox/mcp/css/svg 等 約50語）を `enkana_dict.go` に追加。突合は
  使い捨ての scan テストで実施（コミット対象外）。`TestCorpusTerms` で回帰を固定。
- **Phase 1.8（ミラーのカラオケ朗読・P1）** ✅ 実装済み（2026-07-11）: MirrorView（チャット）の
  各 assistant ターンをカラオケ・ハイライト付きで朗読する。ミラーの回答はポーリングで
  **完結したターンが丸ごと**届く（ストリーミングでない）ため、ChatView の delta 逐次型
  （`startTts`）ではなく ReaderView と同じ `startNarration` 型を採用。新規
  `features/mirror/turnTts.ts`: MarkdownView が innerHTML 描画した**レンダ済み DOM** から
  ブロック（p/h1-h6/li/blockquote 内の段落）を文書順に収集（`collectBlocks`。thinking/ツール/
  plan/question・pre/table/mermaid は対象外）、`textContent` を文分割（`ttsText.ts` の純関数
  `splitSentences`・テスト有り）して朗読。**音声の単位＝文・ハイライトの単位＝ブロック**
  （`.tts-active` クラス＋`scrollIntoView` 追従。文単位ハイライトはレンダ済み HTML のテキスト
  ノード分断で複雑になるため見送り）。ソース（Markdown 文字列）側で分割しないのは marked
  トークン↔DOM の対応維持が脆いため。フッターの読み上げボタンはミラーでは本方式に差し替え
  （`TurnTtsButtons`: 読み上げ中は**一時停止/再開・停止**に切り替わる）。**選択位置から再開**＝
  assistant ターン本文の選択で「ここから読み上げ」ピル（ReaderView と同パターン、選択ブロックの
  先頭文から）。グローバル 1 本再生・TopBar 停止・合成キャッシュと相乗り。クライアント完結
  （CP 変更なし）。実機の音・ハイライト追従は未確認。
  **P2 自動読み上げ（2026-07-11）** ✅: 新しい assistant ターンが届いたら自動でカラオケ朗読。
  設定 `ttsAutoReadMirror`（既定 OFF、AgentsTab「新しい回答を自動で読み上げ」）。
  **アクティブなペインのセッションのみ**（`active` prop でゲート。見ていないセッションの
  `ttsSessionNotify`＝名前の短い告知と完全に相補で二重発声しない）。発火＝ポーリング append の
  検出（`turns` effect で基準 idx より新しい assistant ターンを抽出。初回 tail ロード・
  transcript リセット・セッション切替は基準を取り直すだけで履歴は読まない。sidechain/compact
  除外）。**直列キュー**＝グループ idx 単位で重複なく積み、何か再生中（チャット読み上げ・
  アナウンス・自分）なら `useTtsStore.speaking` の解放か読み上げ終了で次を読む。連続 assistant
  ターンは同じグループに折り畳まれて**育つ**ため、グループごとに読み上げ済みブロック数を持ち
  **増えた分だけ**読む（回答が届くたび続きから）。溜まりすぎ（>4 回答）は古い方から捨てる。
  明示停止（フッター停止・TopBar・他の再生への置き換え）はキューも破棄（`startNarration` の
  `onUnit(null, reason)` に done/stopped を追加して判別、後方互換）。自然終了なら次へ。
- **インラインコードの省略読み（2026-07-11）** ✅: `` `0d882cd` `` のようなコード片を全部読まずに
  省略する（設定 `ttsAbbrevCode`、既定 ON、「読み上げ」タブ）。純関数 `abbrevCode`
  （`ttsText.ts`・テスト有り）: 語が無いハッシュ等＝頭 2 文字＋フィラー語（なんとか/ふがふが/
  むにゅむにゅ）、camelCase・パス等の複数語＝頭一語＋フィラー（3 語以上は＋末尾一語、例:
  `ttsAutoReadMirror` →「tts なんとか Mirror」）。短い語（<6 文字）・純粋な 1 単語・空白入りの
  コマンド・日本語入り・**ユーザー辞書に掛かる表記はそのまま**（辞書優先）。フィラーは
  トークン内容から**決定的に**選ぶ（同一トークン＝同一語尾 → 合成キャッシュが効き聞き直しも安定）。
  適用点＝`plainify` の code オプション（チャット/announce/speakText/朗読 ReaderView）＋
  `turnTts.blockText` の `<code>` 要素（ミラー。レンダ済み DOM にはバッククォートが無いため）。
  注意: 英語回答が Polly 英語音声に行くと日本語フィラーだけ浮く（必要なら lang=en で英語
  フィラーに切り替え可、未実装）。
- **セッションごとの声（2026-07-11）** ✅: 設定 `ttsVoicePerSession`（既定 OFF）。セッション名の
  ハッシュで話者プール（`tts.ts` の `SESSION_VOICES`: VOICEVOX 標準 14 キャラ／Polly 3 声）から
  決定的に割り当て（`sessionVoiceOpts`）。ミラーの読み上げ（手動・自動）とセッション音声通知に
  適用（`startNarration`/`readTurn`/`announce` に voice 上書きパラメータを追加）。ハッシュは
  表示タイトルでなく**セッション名（固定 ID）**で取るので、タイトルを変えても声は変わらない。
  チャットタブ・朗読ビューは選択中の話者のまま。
- **感情スタイルの読み分け（2026-07-11）** ✅: 設定 `ttsEmotion`（既定 OFF）。文（合成 1 回）
  単位で `emotionOf`（`ttsText.ts`・純関数・テスト有り）がエラー/失敗系→ツンツン、成功/完了系→
  あまあまと判定し、`emotionOpts`（`tts.ts`）が speaker 番号を差し替える。スタイル variant を
  持つ話者（ずんだもん・四国めたん・九州そら）のときだけ効き、Polly・ノーマル以外を基準にした
  場合は触らない。キャッシュはキーに voice を含むためスタイル別に共存。
- **ブロック頭の前拍（2026-07-11）** ✅: リスト項目・見出し・引用など「新しいブロックの頭」を
  読む前に一拍（`BLOCK_BEAT`=0.3s）おく。マーカー記号（`-`/`1.`/`#`/`>`）は読まないぶん、
  構造の切れ目を間で表す。判定は純関数 `startsBlock`（`ttsText.ts`・テスト有り。ハイフン語や
  負数には反応しない）。適用: ①ストリーミング（`startTts`）は submit 時に `preGaps` を記録し
  クロック予約に加算、②朗読（`startNarration`）は `preGaps[]` 引数を追加 — ミラー（`turnTts`、
  ブロックが変わる最初の文）と ReaderView（`buildReadUnits` が `preBeat` を付与）が渡す。
  先頭チャンクの前拍は開始遅延になるだけなので無視。ハイライトは前拍の間に先に出る
  （「次はここ」の予告として自然）。
- **確認・質問の読み上げ（2026-07-11）** ✅: 設定 `ttsReadPending`（既定 OFF）。アクティブな
  ペインのセッションが確認待ち（AskUserQuestion／プラン承認／許可要求）になったら内容を読む
  （ポーリングの `pending`/`pendingPlan`/`pendingPermission` の**出現を検知**。開いた時点で既に
  出ていた保留は基準として飲み込み読まない＝ペイン行き来で再読しない。セッション切替でリセット）。
  質問は純関数 `pendingSpeech`（`ttsText.ts`・テスト有り）で「確認です。（質問）選択肢は N つ。
  1、…。2、…。」に組む — 選択肢は**表示ラベルでなく説明文（ツールチップの中身）を優先**して
  読む（画面の表示は省略されがちなため）。プランは定型句、許可は先頭 100 字。`announce` 経由
  （直列・セッションの声）。バックグラウンドのセッションは従来どおり `ttsSessionNotify` の
  短い告知が担当（アクティブ限定なので二重にならない）。
- **再生キャラの表示（2026-07-11）** ✅: TopBar の「読み上げ中・〇〇」に**キャラ名**を追記
  （「読み上げ中・セッション名（ずんだもん）」）。`useTtsStore` に `voice` ラベルを追加し、
  `startTts`/`startNarration` が `voiceCharName`（speaker 番号→キャラ名の写像。スタイル違いは
  同じキャラに束ねる・明示 polly は VoiceId）を登録。auto ルーティングで Polly に落ちた場合
  までは追わない（設定ベースのベストエフォート）。
- **長い回答の要約読み上げ（2026-07-11）** ✅: 設定 `ttsSummaryRead`（既定 OFF）。ミラーの
  **自動読み上げのみ**対象。新着分の読み上げテキスト（`turnTts.turnSpokenText`）が 500 字を
  超えたら、`POST /api/chat/ask`（assistant one-shot・ツールなし）で 2 文要約を生成し、
  `announce` 経由で読む（「要約。…」・セッションの声・TopBar 停止と統合。カラオケは付けない —
  要約文は画面に無いため。フル本文はフッターの読み上げボタンで従来どおり）。生成は 1 本ずつ
  （busy 中はキュー待機）・30s タイムアウト・失敗（ワークスペース停止含む）は全文読みへ
  フォールバック。入力は 6000 字で打ち切り。
- **全ペイン自動読み上げ（2026-07-11）** ✅: 設定 `ttsAutoReadAllPanes`（既定 OFF、
  `ttsAutoReadMirror` のサブオプション）。自動読み上げ・確認読み上げの対象を「アクティブな
  ペイン」から「開いている全チャットペイン」へ広げる。各ペインのキューは 1 本の再生を
  待ち合って直列に読まれる（`ttsAutoPump` のガードを `speaking` だけでなく **store の
  `active`** も見るよう強化 — 合成待ち（登録済みでまだ無音）の再生への割り込みを防ぐ。
  `announce` の pump も同様）。zustand の subscribe は setState 中に同期発火し、プリエンプト
  中（旧 stop→新登録）の一瞬 `active=null` の窓で誤発進するため、ポンプ再開は microtask に
  逃がす。**同じセッションを複数ペインで開いても読むのは先着 1 ペイン**（`turnTts.ts` の
  担当登録 `claimTurnReader`/`isTurnReader`。担当ペインが閉じたら次が引き継ぐ。readOnly
  ペインは登録しない）。停止の意味論を整理: 明示停止（TopBar・フッター）は全ペインの
  キュー＋announce キューを破棄（`tts.ts` の `onTtsStop` 購読）、新再生開始に伴う置き換えは
  `preemptActive()` で区別しキューを温存。ミラーが本文を読むセッションには
  `ttsSessionNotify` の短い告知を重ねない（`hasTurnReader` で判定）。付随修正:
  `startTts` の自然終了で store の `active` を解放するようにした（従来は残置 — `active` を
  見る新ガードが永久待ちになるため）。
- **長文の合成用分割（2026-07-12）** ✅: 句点まで 100 字超のような長い 1 文が「1 回の
  合成」になると、CPU エンジンの合成時間（音声長にほぼ比例）がそのまま無音の待ちになり、
  先読み（MAX_INFLIGHT=2）が息切れする（表で本文が分断され直前の持ち時間が短いと顕著）。
  対策: 純関数 `splitLongSentence`（`ttsText.ts`・テスト有り）— 60 字を超える文は弱い
  区切り（読点・中黒・スラッシュ・ダッシュ・閉じ括弧）で max までのいちばん後ろを探して
  分割（無ければ長さで強制分割・先頭 8 字未満では切らない）。適用は 4 経路: ①ストリーミング
  （submit — 途中の片は CLAUSE_GAP、本来の間は最後の片だけ、前拍は先頭の片だけ）、
  ②ミラー朗読（turnTts — sentHead で文頭の片だけに BLOCK/SENT_BEAT、続き片は間なし。
  ハイライトはブロック単位のまま）、③朗読ビュー（ReaderView — origOf で合成片→表示単位へ
  写像、ハイライト・選択再開はそのまま）、④告知の差し挟み（playInterlude — 要約などの
  長文も片を順に連鎖再生）。
- **アシスタントの声＋組み込み読み補正（2026-07-12）** ✅: ①アシスタント・チャットも
  キャラプールに参加 — 声の優先順位は **明示指定（`assistant.voice`）＞ プール割り当て
  （`ttsVoicePerSession` ON のとき `assistant:<id>` のハッシュ、`voicePoolOpts` に共通化）＞
  設定の話者**。明示指定はアシスタント作成/編集モーダルの「読み上げの声」（自動＋プールの
  キャラ＋Polly。エンジン実カタログ駆動）で選び、エージェント側の assistant レコードに
  `voice` として保存（`workspace/agent/assistants.go` — 保存と echo のみ、解決・合成は
  Console 側）。ストリーミング読み上げと回答フッターの読み上げボタン（`TtsReadButton` に
  voice prop、`speakText` に voice 引数）の両方に適用。②**組み込みの読み補正**
  （`applyBuiltinReadings`）: VOICEVOX が読み間違える開発語を補正 — **ヌメロニム**（i18n→
  インターナショナリゼーション ほか ja.wikipedia「ヌメロニム」の IT 系を網羅: l10n, g11n,
  m17n, a11y, o11y, i14y, c14n, n11n, d11n, p13n, v12n, tr8n, e2e, k8s。単語境界・大文字
  小文字不問。数字を数として読ませない）、**大文字の IT → アイティー**（CMU 辞書の
  it=イット に食われるため。大小を区別し小文字の代名詞 it は触らない）、「空＋カタカナ語」
  （空レポ・空リスト等）は規則で「から」、空文字（列）/空配列/空要素/空判定/空行
  （からぎょう）は個別エントリ。ユーザー/テナント辞書の**後**に適用するので同じ表記は
  ユーザー定義が勝つ。読み整形は `applyReadings`（辞書 → 補正 → 助詞の小休止）に一本化し
  3 経路共通。個別の読み間違いは従来どおり読み仮名辞書（ユーザー/テナント）でも直せる。
  ③enkana 辞書に id/origin/repo（レポ）/repos（レポズ）。④管理モーダルのテナント辞書
  textarea にテーマ配色（`.ds-userdict` が自前で配色を持つ — admin-surface には
  settings-modal のフォーム配色が及ばないため）。
- **間の 3 段化・助詞の小休止・生成中表示・再生のビュー非依存（2026-07-12）** ✅:
  ①「間」を 3 段に — 改行・段落・ブロック頭は一拍（`SENTENCE_GAP`/`BLOCK_BEAT`=0.3）、
  **文中の句点（。！？）はより短い一拍（`SENT_BEAT`=0.15）**、読点早出しは `CLAUSE_GAP`。
  ストリーミングは句点チャンクを 0.15 にし、改行だけの断片が来たら直前チャンクの間を行間へ
  格上げ（「…た。\n」の段落末だけ一拍になる）。ミラー朗読は同一ブロック内の文境界に
  SENT_BEAT、朗読ビューは純関数 `readPreGaps`（`readerText.ts`・テスト有り）— マーカー行と
  「文が終わった後の新しい行」（段落）は 0.3、行内の句点は 0.15、**文の途中の改行
  （ハードラップされた散文）は 0**（`ReadUnit.lineHead` と直前文の終わり方で判別）。
  ②助詞の小休止（設定 `ttsParticlePause`・既定 ON）: 「を・は・で・に・と」＋漢字の境界に
  読点を挿入して「息継ぎ」を作る（`pauseParticles`・純関数・テスト有り。文中は 1 合成の
  内側なので再生ギャップでは作れない。辞書適用の後・合成直前に適用、表示には無関係）。
  ③TopBar のピルに**生成中表示**: 最初の音が鳴る前の合成待ちは loading スピナー＋
  「音声を生成中・…」（store に `preparing`。startTts は最初の submit〜最初のスケジュール、
  朗読は開始キック〜最初のユニット再生）。④ミラーの再生はビュー非依存に — ターミナルへの
  切替・ペインを閉じても再生継続（アンマウントでは止めず、セッション切替だけで止める。
  操作は TopBar の停止）。⑤enkana 辞書に `origin → オリジン` を追加。
- **朗読中の告知差し挟み＋朗読の声の即時切替（2026-07-12）** ✅: ①長い朗読（ファイル・
  長文ターン）中でもセッション通知・確認の告知を待たせない — `startNarration` がユニット
  境界で announce キューを覗き（`takeAnnounce`）、次のユニットの前にその場で読む
  （`playInterlude`。同じ AudioContext・同じ adapter なので一時停止/停止は朗読と一体。
  再生中は TopBar のラベル/声を告知側に差し替えて戻す。pumpAnnounce は再生中動かないので
  二重再生なし）。②朗読ビューの声セレクトを再生中に変えると、
  **いま鳴っている文はそのまま・次の文から**新しい声になる（`NarrationHandle.setVoice` —
  未再生の先読み合成を捨てて cursor から作り直し、in-flight は abort＋エポックで古い声の
  結果を無効化。中断・巻き戻しなし）。あわせてミラーのターンフッターの読み上げ
  操作（読み上げ/一時停止/再開/停止）を**アイコンのみ**に（狭いペインでラベルが崩れるため。
  意味は title）。enkana 辞書に `id → アイディー` を追加（CMUdict が /ɪd/=イドで引くため）。
- **キャラクター設定（2026-07-12）** ✅: ユーザーごとに「使うキャラ・キャラごとの基準
  スタイル（あまあま等）・キャラ別速度」を選べる（設定「読み上げ」タブ「声」内の
  キャラクターリスト。設定キー `ttsVoicePool: Record<キャラ名, {use, style, speed}>`、
  未設定キャラの既定 = 静的プール SESSION_VOICES の 14 キャラのみ ON）。行ごとに **▶ 試聴**
  （`previewVoice` — 定型文を通常の再生パイプで読む。TopBar 停止・合成キャッシュと統合）。
  **一覧はエンジン実データ駆動**: CP に `GET /api/tts/speakers`（VOICEVOX `/speakers` の
  プロキシ。トーク用スタイルのみ・speaker 番号は文字列化・60s キャッシュ・テスト有り）を
  追加し、クライアントは `ttsSpeakers.ts`（30s 負キャッシュ付きの遅延取得）→
  `voiceCharacters()`（カタログ or 静的フォールバック）→ `activeVoicePool()`（ttsVoicePool
  適用後の実プール）で解決。これにより **speaker 番号の静的持ちが実エンジンとずれる問題が
  構造的に解消**（前項の「要実機照合」は不要になった — UI に出る番号は常に実エンジン由来）。
  波及: セッション声（`sessionVoiceOpts`）は有効キャラのプールから選ぶ（全 OFF なら設定の
  話者のまま）。朗読ビューの声セレクトは `readerVoiceChoices()`（有効キャラ＋スタイル・
  キャラ別速度を反映。保存済みの声がプールから外れたら「設定の話者」扱い）。感情読み分けの
  variant はカタログのスタイル名から導出（happy=あまあま/わーい/喜び/たのしい/楽々/元気/
  うきうき、angry=ツンツン/おこ/ツンギレ/不機嫌/怒り。ノーマル以外を基準にしたキャラは
  従来どおり触らない）。TopBar のキャラ名表示もカタログ優先。エンジン停止中は静的
  フォールバック一覧の表示＋注記（編集は可能）。
- **セッション声の衝突緩和（2026-07-12）** ✅: 複数セッションで同じ声（九州そら）が重なった
  報告を受けての 2 点。①`sessionVoiceOpts` のハッシュを剰余の前に **xor 折り畳み**
  （`h ^= h >>> 16`）— 素の `h % N` は下位ビットしか見ず、31 ≡ -1 (mod 8) で実質「文字コードの
  交代和」となり、似た形式のセッション名（共通プレフィックス＋数字。末尾 1↔9・0↔8 は必ず同声）
  で偏っていた。②話者プールを 8 → **14 キャラ**に拡張（追加: 玄野武宏・白上虎太郎・青山龍星・
  WhiteCUL・ナースロボ＿タイプT・櫻歌ミコ。男声 2 で聞き分け向上。武宏・虎太郎は喜び/怒りの
  スタイル variant 付きで感情読み分けも有効。青山龍星の感情スタイルは新しめの speaker ID
  なので base のみ）。朗読ビューの声セレクトもプール由来なので同時に 14 キャラになる。
  既存セッションの声の割り当ては変わる（マージ前・実機確認前なので影響なし）。なお 8 声時代は
  完全一様でも誕生日問題で 5 セッション中どこかが重なる確率が約 8 割あった — 14 声で約 5 割に
  下がるが、同時セッションが増えれば重なり自体は原理的に残る。**追加キャラの speaker ID は
  実エンジン（`GET /speakers`）で未検証** — 実機確認時に要照合。
- **裸のハッシュの省略読み（2026-07-12）** ✅: バッククォートで括られていない生ハッシュ
  （`2a20133` 等）も省略読み（`ttsAbbrevCode` に相乗り・新設定なし）。地の文に混ざるため
  対象は「16 進ハッシュにしか見えない」トークンに厳格化 — 小文字 16 進のみ・7 文字以上
  （git 短縮ハッシュの下限）・**数字と英字の両方を含む**（英字のみ → facade/deadbeef 等の
  英単語を守る。数字のみ → トークン数・タイムスタンプ等の長い数値はそのまま）・UUID
  （8-4-4-4-12）は構造で判定。純関数 `isBareHash`（`ttsText.ts`・テスト有り）＋ `plainify`
  の地の文ステップ 1 か所で 3 経路（チャット/ミラー朗読/朗読ビュー）すべてに効く。読み方は
  abbrevCode のハッシュ枝（頭 2 文字＋フィラー・辞書優先）を共用。あわせて abbrevCode 側も
  ハッシュを語切り出しより先に判定するよう修正（長い SHA が偶然の英字連続を「語」と誤認して
  「fac なんとか aef」のように読まれていたのを「b2 なんとか」に統一）。
- **朗読ビューの声選択（2026-07-11）** ✅: 設定 `readerVoice`（既定 "" = 設定の話者）。
  ReaderView ヘッダーに「声」セレクトを追加（VOICEVOX 標準 14 キャラ＋Polly 3 声、
  `READER_VOICE_CHOICES`/`voiceChoiceOpts`（`tts.ts`）が `TtsOptions` 上書きへ解決して
  `startNarration` に渡す）。VOICEVOX キャラ選択時は provider を `auto` に上げる —
  エンジン不在時は Polly が代読し、復帰したら選んだキャラに戻る。Polly 選択は明示 polly。
  変更は次の朗読開始から適用。選択は設定として永続（他ファイル・他ブラウザにも追随）。
  - **Polly プロバイダ**（`control-plane/tts_polly.go`）: SDK 既定チェーン（IAM ロール、鍵保存ゼロ）。
    出力 MP3（フロントの `decodeAudioData` がそのまま復号するので UI 変更不要）、速度は SSML
    `<prosody rate>`、テキストは XML エスケープ。region は `AF_POLLY_REGION` →
    `AF_ECS_REGION` → `AWS_REGION` の順（未設定なら not-ready = dev では自然に voicevox 専）。
    engine は `AF_POLLY_ENGINE`（既定 neural）。話者は明示 > 言語別既定（ja=Takumi / en=Joanna）。
  - **auto ルーティング**（`tts.go` の純関数 `chooseTTSProvider` + `ttsProvider` インターフェース/map
    dispatch）: 上の表のとおり。言語は設定 `outputLanguage` をフロントが `lang` として送るだけで
    新規言語検出なし。enkana は **voicevox に決まったときだけ**適用（Polly は英語をそのまま読める）。
    voicevox の Ready は 4s TTL キャッシュ（文ごとの /version 連打を避ける）。実際に使った
    プロバイダは `X-TTS-Provider` ヘッダで返す。受け皿不在（Polly 未設定×engine 停止）は
    voicevox に落として 502 を返す。
  - **ECS オンデマンド**（`tts_ecs.go` + `GET/PUT /api/admin/tts`）: `AF_TTS_ECS_SERVICE`
    （cluster/region は `AF_TTS_ECS_*` → `AF_ECS_*` に相乗り）を設定すると管理下になり、
    管理者トグル（super_admin・AdminTab「読み上げ」）で desired count 0↔1。CP ロールに要
    `ecs:DescribeServices`/`UpdateService`。managed 時の enabled は desired count が真実源、
    非管理（dev）は SettingsStore の `tts_engine`（egress_mode と同じ流儀・"off" で voicevox
    ルーティング停止）。readiness ゲート = `/api/tts/status` の `voicevox.state`
    （running/starting/stopped）+ AdminTab の 5s ポーリング「準備中」表示。起動中の日本語は
    auto が Polly JP に逃がし、ready 復帰で次の文からずんだもんに戻る。トグル操作は監査
    （`tts.engine`）。Cloud Map の固定 DNS は `AF_VOICEVOX_URL` に差すだけ（ハンドラ不変）。
  - フロント: 設定に「音声エンジン」（自動/ずんだもん/Polly）と「話者（Polly）」
    （Takumi/Kazuha/Tomoko, `ttsVoicePolly`）を追加。synthesize リクエストに `pollyVoice`/`lang`
    を同送。テスト: `tts_test.go`（ルーティング表・admin トグル）/`tts_polly_test.go`（SSML/
    話者既定/未設定 503）/`tts_ecs_test.go`（state 写像・desired 0↔1）。
    **未検証**: AWS 実環境（実 Polly 音声・ECS トグル・IAM）と実機の音出し。

## 将来の追加プロバイダ: Voiceger（多言語ずんだもん）— 保留

「ずんだもんの声のまま英語も喋る」需要への候補。2026-07-10 に実現性を調査し、**設計記録のみ・
実装保留**と決定（VOICEVOX＋Polly 構成を継続）。

- **Voiceger:Zundamon**（SSS LLC, 2025-08-05）＝ GPT-SoVITS + RVC ベースの多言語ずんだもん TTS。
  日本語/英語/中国語/韓国語/広東語＋6〜8 感情。無料・商用可、クレジット「Voiceger:Zundamon」要。
  配布は Windows バイナリ＋GitHub（zunzun999/voiceger_v2, zundamon-speech-webui）。
- **統合上の壁**: (1) 公式は **Streamlit UI のみで HTTP API 無し** → 基盤の GPT-SoVITS
  `api_v2.py`（既定 `:9880` の `/tts`）にずんだもんモデル＋参照音声を載せて起動し、CP に
  `voiceger` プロバイダ（`AF_VOICEGER_URL` を指すアダプタ）を足す経路になる。(2) **CUDA/ROCm=GPU 前提**。
- **保留理由**: dev ホストの GPU は **AMD Vega 内蔵APU（Picasso/Raven2, gfx90c/gfx902）**で、CUDA 不可・
  **ROCm も APU 非対応** → GPU 加速不可、CPU 実行は遅く重い（OOM 多発ホストで非推奨）。よって
  **このホストでは検証不能**。実装すると「未検証コード」になるため見送り。
- **やるならの前提**: NVIDIA GPU 機 or クラウド GPU（Colab/RunPod/AWS g4dn 等）に GPT-SoVITS を立て、
  CP の `voiceger` プロバイダから `/tts` を叩く（「エンジン＝CP が指す URL」の抽象がそのまま効く）。
  その際 `auto` を「英語→ずんだもん(Voiceger) / 日本語→VOICEVOX / それ以外→Polly」に拡張できる。

## 未決 / 論点

- 逐次再生のチャンク粒度（句点のみ vs 読点も。短文結合の閾値）。実測で調整。
  → 最初の 1 文だけ読点/一定長で早出しする対応を 2026-07-10 に実施（`firstChunkCut`）。
  2 文目以降を読点でも切るか（体感レイテンシ vs 細切れ）は引き続き実測で調整。
- ~~Polly の出力フォーマット~~ → mp3 に決定（`AudioContext.decodeAudioData` が直接復号
  できるためフロント変更なし。2026-07-10）。
- 有効な間の VOICEVOX warm 維持と ECS のアイドル停止（[p3-9-idle-stop](p3-9-idle-stop.md)）の整合。
- 会話の途中参加（既存メッセージの手動再生）時の話者・速度の解決順。
