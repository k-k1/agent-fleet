# 103-final-review. 機能別 AI 補助の最終確認（`temp/szmqaum`）

- 対象: `git diff a0c85a85 origin/temp/szmqaum`（実装 3・設計側 2 = 5 コミット、+2145/-215）
- 前段: [103-review.md](103-review.md)（設計）/ [103-impl-review.md](103-impl-review.md)（実装・13 件）
- 設計: [103-ai-assist-per-feature.md](103-ai-assist-per-feature.md)
- 日付: 2026-09-21。レビューのみ。実装コードと 103 本体は書き換えていない。
- **「実測」は走らせて得た結果、「コード読解」は読んだだけ**。混ぜない。

**このレビューで走らせたもの**（すべて `git archive` で `/tmp` に展開した使い捨てのコピー。
自分のワークツリーにも `agent-fleet@wip-so5kjd7` にも実装コードは入れていない）:

| 何を | 結果 |
|---|---|
| `go build ./...` / `go vet ./...`（`workspace/agent`） | 緑 |
| `go test -count=1 -p 2 ./...`（完全な木） | **緑** |
| `go test -race -count=1 ./internal/chatx/ .` | main 緑。chatx は **pre-existing の競合 1 件のみ**（観察 B） |
| console `npm test` | **305 files / 3177 tests 緑**（1 skip） |
| console `npm run typecheck` | 緑 |
| console `npm run lint` | **実行できず**（`oxlint` が共有 `node_modules` に無い・exit 127）。未確認と明記する |
| `python3 scripts/docs-check.py`（対象ブランチの木） | 432 files / 0 error |
| **変異テスト 4 本** | §0.2 |
| **使い捨て probe 6 本**（Go 4・dom 2） | §0.1 |

---

## 0. 結論

**13 件は全部が実装され、そのうち 11 件は完全に閉じている。**残っているのは 6 件——
重大は無く、**中 3・軽 3**。うち 2 件（中 1・中 2）は**同じ 1 か所**、決定 8 の
「読み・書きが 1 つの式を共有する」がモデルには適用されて **kind には適用されていない**
ことに由来する。並行性の修正（in-flight ガードと 9a94f2b4 の `defer` 化）は
**正しく、インシデントの再発を実測で止めている**。

### 0.1 前回の実測 2 件を当て直した結果

| 前回の指摘 | 今回の実測 |
|---|---|
| 重大 1: 先読みと押下で違う訳 | **解消**。A→B→A で `press="A-TRANSLATION" / prefetch="A-TRANSLATION"`。A→B（戻さない）でも一致 |
| 重大 2: 1 mount で 8 リクエスト | **解消**。`ai-assist/resolution fetches on a single mount = 1` |
| 重大 4: 書き鍵 ≠ 読み鍵 | **モデルは解消・kind は残存**。走行 kind が解決 kind と違うと `kind="codex" model="m-claude"` が書かれ、3 押下 3 走行（中 1） |

### 0.2 変異テストで確かめた「陽性対照が本物か」

| 潰したもの | 期待して見張るテスト | 結果 |
|---|---|---|
| `handleAIAssistResolution` の `go chatx.WarmOneShotKind` | `TestAIAssistResolutionWarmsAfterAnUnknownAnswer` | **赤**（本物） |
| `uiprefs.ChatReplySuggest` の旧キーへのフォールバック | `TestHandleChatSuggestRepliesFallsBackToMirrorKey…` | **赤**（本物） |
| `oneShotKind` のピン分岐（前回の再走） | `TestOneShotKindPinBeatsPriorityOrder` | **赤**（本物） |
| **先読みの `translationMatches`**（押下側はそのまま） | `TestTranslatePrefetchMatchesPress` | 🔴 **緑のまま**（中 2） |

---

## 1. 残っている欠陥

### 【中 1】翻訳の書き鍵は、モデルだけ解決値・**kind だけ走行値**のまま

- **何が**: 決定 8 の訂正は「押下の時点で読み・書きが共有する 1 つの式」。モデルはそうなったが、
  `Kind` は走行値（`OneShotHeadlessRun` の戻り）のままで、**同じ行に 2 つの出所が混ざっている**。
- **どこ**: `workspace/agent/session_translate.go:574-587`。
  `_, cacheModel, cacheModelOK := resolveCacheModel()` と、解決側の kind を **明示的に捨てて**
  `putTranslation(… Kind: ranKind, Model: model …)`。
  根拠として書かれているのは :577-578 の
  「Kind alone is taken from the run: it never drifts from the prediction」。
