# 103-impl-review. 機能別 AI 補助の実装レビュー（`temp/so5kjd7`）

- 対象: `git diff a58a9edc origin/temp/so5kjd7`（3 コミット・47 ファイル・+1377/-191）
- 設計: [103-ai-assist-per-feature.md](103-ai-assist-per-feature.md)（`a58a9edc` の改訂版）/
  設計レビュー: [103-review.md](103-review.md)
- 日付: 2026-09-21。レビューのみ。実装コードと 103 本体は書き換えていない。
- 読み方: 各件は **何が / どのファイル:行 / なぜ問題か / どう直すか**。
  **「実測」はこのレビューで実際に走らせて得た結果、「コード読解」は読んだだけのもの**。
  混ぜない。

**このレビューで走らせたもの**（すべて `git archive` で `/tmp` に展開した使い捨てのコピー。
自分のワークツリーにも他のワークツリーにも実装コードは入れていない）:

- `go build ./...` / `go vet ./...`（`workspace/agent`）— どちらも緑。
- 新規テスト群（`-count=1`）— 緑。
- **変異テスト 1 本**: `oneShotKind` のピン分岐を潰して、どのテストが捕まえるかを数えた。
- **使い捨ての probe テスト 3 本**: ②が鍵に入るか／書き鍵と読み鍵が食い違うとどうなるか／
  `GET /translations` が新しい鍵を知っているか。
- **Console の dom probe 1 本**: タブを 1 回 mount したときの `/ai-assist/resolution` の回数。

---

## 0. 総評

設計レビューの 14 件は**ほぼ全部が実装に反映されている**。特に重い 3 つ——
feature の明示引数化（決定 9）、`HandleChatPlanRefresh` のサーバ側ゲート、
`chatx.Deps` の seam 3 本目——はどれも設計どおりで、テストも本物である
（ピン解決の陽性対照が実際に効くことを変異テストで確認した。§1-7 参照）。

見つかった新しい欠陥は **13 件**。うち重大 5 件で、**4 件が翻訳キャッシュまわりに集中している**。
決定 8（「鍵に解決済みの kind とモデルを足す」）が、設計の想定より難しかったのが理由で、
これは実装の粗さというより**設計の穴が実装で表に出た**ものである（§1-1 と §1-4）。

---

## 1. 指摘（重大度順）

### 【重大 1】`GET /translations` が新しい鍵を知らず、POST と食い違う翻訳を返す

- **何が**: 翻訳の保存が `(hash, lang)` から `(hash, lang, kind, model)` へ増えたのに、
  ミラーの先読み経路 `GET /sessions/{name}/translations` は**言語だけで畳んだ
  `hash → text` の map** を返したままである。
- **どこ**: `workspace/agent/session_translate.go:555-569`（`handleSessionTranslations`——
  この差分で**一切触られていない**）。読み手は
  `console/src/features/mirror/useTranslate.ts:132`。
- **実測**（probe）: 同じ本文に対し、
  1. モデル A で翻訳 → 保存。
  2. モデルを B に変えて押す → `POST /translate` は正しくミス扱いで再生成（`cached=false`）。
  3. **モデルを A に戻して押す** → `POST` は A の訳を返す（`cached=true`,
     `text="OLD-MODEL-A-TRANSLATION"`）。
  4. 同じ瞬間の `GET /translations` は **B の訳**（`"NEW-MODEL-B-TRANSLATION"`）を返す。

  ＝ **同じ本文の翻訳が、ペインを開いたときと押したときで違う。**
- **なぜ問題か**: 決定 8 は「何で訳したかは訳文の同一性の一部」と言って `POST` 側に
  鑑別を入れた。その鑑別を**先読み経路が丸ごと迂回する**。しかも先読みのほうが
  利用者の目に入る回数が多い（ペインを開く・スマホで開く・`mirrorAutoTranslate`）。
  §103.6 の配線表は
  「鍵の実装は Go / TS の 2 本なので `console/src/features/mirror/translate.ts` と対で動かす」
  と名指ししているが、**TS 側も、この GET ハンドラも触られていない**。
  `entries[e.Hash] = e.Text` は後勝ちなので、どちらが出るかはファイル内の並び順という、
  利用者にも次の読み手にも説明できない要因で決まる。
