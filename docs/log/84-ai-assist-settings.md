# 84. 「AI 補助生成」を設定から切り出す（アシスタントとの分離）

- 状態: **実装済み**（2026-09-03）。Console の設定に「AI補助」タブを新設し、アシスタント／
  エージェント／キー操作の 3 タブに散っていた AI 補助生成の設定をそこへ集約した。移行は
  `migrateAiAssistPrefs()` 1 本（localStorage とサーバ prefs の両方が同じ関数を通る）。
- 関連: [19-assistant-chat.md](19-assistant-chat.md)（アシスタント・チャットと one-shot が
  同じ基盤を共有した経緯）/ [46-usage-accounting.md](46-usage-accounting.md)（one-shot の
  モデル選択と台帳）/ [24-tts-zundamon.md](24-tts-zundamon.md)（`outputLanguage` を読み上げが
  借りていた箇所）/ [44-markdown-code-editor.md](44-markdown-code-editor.md)（ファイル編集の提案）

---

## 84.1 いま何が起きていたか

利用者からの指摘はこの 2 つだった。

> 「タイトルのAI提案」— セッションで有効で、アシスタントでは自動提案はない
> 「エージェントの優先順位」— アシスタントだけではなくセッションのタイトル AI 提案でも使われる

調べると、**設定の名前と置き場所が「実装の共有先」を指していて、「利用者が見る面」を
指していなかった**。これが唯一の原因で、そこから派生した食い違いが 5 つ出ていた。

「設定 > アシスタント」に同居していたのは、実は別の 2 つのものだった。

| | ① アシスタント・チャット | ② AI 補助生成（one-shot） |
|---|---|---|
| 実体 | 会話を継続する CLI セッション（`ChatProviders`） | 1 回きりのヘッドレス呼び出し（`OneShotHeadless`） |
| 面 | チャットペイン | **セッション / ミラー / File ペイン / 各リネームダイアログ** |
| 用途 | ビルトイン・カスタムアシスタント | タイトル・ブランチ名・返信候補・編集提案・計画更新 |

② は「アシスタントの機能」ではない。`OneShotHeadless` を呼ぶのは 7 箇所で、そのうち
**4 箇所はアシスタントとまったく関係のない面**に出る。

| 呼び出し元 | 機能 | 面 |
|---|---|---|
| `session_title.go` | セッションのタイトル提案 | セッション |
| `session_title.go` | ブランチ名の提案 | ブランチ名変更ダイアログ |
| `session_suggest_reply.go` | 返信サジェスト（✨） | ミラー |
| `fs_suggest_edit.go` | ファイル編集の提案 | File ペイン |
| `internal/chatx/chat_title.go` | チャットのタイトル提案 | チャット |
| `internal/chatx/chat_suggest_reply.go` | 返信サジェスト（✨） | チャット |
| `internal/chatx/chat_plan.go` | 計画の更新 | チャット |

そのうえ ② の ON/OFF は **3 タブに散っていた**（しかも 2 機能はトグル自体が無い）。

| 設定 | 旧・置き場所 | 実際に効く範囲 |
|---|---|---|
| `autoTitleSuggest` | エージェント > セッション | セッションのタイトル**＋ブランチ名** |
| `assistantTitleSuggest` | アシスタント | チャットのタイトルのみ |
| `replySuggestEnabled` | キー操作 | ミラー＋チャット |
| `assistantAgentOrder` | アシスタント | チャット＋補助生成 7 種 |
| `assistantUtilityModels` | アシスタント | 補助生成 7 種 |
| （ブランチ名） | **無し** | `autoTitleSuggest` に相乗り |
| （ファイル編集の提案） | **無し** | 常時 ON |

### 派生していた食い違い

1. **`assistantUtilityModels` が名前に反して文章生成の既定まで置き換えていた。**
   `OneShotHeadless` の claude 分岐は `if configured { claudeModel = selected }` で、
   呼び出し側が渡した既定（編集提案・計画更新は `sonnet`）を無条件に上書きする。
   「タイトル・サジェストのモデル」に haiku を入れると、**ファイル編集の提案まで haiku に
   落ちる**。ラベルにも注記にもその旨は無い。
