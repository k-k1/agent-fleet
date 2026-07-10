# 24. エージェント回答の音声読み上げ（TTS / ずんだもん・Polly）

- 状態: Phase 1 実装済み（2026-07-09, feat/tts-zundamon）・Phase 2（AWS/Polly）未着手
- 関連: [decisions/0013-tts-zundamon.md](decisions/0013-tts-zundamon.md)（決定記録）/
  [decisions/0005-envelope-custodian.md](decisions/0005-envelope-custodian.md)（秘密情報の封筒暗号）/
  [history/p3-7-aws-adapter.md](history/p3-7-aws-adapter.md)（ECS アダプタ）/
  [dev/03-control-plane.md](dev/03-control-plane.md) / [dev/02-console.md](dev/02-console.md)

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

## 設定（`console/src/lib/settings.ts` + `AgentsTab.tsx`）

`AgentsTab.tsx:76-97`「セッション」セクションに、既存 `outputLanguage`（`:85-91`）と同じ
`OnOff`/`Choice`（`./controls.tsx`）定型で追加。localStorage（`af-display-settings`）＋
`PUT /api/env/ui-prefs` サーバミラー（`workspace/agent/ui_prefs.go`）に自動追随。

- `ttsEnabled`（bool, 既定 false）
- `ttsProvider`（`auto` | `voicevox` | `polly`, 既定 `auto`）
- `ttsVoiceVoicevox`（既定 `3` = ずんだもんノーマル）
- `ttsVoicePolly`（既定 `Takumi`）
- `ttsSpeed`（0.5〜2.0, 既定 1.0）

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
  Polly と同じ CP ロールに相乗り。鍵保存はゼロ。[p3-7-aws-adapter](history/p3-7-aws-adapter.md) の ECS 構成に連なる。
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
- **Phase 1.5（再生制御・FileView 連携）** ✅ 実装済み: 再生をアプリ全体で 1 本に集約する
  グローバルストア `core/store/tts.ts`（`useTtsStore`: speaking/source/stop）。TopBar に
  「読み上げ中・〇〇」インジケータ兼**停止ボタン**を追加（再生中のみ表示、`app/TopBar.tsx`
  + `topbar.css`）。FileView の選択範囲を読み上げるピルを既存「送る」の隣に追加（`speakText()`、
  `ttsEnabled` 有効時のみ、`FileView.tsx` + `viewer.css`）。過去の回答フッターに読み上げ
  ボタンを追加（共用 `features/chat/TtsReadButton.tsx`、ChatView と MirrorView の各ターン
  フッター、assistant/非 user ターンかつ `ttsEnabled` 時のみ）。実機での音出し確認は未。
- **Phase 1.6（バックグラウンドセッションの音声通知・Tier1）** ✅ 実装済み: 稼働中の複数
  セッションが回答/質問を返したら、セッション名を添えて短く音声でお知らせ（設計検討の結論＝
  直列音声なので「全文読み上げ」でなく「アナウンス方式」を採用）。既存の `useSessionNotifications`
  （app 常駐・全セッションの状態を 4s ポーリングし working→idle / →question を検知）に接続。
  多セッション衝突は tts.ts の**アナウンス直列キュー**（`announce()`, 何か再生中なら待つ、
  溜まりすぎは古いものを捨てる）で解消。設定「セッションの音声通知」(`ttsSessionNotify`)。
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
- **Phase 2（AWS）**: Polly プロバイダ（IAM ロール）、管理者トグル → ECS desired 0↔1、
  Cloud Map 固定 DNS、readiness ゲート、`auto` の Polly フォールバック有効化。

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
- Polly の出力フォーマット（pcm を AudioContext 直、または mp3 を `<audio>`）。
- 有効な間の VOICEVOX warm 維持と ECS のアイドル停止（[p3-9-idle-stop](history/p3-9-idle-stop.md)）の整合。
- 会話の途中参加（既存メッセージの手動再生）時の話者・速度の解決順。