- **どう直すか**: `handleSessionTranslations` に `POST` と**同じ鑑別**を通す。
  `handleSessionTranslate` が既に 1 回だけ呼んでいる `translateCachePin()` をここでも呼び、
  `lookupTranslation` と同じ条件（旧鍵は常にヒット・`Kind` 一致・設定由来のモデルがあれば一致）
  で 1 件を選ぶ。`lookupTranslation` を `(hash) → *sessionTranslation` の形で再利用すれば
  条件は 1 か所に保てる。`TranslationsReply` の形は変えなくてよい。
  §103.10 の検証計画に「**先読みと押下が同じ訳を返す**」を 1 行足すこと。

### 【重大 2】設定タブが `/ai-assist/resolution` を機能ごとに叩いている（1 mount で 8 回）

- **何が**: `useAiAssistResolution()` を**カードの中**で呼んでいるので、8 枚のカードが
  それぞれ独立に同じ 8 件を取りに行く。
- **どこ**: `console/src/features/settings/personal/AiAssistTab.tsx:79`（`AiFeatureCard` の中）/
  `console/src/lib/aiAssistResolution.ts:24-41`（フックはインスタンスごとに
  `useState` + `setInterval`。共有も dedupe も無い）。
- **実測**（dom probe）: **タブを 1 回 mount するだけで
  `api("api/ai-assist/resolution")` が 8 回**。`POLL_MS = 10_000` なので、
  タブを開いている間ずっと **48 リクエスト／分**。
- **なぜ問題か**: §103.8-3 が決めたのは
  「**1 リクエストで 8 機能ぶんを返す（機能ごとに叩かせない）**」である。
  Agent 側は正しく 1 レスポンスで 8 件返す作りになっているのに、フロント側でそれが
  取り消されている。1 リクエストあたり Agent は `uiprefs.Read()` を
  8（`enabled`）＋ 8（`aiFeatureAgentPref`）＋最大 8（`aiAssistOrderPref`）回呼び、
  そのたびに ui-prefs.json を読んで JSON を parse する（`uiprefs.Read()` にキャッシュは無い）。
  48 × 20 ＝ **分あたり 1,000 回近いファイル読み込み**が、設定タブを開いているだけで走る。
- **どう直すか**: フックを `AiAssistTab` で 1 回だけ呼び、`row` を prop で
  `AiFeatureCard` に渡す（`f` と並べて 1 引数増えるだけ）。
  回帰を止めたいなら、dom テストに「1 mount あたりの
  `ai-assist/resolution` 呼び出しは 1 回」を 1 本置く。

### 【重大 3】「いま使うのは」が、実際に使う場面ではほぼ常に「未取得」のまま

- **何が**: 解決エンドポイントは可用性キャッシュを**覗くだけで、絶対に温めない**。
  そして**他に温める主体がいない**。
- **どこ**: `workspace/agent/internal/chatx/chat_providers.go:1489-1506`
  （`oneShotKindCached`）/ `workspace/agent/ai_assist.go:66-70`。
  温める側は `git grep headlessAgentAvailable` の結果 **3 つだけ**——
  `preferredFrom`（:128）・`ChatProviderFor`（:152）・`oneShotKind`（:1443）。
  どれも**実際にチャットのターンか一発生成が走ったとき**にしか呼ばれない。
- **コード読解 ＋ ブランチ自身のテスト**: `workspace/agent/ai_assist_test.go` の
  `TestAIAssistResolutionColdCacheIsUnknown` が、冷えたキャッシュで全 8 行が
  `source:"unknown"` / `kind:""` になることを**期待動作として固定している**。
- **なぜ問題か**: 利用者が AI補助タブを開くのは「これから設定しに来た」ときで、
  その直前 60 秒に一発生成が走っている保証はどこにも無い。10 秒ごとにポーリングしても、
  温める主体がいないので**ずっと unknown のまま**である。
  §103.7 が決定 5 で足した行が、いちばん必要な場面で死んでいる。
  103-review 重大 4 への対処として「同期で起動しない」は正しいが、
  「**誰も温めない**」まで行くと、その行が無いのと同じになる。
- **どう直すか**: **応答を待たずに温める**。`handleAIAssistResolution` が unknown を返した
  kind について、レスポンスを書いたあと `go func(){ headlessAgentAvailable(k) }()` を撃つ
  （同時発火は `headlessAvailMu` が直列化する）。10 秒後の次のポーリングが本当の答えを返す。
  「外部プロセスを絶対に起動しない」という契約は**応答経路**についてのもので、
  プロセス全体の禁止ではない——`ai_assist.go` のヘッダをそう書き直す。
  温めるのを避けたいなら、Console が既に持っている `/connections` の接続状態を
  `kind` の材料にする手もある（ただしそれは §103.4 決定 5 が
  「`conns` と `headlessAgentAvailable` は別物」と退けた道なので、前者を推す）。