2. **`outputLanguage`（回答言語）が読み上げに流用されていた。** `ttsOptsFromSettings()` が
   `lang: s.outputLanguage` を入れ、CP の `chooseTTSProvider` は `lang=="en"` で
   VOICEVOX → Polly、`pollyVoiceFor` は Takumi → Joanna に切り替える。使うのは
   ChatView だけでなく **ミラーの読み上げ・朗読ビュー・File ペイン**。つまり
   *チャットの回答言語を English にしただけで、セッションの読み上げの声が変わっていた。*
3. **`autoTitleSuggest` がブランチ名の提案も止めていた。** どのラベルにも注記にも無い。
4. **OFF にしてもボタンは消えなかった。** `ChatTitleModal` / `SessionTitleModal` /
   `BranchRenameModal` のどれも設定を見ておらず、押すと 400（`feature_disabled`）で
   トーストが出るだけだった。
5. **注記が実装より狭かった。** 「エージェント優先順位」は反映タイミングを
   「新しい会話から」と書いていたが、one-shot 側は毎回 live 読みで即反映される。

## 84.2 決めたこと

**設定は「実装の共有先」ではなく「利用者が見る面」で分類する。** その上で 3 つ。

1. **② を独立タブへ**（設定 > 個人設定 > **AI補助**）。散っていた ON/OFF もここへ集約し、
   トグルの無かった 2 機能（ブランチ名・ファイル編集の提案）にもトグルを与える。
   アシスタントタブは「アシスタントとの会話そのもの」を変える設定だけに戻す。
2. **1 キー 1 責務**。`outputLanguage` の読み上げ流用をやめ、`ttsLang` を新設。
   タイトル提案は セッション / チャット / ブランチ名 で 3 キーに割る。
   補助生成のモデルは用途で 2 系統に割る（下記）。
3. **表示と実効を一致させる**。OFF の機能はボタンごと出さない。

### 優先順位とモデルを 2 系統に割る理由

チャットと補助生成は**欲しいものが逆**である。チャットは強い CLI／モデルを選びたい。
補助生成は常時走るので、安くて速い・動くものを選びたい。1 本にすると必ずどちらかが
妥協になる。よって:

- CLI 優先順位: `assistantAgentOrder`（チャット）と `aiAssistOrder`（補助生成）。
- モデル: `assistantModels`（チャット）／`aiShortModels`（短文）／`aiProseModels`（文章）。

モデルを **short / prose** に割ったのは 84.1 の 1 が理由。要求される品質が違う。

| 用途 | 対象 | 推奨の解決先 |
|---|---|---|
| `OneShotShort` | セッション/チャットのタイトル、ブランチ名、返信候補 | 軽量（claude なら haiku） |
| `OneShotProse` | ファイル編集の提案、計画の更新 | 中位（claude なら sonnet。アシスタント本体と同じ） |

## 84.3 移行（アップグレードで挙動を変えない）

`console/src/lib/settings.ts` の `migrateAiAssistPrefs()` 1 本。**足りないキーを埋めるだけ**で
既存の値には触らない。`load()`（localStorage）と `hydrateUIPrefs()`（サーバ prefs）が同じ
関数を通る — 旧 `assistantTitleSuggest` の移行規則が 2 箇所に写経されていたのが、今回の
食い違いを増やした一因なので、そこは畳んだ。

| 新キー | 継ぐ元 | 理由 |
|---|---|---|
| `assistantTitleSuggest` / `branchSuggestEnabled` | `autoTitleSuggest` | 旧キーが 3 つを兼ねていた。明示的な OFF は 3 つとも継ぐ |
| `aiAssistOrder` | `assistantAgentOrder` | 分離前は 1 本。分けたことに気づかないまま挙動が変わらない |
| `aiShortModels` | `assistantUtilityModels` | 名前どおりの用途 |
| `aiProseModels` | **継がない** | ★ 下記 |
| `ttsLang` | `outputLanguage` | 借りていた頃の値を継ぐ＝読み上げの挙動は据え置き |
| `editSuggestEnabled` | （既定 true） | 設定が無く常時 ON だった＝欠落は「従来の挙動」 |