- **実測**（probe B）: 走行が解決と違う kind を報告すると、
  ```
  press 1: cached=false calls=1
  press 2: cached=false calls=2
  press 3: cached=false calls=3
  stored row: kind="codex" model="m-claude"
  ```
  ＝ **3 押下 3 走行・永久ミス**。重大 4 で測ったのと同じ結果が、kind 経由で残っている。
  しかも書かれた行は内部矛盾している（codex の kind に claude のティアモデル）。
- **なぜ問題か**（「drift しない」が成り立たない理由・コード読解）: 解決と走行は
  **別々の `resolveOneShot` 呼び出し**で、間に生成そのものが挟まる。
  - `resolveCacheModel` は `onceCacheModel` で遅延される（軽 12 の対処）。**ストアが空の初回**は
    `lookupTranslation` が `resolve()` を呼ばないので、解決は `translateOneShot` が**返ったあと**に
    初めて走る——窓は**生成時間まるごと**（prose で数秒〜数十秒）。
  - 可用性キャッシュは 1 分。生成の途中で期限が切れれば、走行側と解決側は別の答えを見る。
  - ログイン失効（`reconcileChatCreds`）でも切り替わる。
- **どう直すか**: 1 行。
  ```go
  cacheKind, cacheModel, cacheModelOK := resolveCacheModel()
  // …
  putTranslation(name, &sessionTranslation{Hash: hash, Lang: lang, Kind: cacheKind, Model: model, …})
  ```
  あわせて 103 側の**取りこぼし 1 文**——§103.5 の
  「翻訳は『**実際に走った kind / model**』を要る（決定 8）ので、それを返す `OneShotHeadlessRun` を
  足し」（`103-ai-assist-per-feature.md:155-157`）——は決定 8 の訂正に取り残されており、
  いまのコードの根拠として残っている。ここも「解決時に決めた kind / model」へ直すこと。
  （`OneShotHeadlessRun` 自体は台帳の突き合わせに使えるので、残してよい。）

### 【中 2】重大 1 のテストは、**照合規則**の陽性対照になっていない

- **何が**: `TestTranslatePrefetchMatchesPress` は **A→B→A**（ピンを変えて戻す）で、
  この形は先読みの**先着優先の短絡だけ**で満たされる。照合（`translationMatches`）が
  効いているかは検査していない。
- **どこ**: `workspace/agent/session_translate_test.go:633`。
  テスト自身のコメント（:653-656）も「A naive 'last entry in append order wins' reader (the bug)」と、
  順序の話だけを目標に書いている。
- **実測**（変異）: `handleSessionTranslations` の
  `if translationMatches(e, kind, model, modelOK)` を無効化し、
  **先着優先の短絡と `resolveCacheModel()` の呼び出しはそのまま残す**と——
  - `TestTranslatePrefetchMatchesPress` … **PASS**（気づかない）
  - 同時に当てた A→B の probe … `press="B-TRANSLATION" / prefetch="A-TRANSLATION"`
    ＝ **重大 1 がそのまま再発している**
- **なぜ問題か**: 「順序を保ったまま照合を落とす」リファクタが緑で通る。
  しかも A→B（ピンを 1 回変えて戻さない）は A→B→A より**ありふれた**操作で、
  そこだけが素通しになる。**中 7 と同じ形が 2 度目**——
  「テストが通る経路」と「テストが検査したい規則」がずれている。
- **どう直すか**: A→B を 1 ケース足す（戻さない）。A→B は照合を要求し、A→B→A は順序を要求するので、
  **両方残す**のが正しい。§103.10 の検証計画にも「先読みは**ピンを 1 回変えただけ**で
  押下と一致すること」と書き分ける。

### 【中 3】ピン済みでモデル未設定のカードが「推奨」と表示する（実効は §1 のティア設定）

- **何が**: 中 9 で新設した「いま使うのは X / Y」の Y が、**①未設定を「推奨」と読み替える**。
  ①が無ければ Agent は②（`aiShortModels`/`aiProseModels[kind]`）へ落ちるのに、画面は③相当の
  「推奨」を出す。
- **どこ**: `console/src/features/settings/personal/AiAssistTab.tsx:95`
  ```ts
  const modelValue = pin ? s.aiFeatureModels?.[f.id]?.[pin] || "" : tierModels?.[modelKind] || "";
  ```
  `|| ""` で「未設定」が潰れ、`aiModelRow.tsx:60` の `if (!value || value === ASSISTANT_RECOMMENDED_MODEL)`
  が「推奨」を返す。**ピンしていない側の枝は正しくティア既定を見ている**——ピン側だけが落ちている。