### 【重大 4】翻訳キャッシュの**書き鍵と読み鍵が別の式**で、食い違うと永久にミスする

- **何が**: 書くときの `Model` は「実際に走った値」（`call.ModelReq`）、
  読むときの `pinnedModel` は「これから走るはずの値」（`resolveOneShot` の予測）。
  2 つは別のコードが計算しており、一致する保証が無い。
- **どこ**: 書き `workspace/agent/session_translate.go:529-533`（`ranModel`）/
  読み `session_translate.go:364-370`（`translateCachePin`）/
  値の出どころ `internal/chatx/chat_providers.go:1664`
  （`defer func() { kind, model = call.Kind, call.ModelReq }()`）。
- **実測**（probe）: 走行が予測と違うモデル名を報告する状況を作ると、
  **3 回押して 3 回とも `cached=false`・モデル 3 回走行**。
  保存行は 1 行のまま（`putTranslation` が同じ `Hash/Lang/Kind/Model` を置換するので、
  ストアが膨れることは無い）——つまり**貯めても二度と読めない**状態が恒久化する。
  §103.8-2 と決定 8 が避けようとしたキャッシュ破壊そのものである。
- **食い違う実経路**（コード読解、未実行）: **agy**。
  `chat_providers.go:1794` は `if m := agyChatModel(m, filterVisibleModels(...)); m != ""` で
  `call.ModelReq` を埋めるが、`agyChatModel`（:1067-1077）は
  **ライブのカタログに載っていない ID を `""` に落とす**。
  設定したモデル ID がカタログから消える／改名されると、書き鍵は `""`、読み鍵は旧 ID のまま。
  カタログが空のとき（CLI 未ログイン等）は素通しなので、**発生が間欠的**＝いちばん気づきにくい。
  - codex のリトライ（`CodexOneShotWithRetry`:1529 が `modelReq` を `""` に落とす）は同じ形だが、
    リトライは `autoPicked` のときだけで、モデルが configured なら `codexOneShotArgsFor` は
    `autoPicked=false` を返す。**鍵に model が入るケースでは発火しない**（ここは問題にならない）。
  - `claude` / `opencode` / `cursor` は `selected` をそのまま `ModelReq` に入れるので一致する。
- **なぜ問題か**: 鍵の役割は「**どの設定が生んだ訳か**」であって
  「ベンダが何を課金したか」ではない。後者は台帳の仕事で、現に台帳が持っている。
  そして読み側は**実行前にしか鍵を作れない**以上、「実際に走った値」は鍵には使えない。
  ここは実装のミスではなく、**決定 8 の字面（「実際に走った値」）が構造的に無理**なのである。
- **どう直すか**: 読み鍵と書き鍵を**同じ式**にする。`handleSessionTranslate` は既に
  1 回だけ `translateCachePin()` を呼んでいる（:499）ので、その `pinnedModel` を
  そのまま `putTranslation` に渡す（`ranModel` は捨てるか、`ranKind` だけ残す）。
  103 側は決定 8 の「実際に走った値」を
  「**解決時に鍵として決めた値（＝押下の時点で読み書き同じ式）**」へ訂正する。

### 【重大 5】§103.9 の移行 2（チャット ✨ の継承）が Agent 側に無い

- **何が**: `ChatReplySuggest()` は `assistantReplySuggestEnabled` を読むだけで、
  **`replySuggestEnabled` へのフォールバックが無い**。継承は Console の移行コードにしかない。
- **どこ**: `workspace/agent/internal/uiprefs/prefs.go:168-171`。
  比較対象は**同じファイルの 2 つの前例**——`BranchSuggest`（:129-134）と
  `AssistantTitleSuggest`（:322-327）で、どちらも**サーバ側で**旧キーに落ちる。
  継承は `console/src/lib/settings.ts:983-986`（`migrateAiAssistPrefs`）だけ。