★ `aiProseModels` だけ意図的に継がない。旧キーは「タイトル・サジェストのモデル」という
名前のまま文章生成の既定まで置き換えていた。そこに haiku を入れた人は**名前どおり短文用途に
入れた**のであって、ファイル編集の提案を落としたかったわけではない。文章側は用途に合った
推奨へ戻す — この整理の目的そのものなので、ここだけは「挙動を変えない」より優先する。
既定は両系統とも `recommended` なので、実際に影響を受けるのは旧キーに**具体モデルを明示して
いた人だけ**である。

Agent 側も同じ鎖でフォールバックする（`aiShortModelPref` → `assistantUtilityModels`、
`aiAssistOrderPref` → `assistantAgentOrder`、`BranchSuggest()` → `AutoTitleSuggest()`）。
Console が新キーを書く前に Agent だけ新しくなっても挙動が変わらないようにするため。

### `ttsLang` の "auto" だけ挙動が変わる

新設の "auto" は **UI 表示言語に従う**。借用時代の `outputLanguage="auto"` は「入力に合わせる」
という別の軸の値で、TTS 側からは常に非 en＝日本語扱いになっていた。つまり英語 UI の利用者も
VOICEVOX に流れていた。`ttsOptsFromSettings()` は既に `enkana` / 助詞ポーズを
`getLocale() === "ja"` で切っており、読み上げの言語軸は元から UI ロケールである。そこへ揃えた。

## 84.4 実装の要点

- **`OneShotTier`**（`internal/chatx/chat_providers.go`）。`OneShotHeadless` の第 2 引数。
  用途は「その呼び出しが何を必要とするか」で、設定の置き場所ではない。
- **`PreferredHeadlessAgent`（チャット）と `PreferredAssistAgent`（補助生成）** に分割。
  共通部は `preferredFrom(order)` に畳んだ。
- **`agentOrderPref(keys...)`**（`ui_prefs.go`）は先勝ちのフォールバック鎖。1 本のリストでは
  なく鎖にしてあるのは、旧 prefs から補助生成側を引き継ぐため。
- **ゲートの追加**: `handleSessionSuggestBranch` は `uiprefs.BranchSuggest()`、
  `handleFSSuggestEdit` は `uiprefs.EditSuggest()`。
- **UI 側のゲート**: `ChatTitleModal` / `SessionTitleModal` / `BranchRenameModal` /
  `FileEditControls` は設定を見て**ボタンを描画しない**。サーバの 400 は防御として残す。

## 84.5 これで直ったこと

| 84.1 の食い違い | 直り方 |
|---|---|
| ユーティリティモデルが文章生成の既定を上書き | short / prose の 2 系統に分割 |
| 回答言語が読み上げのエンジン・声を変える | `ttsLang` を新設し流用を停止 |
| タイトル提案の設定がブランチ名も止める | `branchSuggestEnabled` を新設 |
| OFF でもボタンが出て 400 になる | 4 箇所とも設定を見て非表示に |
| 注記が実装より狭い | 全注記を書き直し（ja / en 両方）＋ guide/member/12・guide/ref/settings |

## 84.6 残っていること

- **カスタムアシスタントの MCP / 知識ディレクトリ**は今回触っていない。これは会話の設定なので
  アシスタント側で正しい。
- **`AF_TITLE_MODEL_*` 環境変数**は short 側の上書きとして残している。prose 用の環境変数は
  作っていない（デプロイ側から編集提案のモデルを固定したい要求がまだ無い）。
- 実機での目視は開発 Workspace の headless Chromium では**行っていない**。設定タブの
  描画差分（`scripts/shots`）は未取得。