- **実測**（dom probe）: `aiFeatureAgents={"translate.mirror":"codex"}` /
  `aiFeatureModels={}`（＝「既定（上の設定に従う）」）/ `aiProseModels={"codex":"gpt-5-mini"}` で
  ```
  effective model per the Agent = aiProseModels.codex = "gpt-5-mini"
  the card says -> Currently uses: Codex / Recommended (currently: gpt-5.6-luna)
  ```
  モデル名も、由来（「推奨」）も、どちらも違う。
- **なぜ問題か**: 中 6 でピッカーには「既定（上の設定に従う）」を足して直した穴が、
  **その真下に新設した説明行**で開き直している。84 の 3 番目の原則（表示と実効を一致させる）で、
  この一連の指摘がずっと追ってきたものそのもの。
- **どう直すか**:
  ```ts
  const override = pin ? s.aiFeatureModels?.[f.id]?.[pin] : undefined;
  const modelValue = override ?? tierModels?.[modelKind] ?? "";
  ```
  ただしこれだけでは足りない: `useResolvedModelLabel` は **「未設定」と「明示的な空」を区別できない**
  （Agent は `assistantModelPref` の `ok` で区別し、空＝「CLI 既定」として `--model` を渡さない）。
  `value: string | undefined` を取り、`undefined`＝推奨／`""`＝`ui.default` と分けること。
  dom テストは「ピン＋override 無し＋ティアに具体モデル」で 1 本。

### 【軽 4】in-flight の待ち手は、**期限切れ**のキャッシュ値を受け取る（コメントと不一致）

- **何が**: 待ち手はリーダーを待ったあと `headlessAvail[kind]` を**鮮度を見ずに**読む。
- **どこ**: `workspace/agent/internal/chatx/chat_providers.go:125-132`。
  一方 :137-143 のコメントは「A leader that dies must still hand the waiters an answer (**false**)」。
- **実測**（probe）: 10 分前の `true` が残った状態でリーダーの検査が panic すると——
  ```
  waiter got true after the leader panicked (checks run = 1)
  RESULT: the waiter got the EXPIRED cached value, not the documented false
  ```
  **詰まりはしない**（9a94f2b4 の `defer` 化はそこを確かに直している）。
- **なぜ問題か**: 待ち手は「1 分以内の答え」という契約を破った値で動く。実害は
  「落ちている CLI で走ろうとして失敗する（優先順位へ落ちない）」で、一過性・限定的。
  むしろ問題は**安全側の経路のコメントが自分のコードと違う**ことで、
  次に読む人がこの分岐を信用して設計する。
- **どう直すか**: 待ち手側も鮮度を見る。
  ```go
  <-wait
  headlessAvailMu.Lock()
  t, ok := headlessAvailAt[kind]
  v := ok && time.Since(t) < time.Minute && headlessAvail[kind]
  headlessAvailMu.Unlock()
  return v
  ```
  （これならコメントの「false を渡す」が本当になる。）

### 【軽 5】`WarmOneShotKind` の doc が、**同じ PR が入れたガードを否定している**

- **どこ**: `chat_providers.go:1584-1587`
  > headlessAgentAvailable does NOT hold headlessAvailMu across the exec call …
  > so two callers racing on the same cold kind can each still run their own `auth status` —
  > this warms at most a handful of kinds every ~10s poll, **not per request**,
  > so the occasional doubled-up call is **not worth a dedicated in-flight guard**.
- **なぜ問題か**（コード読解）: 2 つとも事実に反する。
  1. **その in-flight ガードは 1500 行上に、postmortem つきで存在する**
     （`headlessAvailInFlight`、:66-83）。この一文は次の読み手に「ガードは要らない」と読ませる——
     ~490 プロセス・25GiB・OOM を出したのと同じ判断へ戻す形。
  2. 「not per request」も違う。`handleAIAssistResolution`（`ai_assist.go:79-82`）は
     **unknown だった feature ごとに `go` を 1 本**撃つので、**1 リクエストあたり最大 8 本**である
     （ガードがそれを kind ごと 1 exec に畳むから安全なのであって、本数が少ないからではない）。
- **どう直すか**: この段落を削り、「**ガードがあるから安全**」へ置き換える。
  インシデント再発防止のコメントは、ガードの存在を指していなければ意味を持たない。

### 【軽 6】先読み `GET /translations` が CLI を起動しうる（軽 12 の移動先）

- **どこ**: `session_translate.go:624` の `resolveCacheModel()`。遅延はされているが、
  **その言語に非 legacy の行が 1 つでもあれば呼ばれる**。
  `resolveCacheModel` は自分の doc（:390-392）が明記するとおり shell out しうる。