- **なぜ問題か**: `migrateAiAssistPrefs` はメモリ上の移行で、サーバの ui-prefs.json に
  書き戻るのは利用者が**次に何か設定を保存したとき**（全オブジェクト PUT）である。
  それまでサーバのキーは欠落＝`ChatReplySuggest()` は true。
  つまり **画面はボタンを隠すのに `POST /chat/conversations/{id}/suggest-replies` は通る**——
  これは 103-review 重大 2（§103.3-3）で直したはずの失敗の、ちょうど裏返しである。
  AGENT_TOKEN を持つもの（各セッションの MCP サーバを含む）から見れば、
  返信候補を切った利用者の設定が効かない窓が残る。しかも**ミラー側は今回サーバで直っている**
  ので、2 つの ✨ で防御の強さが違う。
  §103.9 の表の「継ぐ元: `replySuggestEnabled`（明示 OFF は両方へ）」は、
  前例 2 つと突き合わせれば「サーバでも継ぐ」と読むのが自然である。
- **どう直すか**: `BranchSuggest` と同じ形にする。

  ```go
  func ChatReplySuggest() bool {
      if v, ok := Read()["assistantReplySuggestEnabled"].(bool); ok {
          return v
      }
      return ReplySuggestEnabled() // 旧キーの明示 OFF を継ぐ（BranchSuggest と同じ理由）
  }
  ```
  （`uiprefs` から `sessionx` は呼べないので、`replySuggestEnabled` を直接読む形になる。）
  `chat_suggest_reply_test.go` に「旧キーだけ false・新キー欠落」で 400 になるケースを 1 本。

---

### 【中 6】ピンしたカードのモデル欄が、実効値と違う既定を表示する／§1 に戻す道が無い

- **どこ**: `console/src/features/settings/personal/AiAssistTab.tsx:108`
  （`value={s.aiFeatureModels?.[f.id]?.[pin] || ASSISTANT_RECOMMENDED_MODEL}`）
- **なぜ問題か**（コード読解）: 未設定のとき Agent は `aiFeatureModelPref` が ok=false →
  `oneShotModelPref(kind, tier)`＝**§1 のティア設定**を使う
  （`chat_providers.go:1416-1422`）。画面は「推奨（現在: …）」と出す。
  §1 で `aiProseModels.codex = "gpt-5.6-luna"` にしている利用者が翻訳を codex にピンすると、
  **画面は「推奨」・実行は gpt-5.6-luna**。84 の 3 番目の原則（表示と実効を一致させる）に反する。
  さらに、いったん触ると「§1 に従う」へ**戻せない**。`AiModelRow` の選択肢は
  `[推奨, ...カタログ]` で、`""`（`ui.default`）は `assistantModelPref` が `("", true)` を返す
  ＝ configured で「CLI 既定」を意味する**別の状態**である。未設定＝継承を表す選択肢が無い。
- **どう直すか**: `AiModelRow` に「**既定（上の設定に従う）**」を足し、その値を
  **キーの削除**にマップする（`aiFeatureModels[feature]` から `[pin]` を消す）。
  未設定のときはその項目を選択状態にする。

### 【中 7】翻訳側の「ピン」テストは、ピン経路を通っていない

- **どこ**: `workspace/agent/session_translate_test.go:487`
  （`TestTranslateModelPinChangeMissesCache`）
- **実測**（変異テスト）: `oneShotKind`（`chat_providers.go:1444`）のピン分岐を
  `false &&` で殺すと——
  - `internal/chatx` の `TestOneShotKindPinBeatsPriorityOrder` は**赤**になる
    （＝ `chat_resolve_test.go` は本物の陽性対照である）。
  - 同じ変異で `TestTranslateModelPinChangeMissesCache` は**緑のまま**。
- **なぜ問題か**: テストバイナリでは何も認証されていないので `headlessAgentAvailable("claude")`
  は false。ピンは採用されず、`preferredFrom` が全滅時に返す `order[0]`（＝claude）に
  たまたま落ちて、結果の kind が一致してしまう。
  テスト名とコメント（"Pinning a feature to a different concrete model"）が、
  **実際に通っている経路を偽って説明している**。いまのままだと、
  翻訳から見たピン経路の回帰はどのテストにも掛からない。
- **どう直すか**: `chat_resolve_test.go` の `forceHeadlessAvailable` と同じ手を main 側からも
  使えるようにする（`chatx` にテスト専用のエクスポートを足すのがいちばん安い）。
  そのうえでテスト名を実際の経路に合わせる。

### 【中 8】②（§1 のティアモデル）が鍵に入ることを、コメント・命名・テストのどれも言っていない

→ 判断は §3 (b)。ここでは事実だけ。

