# 0013. エージェント回答の音声読み上げ — CP-native TTS・プロバイダ抽象・ずんだもん主役 / Polly 受け皿

[English](0013-tts-zundamon.md) | 日本語

- 状態: 確定（2026-07-09）・Phase 1〜2 実装済み（2026-07-10）
- 関連: [24-tts-zundamon.md](../log/24-tts-zundamon.md)（設計本体）/
  [0005-envelope-custodian.md](0005-envelope-custodian.ja.md)（秘密情報の扱い）/
  [p3-7-aws-adapter.md](../log/p3-7-aws-adapter.md)（ECS）

## 背景

チャット（assistant-chat）のエージェント回答を「ずんだもん」の声で読み上げたい。ずんだもんの声は
実質 **VOICEVOX エンジン**が必須（ブラウザの Web Speech API では出せない）。将来 **AWS Polly** など
他エンジンにも広げたい。既存 TTS コードはゼロ。回答は SSE でトークンが流れ、フロントの `ChatView`
（`onDelta`/`onDone`/`stop`）に集約されるため、読み上げのフック点は一点で足りる。

検討の要点は「エンジンをどこで動かすか」「Polly の認証情報をどこに置くか」の 2 つ。CP の外向き通信は
egress 制限外（OAuth コードが `http.DefaultClient` 直叩き）、バイナリ応答の前例あり（LFS の
octet-stream）。一方、秘密情報の金庫は現状すべて Agent コンテナ側で、CP 側に第三者鍵の金庫はない。

## 決定

1. **TTS は CP-native**（Agent 非経由）。VOICEVOX / Polly を CP から直接叩き、WAV を octet-stream で
   返す。egress 制限外なので allowlist 変更不要。ワークスペース停止中でも動く。チャットが
   「Agent が CLI 実行 → CP がプロキシ」だったのとは責務が異なる（外部 HTTP サービス呼び出しに過ぎない）。
2. **プロバイダ抽象＋使い分けは CP に集約**。`voicevox` / `polly` を map dispatch。フロントは
   `providerPref` とテキストを送るだけで、最終的にどちらで鳴らすかは CP が決める（engine の ready
   状態を知るのは CP のみ = 単一の真実源）。
3. **VOICEVOX エンジンは "CP が指す URL"**（`AF_VOICEVOX_URL`）。物理配置（同居 docker / 専用箱 /
   ECS）を差し替えても CP ハンドラは不変。**共有ワークスペースホストには載せない**（~1GB 常駐が
   fleet を OOM で巻き込むため）。自ホストはサイドカー、AWS は ECS。
4. **Polly 認証は IAM インスタンス/タスクロール**。CP 側に新たな秘密金庫は設けない。日本語ニューラル
   音声も持つため、Polly は「非日本語専用」ではなく**日本語のフォールバック兼・声の選択肢**。
5. **AWS では管理者トグルで ECS オンデマンド起動**（Service desired 0↔1、Cloud Map 固定 DNS、
   readiness ゲート）。停止中コスト 0。CP ロールに `ecs:UpdateService`/`DescribeServices` を追加。
6. **使い分けは `auto` 既定**：日本語 & engine ready → ずんだもん、engine 不在 → Polly JP、
   非日本語 → Polly。明示 `polly` なら日本語でも常に Polly。言語判定は既存 `outputLanguage` を再利用。

## 帰結

- フェーズ: **Phase 1（自ホスト）** = CP-native ルート（`/api/tts/synthesize`・`/api/tts/status`）＋
  VOICEVOX 常駐サイドカー＋フロント句点逐次再生＋設定トグル。**Phase 2（AWS）** = Polly（IAM ロール）＋
  管理者トグル→ECS＋`auto` フォールバック有効化。詳細は docs/24。
- フロントは合成 in-flight を 2〜3 に絞り、再生は文の連番順に固定（逐次だが順序保証）。Markdown/
  コードブロック/URL はプレーン化。`stop()` と読み上げ中断を連動。
- 設定は `lib/settings.ts` + `AgentsTab`「セッション」に既存 `outputLanguage` と同じ定型で追加。
  VOICEVOX の URL はユーザー設定でなく CP config。
- VOICEVOX / ずんだもんの**クレジット表記**を常時表示する（利用条件）。
- 捨てた案: ブラウザ→ローカル VOICEVOX 直（Polly の鍵をブラウザに置けず、多言語/常時稼働の受け皿に
  ならない）／コンテナ内エンジン同梱（OOM リスク）／CP 側秘密金庫の新設（IAM ロールで不要）。