- **なぜ問題か**（コード読解）: 読み手は `console/src/features/mirror/useTranslate.ts:128-141` で、
  **ペインを開くたび**（`[session, lang, enabled, apply]`）に叩く。可用性キャッシュが冷えていれば
  最大 5 本の `auth status` を待ってから既存の訳が出る。以前はディスク I/O だけだった。
  旧エントリが legacy のうちは無料なので、**運用とともに悪化する**（新しい訳が増えるほど当たる）。
  in-flight ガードが本数は抑えるので重大ではない。
- **どう直すか**: `/ai-assist/resolution` のために作った「覗くだけ」の入口
  （`ResolveOneShotCached`）をここでも使い、キャッシュが冷えていれば**照合を諦めて
  legacy と同じ扱いにする**か、行を出さない。§103.5 の「規則は 1 つ・入口は 2 つ」に、
  先読みがどちらの入口かを 1 行書くこと。

---

## 2. 観察（欠陥として数えないもの）

### 観察 A: チャット ✨ ゲートのテストは、**赤くなると実 CLI を起動して実トークンを使う**

変異テスト中に偶然踏んだ。`uiprefs.ChatReplySuggest` のフォールバックを外したところ、
`TestHandleChatSuggestRepliesFallsBackToMirrorKeyBeforeConsoleMigrationLands` が
400 の代わりに **200 と、Claude Haiku 4.5 の実際の応答本文**を返した
（本文に `Model: Claude Haiku 4.5 (claude-haiku-4-5-20251001)` が入っていた＝実測）。

つまりこれらのゲートテストは「**緑＝CLI を起動しない／赤＝実 CLI が走って課金される**」という
性質を持つ。このブランチ自身の postmortem
（`headlessAvailInFlight` の doc）が立てた
「ループしうるテストで実 CLI に触れない」という規則と噛み合わない——ゲートの回帰は
定義上「赤」であり、そのときこそ CLI が走る。

このブランチで新設されたテストだけの性質ではない（前ラウンドの
`TestHandleChatSuggestRepliesGatedByItsOwnKey` も同じ）ので欠陥としては数えないが、
生成の seam を差し替えるか、`PATH` から CLI を外して走らせるのが筋である。

### 観察 B: `-race` が 1 件報告するが、**このブランチのせいではない**

`go test -race ./internal/chatx/` が `TestSessionReportHeldWhileAutoResuming` で DATA RACE を出す
（`helpers_test.go:156` の `stubAbortResumeHolds` の書き込みと、reconciler goroutine からの
`seams.go:123` の読み）。**base `a0c85a85` でも同じ 1 件が同じ形で再現する**ので、
103 の作業とは無関係の既存の問題である（docs/log/47 の report-abort 系 seam）。

103 が新設した `headlessAvailCheck` の seam には、`-race` で競合は出ていない。

---

## 3. 13 件の反映状況

| # | 指摘 | 状態 | 確かめ方 |
|---|---|---|---|
| 重大 1 | 先読みが新しい鍵を知らない | ✅ 閉 | **実測**（A→B→A・A→B とも一致）。ただしテストは中 2 |
| 重大 2 | 1 mount で 8 リクエスト | ✅ 閉 | **実測** 1 回／mount。`AiAssistTab` で 1 回呼び prop 渡し |
| 重大 3 | 誰も温めないので永久に unknown | ✅ 閉 | **変異で陽性対照を確認**。応答を書いたあと `go WarmOneShotKind`。ヘッダも「応答経路の契約」へ |
| 重大 4 | 書き鍵 ≠ 読み鍵 | ⚠️ **モデルのみ閉・kind 残存** | **実測**（中 1） |
| 重大 5 | チャット ✨ の継承が Agent に無い | ✅ 閉 | **変異で陽性対照を確認**。`ChatReplySuggest` が `replySuggestEnabled` へ落ちる。`deps_test` も本物の関数を配線 |
| 中 6 | モデル欄に「§1 に従う」が無い | ✅ 閉 | `AI_FEATURE_MODEL_FOLLOW_DEFAULT`＝**キーの削除**にマップ（番人を保存しない）。空になった入れ子も削除（§103.8-5 と整合） |
| 中 7 | ピンのテストがピン経路を通らない | ✅ 閉 | `SetHeadlessAvailableForTest` を export、`TestTranslatePinRoutesToPinnedKind…` が使用 |
| 中 8 | ②が鍵に入ることを誰も言っていない | ✅ 閉 | `translateCacheModel` / `modelOK` に改名、doc は①②を明記、「推奨」の非対称も 1 文。`TestTranslateTierModelChangeMissesCache` 新設 |
| 中 9 | Y がどこにも描かれない | ⚠️ **描かれたが表示が違う**（中 3） | **実測** |
| 中 10 (a) | `recommendedModelId` の hidden models ずれ | ✅ 閉 | `useResolvedModelLabel` に一本化し、非表示なら `ui.default`。`AiModelRow.dom.test.tsx` 新設。削除本体は §103.11 へ（理由も「矛盾」に訂正済み） |
| 軽 11 | `tier` 未使用 | ✅ 閉 | Go 側から削除 |
| 軽 12 | 全ヒット押下に CLI 起動 | ✅ 押下は閉／⚠️ **先読みに移動**（軽 6） | `onceCacheModel` で遅延。`TestLookupTranslationDefersResolveForLegacyHits` 新設 |
| 軽 13 | Deps スタブが hidden models を落とす | ✅ 閉 | `TestResolveOneShotHonoursHiddenModels` を package main に置き、本物の配線で通した |