- **実測**（probe）: `aiFeatureModels` を**一切置かず**、`aiProseModels.claude` だけを
  `tier-a` → `tier-b` に変えると、`cached=false` になり再生成が走る。
  ＝ **②は鍵に入っている。**
- 一方、コードのコメントと命名は①だけを語っている——
  `session_translate.go:304-307`（`lookupTranslation` の "ONLY when the feature is explicitly
  pinned to one concrete model"）、`translateCachePin` という関数名、
  `pinnedModel` / `pinnedOK` という変数名、`TestTranslateModelPinChangeMissesCache` という
  テスト名。**次の読み手は、コードを「コメントどおりに」①だけへ狭めてしまう。**
- **どう直すか**: 名前を `translateCacheModel` / `settingModel` / `settingOK` に、
  コメントを「**設定由来のモデル（①のピン、または②のティア設定）があるときだけ鍵に入る。
  ③（`AF_*_MODEL`／推奨／CLI 既定）は追跡していないので入れない**」に直す。
  **②の case のテストが 1 本も無い**（このレビューは probe を書いて確かめた）ので、
  上の probe をそのまま常設テストにする。

### 【中 9】決定 5 の Y（モデル名）が、結局どこにも描かれていない

- **どこ**: `console/src/lib/i18n/locales/ja/aiassist.ts` の
  `"aiassist.currently_using": "いま使うのは: {agent}"` — モデルの欄が無い。
  `console/src/lib/aiAssistResolution.ts:5-8` のコメントは
  「モデル名が要る呼び手は自分のカタログから描く」と書くが、**そうする呼び手がいない**。
- **なぜ問題か**: 改訂版 103 の決定 5 は「X は Agent が答える。**Y は Console が持っている
  カタログで描く**」で、§103.7 のモックアップも「claude / claude-haiku-4-5」である。
  実装は X だけ。半分が落ちている。
  しかも §103.6 の Agent 行は
  「`GET /ai-assist/resolution` — 8 機能ぶんの `{feature, enabled, kind, model, source}`」と
  **`model` をレスポンスに載せると書いており、決定 5 と矛盾している**（103 の側の問題）。
- **どう直すか**: Y を描くなら `AiFeatureCard` で、
  ピン時は `aiFeatureModels[feature][pin]`、未ピン時は `aiProseModels/aiShortModels[row.kind]` を
  `useModelOptions(row.kind)` のラベルに当てる（追加リクエスト 0）。
  やらないなら **§103.11 に落とす**。どちらにせよ §103.6 の `model` 欄を消して
  決定 5 と揃えること。

### 【中 10】(a) の判断 → §3 参照（保留は妥当だが、理由が違い、安い部分は今できる）

---

### 【軽 11】`aiAssistFeature.tier` が未使用

- `workspace/agent/ai_assist.go:36`。`handleAIAssistResolution` はこのフィールドを 1 度も読まない
  （ブランチ全体で `.tier` の参照は 0 件）。Go はフィールドの未使用を弾かないので静かに残る。
- Console 側の `AI_ASSIST_FEATURES` にも `tier` があり、そちらは `AiFeatureCard` が使っている。
  **8 機能のカタログが Go と TS に 2 つある**状態で、片方のフィールドだけが死んでいる。
- 直し方: 消す。中 9 で Y を返すことにするなら、代わりにレスポンスへ `tier` を載せて
  カタログの重複そのものを減らす手もある。

### 【軽 12】`translateCachePin` の doc が `oneShotKind` の doc と矛盾し、全ヒットの押下に CLI 起動を持ち込んだ

- `session_translate.go:358-363`（`translateCachePin` の doc）と `:495-498`（呼び出し側のコメント）は「Cheap (no CLI started) / resolveOneShot never shells out」
  と書くが、`chat_providers.go:1436-1442` の `oneShotKind` は
  「**This CAN shell out**」と明記している。同じ PR の中で正反対である。
- 実効（コード読解）: `handleSessionTranslate` は `translateCachePin()` を
  **キャッシュ参照より前**に呼ぶ（:499）。以前は全ヒットの押下はディスク I/O だけだったが、
  可用性キャッシュが冷えていると最大 5 本の `auth status` を待つ。
  `mirrorAutoTranslate`（trigger=auto）でも同じ経路を通る。
- 直し方: doc をどちらかに寄せたうえで、**最初のミスが出るまで `translateCachePin` を呼ばない**
  （`sync.OnceValues` で遅延させれば 1 押下 1 回という性質も保てる）。

### 【軽 13】テストの Deps スタブが本番関数の手写しで、hidden models のルールだけ落ちている

- `workspace/agent/internal/chatx/deps_test.go:61-78` の `AiFeatureModelPref` は
  `ui_prefs.go:186-203` の写しだが、`sessionx.ModelHidden` のフィルタが無い。
- 結果: §103.10 が挙げた「**hidden model が推奨へ落ちる**」は、`chatx` の解決チェーンを
  通したテストでは 1 本も確かめられていない（`ui_prefs_test.go` の単体はある）。
- 直し方: スタブに同じ 1 行を入れるか、`ui_prefs_test.go` 側でチェーンまで通す。

---

## 2. 設計との照合（フェーズ 2）

### 決定 1〜9

| # | 決定 | 判定 | 根拠 |
|---|---|---|---|
| 1 | 機能ごとにエージェント 1 つ＋そのモデル | ✅ | `aiFeatureAgents` / `aiFeatureModels`、カード 1 枚に 1 組 |
| 2 | 未設定は既定に従う | ✅ 実測 | `TestOneShotKindPinBeatsPriorityOrder` の第 2 アサーション |
| 3 | 粒度＝台帳の feature | ✅ | `ai_assist.go:47-56` / `aiAssistFeatures.ts` とも台帳の文字列そのまま。ラベルも `usage.val.feature.*` を共有（§103.3-4 の解） |
| 4 | kind スコープ＋落ちた先の kind のモデル／**自動では具体モデルを選ばせない** | ✅ 実測 | `TestOneShotKindPinFallsBackWhenUnavailable`＋`AiAssistTab.dom.test.tsx` |
| 5 | X は Agent、Y は Console のカタログ | ⚠️ 半分 | X は実装済み。**Y は誰も描いていない**（中 9）。§103.6 の `model` 欄とも矛盾 |
| 6 | 穴 4 つを同じ回で塞ぐ | ✅ | トグル 2 つ・キー名・ラベル共有・コメント訂正すべて |
| 7 | lcpp を入れない | ✅ | `ASSISTANT_AGENT_KINDS` に無い |
| 8 | 翻訳の鍵に kind とモデル | ⚠️ 逸脱 2 件 | 書き鍵と読み鍵が別式（重大 4）／先読み経路が未対応（重大 1）。②を鍵に入れる点は**設計より正しい**（§3 (b)） |
| 9 | feature は明示引数。ctx のタグを使わない | ✅ | `OneShotHeadless(ctx, feature, tier, …)`。**位置引数**（構造体にしない、という §103.5 の指示どおり）。8 呼び出し全部が `usagex.Feature*` 定数を渡している |

### §103.5 の不変条件

- 「ピンを先に見る」 ✅ `oneShotKind`（:1444）。理由のコメントまで入っている。
- 「①のモデルは落ちた先の kind のものだけ」 ✅ 実測。
- 「`resolveOneShot` を**3 箇所で共有**する（同じ答えが 3 箇所に出る唯一の作り方）」 ⚠️
  **共有していない**。`/ai-assist/resolution` は別関数 `oneShotKindCached` を通る。
  103-review 重大 4（カタログ列挙コスト）への対処としては正しい判断だが、
  設計が立てた不変条件は破れている。実害は「嘘をつく」ではなく「**答えないことがある**」で、
  その代償が重大 3（永久に unknown）である。103 の §103.5 を実態に合わせて訂正すること。
- 「seam は 3 本」 ✅ `AiFeatureAgentPref` / `AiFeatureModelPref` / `ChatReplySuggestEnabled`。

### §103.6 配線表