**設計側（2a0d0a10）**: 決定 5 の一本化、決定 8 の「実際に走った値」撤回、
§103.5 の「規則は 1 つ・入口は 2 つ」、§103.13 のまとめ——いずれも実測と一致しており、
(a)(b) の判断もそのまま採られている。**唯一の取りこぼしが §103.5:155-157**
（「翻訳は『実際に走った kind / model』を要る」）で、これが中 1 のコードの根拠として残っている。

---

## 4. 並行性の修正について（最優先で見た結論）

**ガードは正しい。**

- **リーダー／待ち手の受け渡し**: 完了時はキャッシュ書き込みが `close(done)` の**前**、
  同じロックの中で行われるので、待ち手が古い値を読む窓は無い（コード読解）。
- **本数の上限**: 冷えた 1 kind に対し `headlessAvailCheck` は 1 回だけ。
  ブランチのテスト（50 並列＋2ms ポーリング）がそれを固定しており、**実 CLI には一切触れない**
  （`headlessAvailCheck` の seam 経由）。インシデントの形が再現できないことを確かめる作りとして
  正しい。
- **9a94f2b4（`defer` 化）は妥当で、必要**。リーダーの panic でチャネルが閉じられないと
  「一過性の障害が恒久的な障害になる」というのは、`headlessAgentAvailable` が
  **アシスタントチャットを含む全経路**の入口であることを考えると、直す価値が十分ある。
  「返ってきた検査だけキャッシュする」（`completed` フラグ）も正しい——panic のゼロ値を
  1 分固定すると、一過性の失敗が 1 分間の「未接続」に化ける。
  **実測でも待ち手は詰まらない**（probe）。
- 残る指摘は 2 つとも**コメント側**である（軽 4 の「false を渡す」・軽 5 の
  「ガードに見合わない」）。コードの欠陥ではないが、どちらも**インシデントの再発防止として
  書かれた文が事実と違う**ので、直す優先度は低くない。

`SetHeadlessAvailCheckForTest` / `SetHeadlessAvailableForTest` /
`ClearHeadlessAvailableForTest` の 3 つは、いずれも restore を返す形で漏れない。
`headlessAvailCheck` は `headlessAvailMu` の外で読まれる package 変数なので、
**検査中に restore が走ると理屈の上では競合する**（デタッチした `WarmOneShotKind` goroutine が
テスト終了後も走りうる）——`-race` で全パッケージを回して**この競合は出なかった**ので、
いまのテストの書き方では踏んでいない。実際に踏むなら `t.Cleanup` の restore を
「in-flight が空になるまで待つ」形にするのが確実だが、現時点で観測されていない以上、
指摘としては挙げない。

---

## 5. まとめ

- **重大は残っていない。**中 3 件・軽 3 件。
- 中 1 と中 2 は**同じ 1 か所**（決定 8 の「1 つの式」が kind に適用されていない／
  その照合規則にテストが効いていない）で、**どちらも数行**で閉じる。
- 中 3 は、中 6 で直した穴が**その真下の新しい行**で開き直しているもの。
  ①未設定を②へ落とすだけで閉じるが、「未設定」と「明示的な空」を分ける必要がある。
- 軽 4・軽 5 は**コメントが自分のコードと違う**類で、しかも両方ともインシデントの
  再発防止として書かれた箇所にある。コードより先に直す価値がある。
- 軽 6 は軽 12 が押下から先読みへ移っただけで、悪化は緩やかだが、放置すると
  「ペインを開くと数秒固まる」として戻ってくる。