| 行 | 判定 |
|---|---|
| `chat_providers.go` の `resolveOneShot` ＋ feature 引数化（8 箇所） | ✅ |
| `ui_prefs.go` の 2 関数（hidden-models 除外を通す） | ✅（`ui_prefs.go:186-203`（hidden は :199）） |
| `uiprefs/prefs.go` の 2 関数・旧 `replySuggest` のフォールバックを書かない | ✅ 書いていない。doc に根拠の 1 文も入っている。ただし**移行 2 のフォールバックまで落とした**（重大 5） |
| 止める場所を名指し（`chat_plan.go:425` / `chat_suggest_reply.go:59`） | ✅ 両方。400 のテストも両方ある |
| `GET /ai-assist/resolution`（起動しない契約） | ⚠️ 契約は守られているが `model` を返していない（中 9）／温める主体がいない（重大 3） |
| `session_translate.go` の鍵＋旧鍵読み／**TS と対で動かす** | ❌ **TS 側も GET ハンドラも未対応**（重大 1） |
| CP の所有者側 1 行 | ✅ `control-plane/routes.go:807`、共有側に双子なし。golden も更新済み |
| `lib/aiAssistFeatures.ts` | ✅ |
| `AiAssistTab` 2 段構成・`settings.ts` の 4 キー・コメント訂正 | ✅ |
| **`recommendedModelId` を消す** | ❌ 未実施（§3 (a)） |
| ゲートを見る側（✨ 2 つを別キーに・計画のボタン） | ✅ `ChatView.tsx:1176-1177` |
| `guide/` を ja / en 両方＋翻訳の約束の文言 | ✅ 3 対すべて。`aiassist.note_mirror_translate` も「同じ本文・**同じエージェント/モデル**なら無料」に直っている |

### §103.9 移行 2 件

- 移行 1（旧鍵を読み続ける） ✅ 実測（`TestTranslateReadsLegacyKeyEntries`。
  `Kind`/`Model` 両方空を常にヒット扱い）。
- 移行 2（返信候補のゲートが初めて効く） ⚠️ ミラー側は ✅、**チャット側の継承がサーバに無い**
  （重大 5）。

---

## 3. 自己申告 2 件の判断

### (a) `recommendedModelId` の削除を見送った件 — **保留は妥当。ただし理由が違い、安い半分は今できる**

申告の理由（「§1 は kind×tier で 10 行、解決エンドポイントは feature 別 8 件なので、
繋ぐと 10 リクエストか、重大 4 のカタログ列挙コストが戻る」）は、**成り立っていない**。
そもそも **改訂版 103 自身が矛盾している**からである:

- **決定 5**: 「Y（モデル名）は **Console が持っているカタログで描く**」
  ——モデル名の解決は Console がやる、と言っている。
- **§103.6 の Console 行**: 「`recommendedModelId` を消す。**推奨の解決は Agent が答え**、
  Console はラベルを描くだけにする」——Agent がやる、と言っている。

この 2 行は両立しない。実装がここで止まったのは正しい（推測で片方を選ぶのは
103-review 重大 5 で指摘したのと同じ失敗の形になる）。
**ただし理由として書くべきは「コストが合わない」ではなく「103 が自分と矛盾していて、
どちらを採るか決まっていない」**である。そこが決まらないと、消すも残すも根拠が無い。

一方で、**この保留が残している欠陥（§103.8-7 の hidden models のずれ）は、
エンドポイントを 1 本も増やさずに今日直せる**。ずれているのは 1 行だけである:

```ts
// aiModelRow.tsx:57（現状・この差分では未変更）
const resolvedLabel = live.find(([id]) => id === recommended)?.[1] || recommended || tr("ui.default");
//                                                                   ^^^^^^^^^^^^^^ ここ
```

`live` は hidden models を除いたあとの一覧なので、推奨が非表示なら `find` は外れる。
Agent はそのとき `visibleModel()` が `""` を返して**CLI 既定に落ちる**
（`chat_providers.go:1527-1545` の `recommendedUtilityModel`）。ところが Console は `|| recommended` で
**除外したはずの ID をそのままラベルにする**。
＝ haiku を「使わないモデル」に入れた利用者の画面は「推奨（現在: haiku）」と読めるのに、
実際は claude の既定が走る。

```ts
// 直し（カタログ呼び出しもリクエストも増えない）
const visible = live.some(([id]) => id === recommended);
const resolvedLabel = visible ? live.find(([id]) => id === recommended)![1] : tr("ui.default");
```

**判断**:
1. 上の 1 行は**今回の PR で直す**（テストも 1 本。「hidden にすると推奨のラベルが既定になる」）。
2. `recommendedModelId` を消して 1 本化する件は **§103.11 に落とす**。
   理由は「決定 5 と §103.6 の Console 行が互いに矛盾しており、
   **Y を Agent に答えさせるのか Console に描かせるのかを 103 が決めていない**。
   決めてから着手する」と書くこと。中 9 と同じ決着が要る論点なので、まとめて 1 回で。

### (b) 翻訳キャッシュ鍵の逸脱 — **逸脱の指摘は正しい。線の引き方も実は正しい。間違っているのはコメントと命名**

3 つに分けて答える。

**1. 「実際に走った値は鍵にできない」という逸脱の指摘は正しく、設計の穴である。** ✅
読み側の鍵は実行前にしか作れないので、「実際に走った値」では作れない。
決定 8 の字面のほうを訂正すべきである。
ただし実装はその穴を**半分しか塞いでいない**——読みだけ予測に寄せ、書きは走行値のまま残した。
その結果が**重大 4**（書き鍵 ≠ 読み鍵 → 永久ミス。実測で 3 押下 3 走行）である。
正しい形は「**読みも書きも `translateCachePin()` の値を使う**」。

**2. 「線の引き方が狭いのではないか」という見立て——実装は既に広いほうになっている。** ✅（実測）
申告の「①のときだけ」は、**自分のコードの説明として誤り**である。

```go
// chat_providers.go:1460-1466
func resolveOneShot(feature string, tier OneShotTier) (kind, model string, configured bool, source string) {
    kind, source = oneShotKind(feature)
    if v, ok := aiFeatureModelPref(feature, kind); ok {   // ①
        return kind, v, true, source
    }
    v, ok := oneShotModelPref(kind, tier)                  // ②
    return kind, v, ok, source                             // ← ② の ok が configured になる
}
```

`configured` は**①でも②でも true** になる。`translateCachePin` はそれを
`configured && model != "" && model != AssistantRecommendedModel` で受けるので、
**②のティア設定も鍵に入る**。

probe で確かめた: `aiFeatureModels` を一切置かず `aiProseModels.claude` だけを
`tier-a` → `tier-b` に変えると `cached=false` になり再生成が走る。
＝ **「②のティアのモデルを変えても古い訳が出続ける」という懸念は、実際には起きていない。**

未設定の利用者が巻き込まれない仕掛けも効いている:
`DEFAULTS.aiProseModels` は全 kind が `"recommended"`（`settings.ts:1072-1078`）で、
`translateCachePin` はその番人を明示的に除外している。
つまり **§1 を触っていない人は kind だけの鍵**——キャッシュ破壊の回帰は無い。

**したがって、あなたの見立ての「正しい規則」＝「設定由来のモデル（①②）が在れば鍵に入れ、
③へ落ちるときだけ空」が、そのまま実装の挙動である。コードを狭める必要は無い。**

**3. 直すべきは、コードではなく「コードについて書いてあること」。**
いま①だけを語っているのは 4 か所——
`lookupTranslation` の doc（`session_translate.go:304-307`
"ONLY when the feature is explicitly pinned to one concrete model"）、
関数名 `translateCachePin`、変数名 `pinnedModel` / `pinnedOK`、
テスト名 `TestTranslateModelPinChangeMissesCache`。
**コードは②を見ているのに、4 か所そろって①だと書いてある。**
次の読み手が「コメントと実装が食い違っている」と気づいたとき、
どちらに寄せるかは五分五分である——狭いほうに寄せたら回帰する。

あわせて 2 つ書き足すこと:

- **②の case のテストが 1 本も無い**（このレビューは probe を書いて確かめた）。常設にする。
- **「推奨」に戻したときの非対称**: 具体モデル → 「推奨」に戻すと `pinnedOK` が false に落ちるので、
  **その具体モデルで作った訳が「推奨」のまま再利用される**。
  番人を鍵から外すこと自体は正しい（「推奨」はライブカタログ次第で動くので、
  鍵に入れると押すたびに外れる）が、この非対称は意図して受け入れた性質として
  1 文書いておくべきである。

---

## 4. まとめ

- 設計レビュー 14 件の反映は**良好**。重い 3 つ（決定 9 の明示引数化・サーバ側ゲート・seam 3 本目）は
  設計どおりで、陽性対照も本物だと変異テストで確認した。
- 新規の重大 5 件のうち **4 件が決定 8（翻訳キャッシュ）**に集まっている。
  これは実装の粗さではなく、**決定 8 の「実際に走った値を鍵にする」が構造的に無理**
  （読み側は実行前にしか鍵を作れない）という設計の穴が、実装で表に出たものである。
  103 の決定 8 を「解決時に決めた値」へ訂正し、`putTranslation` と
  `handleSessionTranslations` の 2 か所を同じ式に揃えれば、重大 1・4 と中 7・8 は一度に片付く。
- 残る重大 2・3・5 は独立していて、どれも数行で直る。
