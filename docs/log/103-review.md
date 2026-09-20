# 103-review. 「AI 補助を機能ごとに指定する」設計への批判的レビュー

- 対象: [103-ai-assist-per-feature.md](103-ai-assist-per-feature.md)（状態: 設計、2026-09-20）
- 日付: 2026-09-20。レビューのみ。実装・設計文書の書き換えはしていない。
- 読み方: 各件は **何が / どのファイル:行 / なぜ問題か / どう直すか**。
  **「実測」と書いた行はコードを開いて確かめたもの、「推測」と書いた行は憶測**。
  ビルドもテストも走らせていない（依頼の範囲外）。

---

## 0. 事実の裏取りの結果（先に結論）

設計文書が既存コードについて断定している 5 点を、1 つずつコードで確かめた。

| 申告 | 判定 | 根拠 |
|---|---|---|
| §103.5 タグ付き ctx が `OneShotHeadless` まで届く | **正しい**（8 経路すべて実測） | 下の §0.1 |
| §103.3-3 返信候補のサーバ側ゲートが効いていない | **正しい**（書き手 0 件を再確認） | 下の §0.2 |
| §103.2 の 8 機能表（面・ティア・トグル・env） | **中身は正しい。9 つ目は無い。ただし本文の「7 箇所」は誤り** | 下の §0.3 / 指摘 8 |
| §103.3-2 `plan.update` にトグルが無い／`replySuggestEnabled` が 2 機能を兼ねる | **正しい** | 下の §0.4 |
| §103.3-5 `migrateAiAssistPrefs` のコメントが実装と逆 | **正しい**（引用行も一致） | 下の §0.5 |

### 0.1 ctx は本当に届いているか（§103.5 の土台）

`OneShotHeadless` の**テストでない呼び出し元 8 箇所**について、タグを載せた ctx が
そのまま渡っているかを全経路で追った。差し替え・`context.Background()` の作り直し・
goroutine 跨ぎは **1 件も無い**。

| # | `OneShotHeadless` の行 | タグを載せる行 | ctx の素性 | 判定 |
|---|---|---|---|---|
| 1 | `session_title.go:190` | `:122` | goroutine 内で `Background`＋`WithTimeout`→ タグ → `runTitleSuggestLLM(ctx,…)` | 届く |
| 2 | 同上 | `:590` | `r.Context()` → タグ → 同関数 | 届く |
| 3 | `session_title.go:725` | `:789` | `r.Context()`＋`WithTimeout` → タグ → `runBranchSuggestLLM` | 届く |
| 4 | `session_suggest_reply.go:293` | `:324` | `r.Context()`＋`WithTimeout` → タグ → `runReplySuggestLLM` | 届く |
| 5 | `chat_plan.go:357` | `:353` | 呼び出しの直前、同じ関数内 | 届く |
| 6 | `chat_suggest_reply.go:44` | `:74` | `r.Context()`＋`WithTimeout` → タグ → `runChatReplySuggestLLM` | 届く |
| 7 | `chat_title.go:75` | `:107` | `r.Context()`＋`WithTimeout` → タグ → `runChatTitleSuggestLLM` | 届く |
| 8 | `session_translate.go:318` | `:461` | singleflight（`translateShared`）の**中**で `r.Context()` から作り直し → タグ → `translateOneShot` | 届く |

- #1 は `go generateSessionTitle(name, turns)`（`session_title.go:109`）で goroutine を跨ぐが、
  **ctx は goroutine の中で作られてから**タグが載る。跨いでいるのは ctx ではなく引数。
- #8 は飛行に**相乗りした側**が指揮者の結果を受け取る形（`session_translate.go:451-473`。
  コメント自身が「タグは LEADER のもの」と書いている）。相乗り側も同じ feature なので、
  機能別解決の観点では破綻しない。
- #4 と #6 は `translateOneShot` / `editSuggestLLM` と同じく **`var` の seam 越し**
  （`session_translate.go:317` / `fs_suggest_edit.go:214`）だが、ctx はそのまま素通りする。

**したがって §103.5 の土台は今日のコードでは成立している。** ただし「成立している」と
「守られている」は別で、そこが指摘 1。

### 0.2 返信候補のゲート（§103.3-3）

- 読み手: `workspace/agent/internal/sessionx/session_suggest_reply.go:111`
  — `uiprefs.Read()["replySuggest"].(bool)`、`ok=false` なら true。
- 書き手: **リポジトリ全体で 0 件**。`grep -rn '"replySuggest"'`（node_modules 除く）の
  ヒットはこの読み手 1 行だけ。Console は `replySuggestEnabled`
  （`console/src/lib/settings.ts:539,1099` / `AiAssistTab.tsx:78`）。
  CP・移行コード・テスト・ui-prefs の PUT ハンドラのいずれにも別名化は無い。
- 履歴: `git log --all -S'replySuggest'` で該当は `43907e7f`（返信サジェスト v2）1 本。
  **そのコミットメッセージ自身が**「agent: … ＋ `replySuggest` ui-pref ゲート」と
  「settings: `replySuggestEnabled`（KeysTab トグル・既定 ON）」を並べて書いている。
  初日から食い違っていたという申告は正しい。
- 効き先は 2 箇所: `session_suggest_reply.go:308`（ミラー）と
  `chatx/chat_suggest_reply.go:59`（チャット、`seams.go:107` → `chat_wiring.go:59` で
  同じ `sessionx.ReplySuggestEnabled` に束ねられている）。**両方とも常に true に倒れる。**

### 0.3 8 機能表と「9 つ目」

- 表の **面・ティア・トグル・env 既定は 8 行すべて実測と一致**。env は
  `AF_TITLE_MODEL`=haiku（`session_title.go:184`）、`AF_SUGGEST_MODEL`=haiku（`:105`）、
  `AF_EDIT_SUGGEST_MODEL`=sonnet（`fs_suggest_edit.go:67`）、
  `AF_PLAN_MODEL`=sonnet（`chat_plan.go:121`）、
  `AF_TRANSLATE_MODEL`=sonnet（`session_translate.go:75`）。
- ティアも一致（`OneShotShort` 5 本 / `OneShotProse` 3 本）。
- **9 つ目の補助機能は無い。** 台帳の feature 定数（`usagex/ledger.go:29-52`）を全部当たると、
  8＋境界 5（`assistant.chat` / `assistant.ask` / `assistant.autoturn` / `assistant.bridge` /
  `compact`）＋`session` / `tool.imagegen` / `engine.llm` / `unknown` で閉じる。
  `usagex.WithTag` の呼び出し元も 14 箇所すべて確認し、この対応から漏れるものは無かった。
- ただし**呼び出し元の数え方が誤っている**（指摘 8）。境界の**理由**も 1 件事実と違う（指摘 7）。

### 0.4 トグルの穴（§103.3-2）

- `plan.update`: `HandleChatPlanRefresh`（`chat_plan.go:425`）に **feature ゲートが無い**
  ——`uiprefs` にも `PlanUpdate` に当たる関数が無い（`uiprefs/prefs.go` 全文確認）。申告どおり。
- `replySuggestEnabled` が `suggest.session` と `suggest.chat` を 1 キーで止める: §0.2 のとおり。
  （正確には「**止めるはずだった**」。今は画面だけが消える。）

### 0.5 `migrateAiAssistPrefs`（§103.3-5）

`console/src/lib/settings.ts:924-929` の関数ドキュメントは
「aiProseModels は意図的な例外／prose 側は用途に合った推奨へ戻す」と書き、
`:943-946` の行内コメントは「両方が旧 utility を継ぐ＝リリース前の挙動をそのまま運ぶ」と
**逆のこと**を書き、実装 `:949-950` は行内コメントのほうに従っている。
引用行も申告どおり。**関数ドキュメントだけが覆す前の版のまま。**

---

## 1. 指摘（重大度順）

### 【重大 1】台帳のタグを設定解決の入力に使うと、観測用の軸が挙動を決める軸になる

- **何が**: §103.5 の中核——`OneShotHeadless` の中で `usagex.TagOrUnknown(ctx).Feature` を読み、
  そこから機能別のエージェント／モデルを解決する。
- **どこ**: `workspace/agent/internal/usagex/call.go:8`,`:24-30` /
  `workspace/agent/internal/chatx/chat_compact.go:123` /
  `workspace/agent/internal/chatx/chat_providers.go:1541`
- **なぜ問題か**（実測にもとづく）:
  1. `usagex/call.go:8` が**契約を明文で逆向きに書いている**——
     「どこで呼ばれたかではなく、何のための呼び出しかを記録する。だからそれだけが ctx に乗る。
     **呼び出し場所を変えても、何として記録されるかは変わってはならない**」。
     この設計はその ctx 値を読んで**何が走るか**を変える。以後は
     「記録用のラベルを動かすと、走る CLI とモデルが変わる」になる。
  2. タグは現に**可変メタデータとして扱われている**。`chat_compact.go:117-124` は
     チャットのターンの途中で `assistant.chat` のタグを `compact` に**上書き**する
     （コメント自身が「上書きしないと外側の assistant.chat に数えられる」と書く）。
     いま `compactConversation` は `prov.Send` を通るので当たらないが、
     将来どれか 1 つが `OneShotHeadless` 経由になった瞬間、**上書きされた feature のピンで走る**。
     台帳の都合でタグを付け替えるのは今後も正当な操作であり続けるので、
     この地雷は「気をつける」では消えない。
  3. §103.5 は「タグ無しは今日と同じ、設定が効かないだけで壊れない」と言うが、
     `FeatureUnknown` は**タグを忘れる事故が現に起きる**という前提で置かれている定数
     （`usagex/ledger.go:49-52`）。タグ忘れ＝**設定が黙って効かない**は、利用者から見れば
     §103.3-3 とまったく同じ「画面はオンなのに実効が違う」事故である。
  4. コンパイラも `chatx.Configure` の反射チェックも、これを守らない。反射チェックが見るのは
     `Deps` のフィールドが埋まっているかだけ（`deps.go:115-129`）で、
     「呼び出し元が ctx にタグを載せたか」は検査対象外。
- **どう直すか**: `OneShotHeadless` に **feature を明示引数で渡す**
  （`OneShotHeadless(ctx, feature, tier, persona, prompt, claudeModel)`、
  あるいは引数が 6 つになるのを嫌うなら `OneShotSpec{Feature, Tier, Persona, Prompt, ClaudeModel}`）。
  触る行は **8 行**で、どれも既に隣の行で同じ定数を書いている（§0.1 の表）。
  「呼び出し元を 1 行も触らない」で買えるのは 8 行の diff であり、
  売るのは**コンパイラによる強制**と**観測軸と挙動軸の分離**。割に合っていない。
  台帳側は今までどおり ctx のタグを読めばよく、2 つの軸が一致することは
  「`WithTag` の直後に同じ定数を渡す」という目で見える形で担保される。

### 【重大 2】新しいトグル 2 つの「サーバ側で止める場所」が設計に無い

- **何が**: §103.6 の Agent 行は `uiprefs/prefs.go` に `PlanUpdate()` と `ChatReplySuggest()` を
  **「新設」するとだけ**書き、§103.6 の Console 行は「計画更新のボタンを `planUpdateEnabled` で
  描かない」と書く。**どのハンドラがその値を見て 400 を返すのかが、どこにも書かれていない。**
- **どこ**: `workspace/agent/internal/chatx/chat_plan.go:425-453`（`HandleChatPlanRefresh`——
  実測、feature ゲートは現在 1 行も無い）/ `chatx/chat_suggest_reply.go:59`（現状は
  共有キーを読む `replySuggestEnabled()`）
- **なぜ問題か**: これは §103.3-3 で自分が掘り当てた不具合と**同じ失敗の形**である。
  「Console がボタンを消す」＋「サーバが止めない」＝ 84 の 3 番目の原則（表示と実効を一致させる／
  サーバの拒否は防御として残す）が成立しない状態。`POST /chat/conversations/{id}/plan/refresh`
  は CP の allowlist に載っている REST で、`AGENT_TOKEN` を持つものなら誰でも叩ける。
  §103.10 の検証計画も**返信候補の 400 しか測らない**ので、この穴はテストにも掛からない。
- **どう直すか**: §103.6 の Agent 行を 2 本に割り、**enforcement point を名指しする**。
  - `chat_plan.go:425` の先頭に `if !uiprefs.PlanUpdate() { 400 errCodeTitleFeatureDisabled }`
    （`fs_suggest_edit.go:224` / `chat_title.go:92` と同じ形）。
  - `chat_suggest_reply.go:59` を新しいチャット専用ゲートへ（指摘 3 も参照）。
  - §103.10 に「**新トグル 2 つとも、オフで 400 が返ることを直接叩いて確認する**」を足す。
    片方だけ測る計画は、片方だけ直った状態を緑にする。

### 【重大 3】`chatx.Deps` の seam は 2 本では足りない——3 本要る

- **何が**: §103.5 は「`chatx.Deps` に seam を 2 本足す（`AiFeatureAgentPref` /
  `AiFeatureModelPref`）」と書く。
- **どこ**: `workspace/agent/internal/chatx/deps.go:83`（`ReplySuggestEnabled func() bool`）/
  `chatx/seams.go:107` / `workspace/agent/chat_wiring.go:59`
  （`ReplySuggestEnabled: sessionx.ReplySuggestEnabled`）
- **なぜ問題か**: チャット ✨ を `assistantReplySuggestEnabled` で独立させる（§103.9 の表）には、
  `chat_suggest_reply.go:59` が読む `replySuggestEnabled()` を**ミラー側と別の関数に束ね直す**
  必要がある。いまは `Deps` の 1 フィールドが両方を兼ねており、`chatx` から `uiprefs` の
  新関数を直接呼ぶことはできない（`chatx` → main の逆依存は `Deps` 1 本に閉じる決まり、
  `deps.go:3-17`）。よって **3 本目**（`ChatReplySuggestEnabled`）が要る。
  フィールドを足せば `Configure` の反射チェックに自動で入る一方、
  `deps_test.go:107` と `sessionx/deps_stub_test.go:158` の両方を動かすことになる。
  §103.5 の「2 本」という数字のまま実装に入ると、この 3 本目は配線漏れとして
  **起動時 panic** で見つかる（幸い黙って通ることはない）が、設計の見積りは外れる。
- **どう直すか**: §103.5 を「seam を 3 本」に改め、§103.6 の Agent 行に
  `chat_wiring.go` の**差し替え**（`ReplySuggestEnabled` は `sessionx` のまま、
  新 `ChatReplySuggestEnabled` は `uiprefs.ChatReplySuggest`）を明記する。

### 【重大 4】`/ai-assist/resolution` の費用見積りが 1 桁小さい

- **何が**: §103.8-3 は「冷えていると CLI を最大 5 本叩く（`headlessAgentAvailable` の
  キャッシュは 1 分）」としてコストを見積もっている。
- **どこ**: `chat_providers.go:73-108`（`headlessAgentAvailable`）/ `:1427`（`recommendedUtilityModel`）/
  `chat.go:421`（`recommendedAssistantModel`）/
  `internal/agents/codex/models.go:35`（**15 秒**タイムアウト、`codex debug models`）/
  `internal/agents/opencode/models.go:52`（**10 秒**、デーモンが無ければ CLI）/
  `internal/agents/agy/models.go:54`（**15 秒**、`agy models`）
- **なぜ問題か**: 高いのはログイン判定ではなく**モデルカタログの列挙**である。
  §103.7 が画面に出すと決めた「いま使うのは X **/ Y**」の Y は、
  未設定機能では `recommendedOneShotModel` の結果＝ `codex.Models()` /
  `opencode.Models()` / `agy.Models()` に落ちる。カタログのキャッシュも 1 分で、
  タブを開いた時刻が悪いと **1 リクエストで数十秒**になり得る。
  （opencode の列挙が 10 秒フルに張り付く実例は既に踏んでいる。）
  「1 リクエストで 8 機能ぶんを返す」という緩和は**機能の数**を減らすだけで、
  **CLI の数**は減らさない——8 機能が 5 CLI にばらけていたら 5 本ぶん全部払う。
  そして §103.7 の「タイムアウトしたら行を出さない」は、
  *いちばん知りたいとき（設定を触っているとき）に何も出ない*という形で効く。
- **どう直すか**（安い正直な形）:
  1. エンドポイントを **「絶対に外部プロセスを起動しない」** 契約にする。
     `headlessAgentAvailable` / `*.Models()` は**キャッシュにヒットしたときだけ**答え、
     未取得は `source:"unknown"` で返す。画面は §103.7 の「未取得なら行を出さない」をそのまま使う。
  2. 返すのは **`{feature, enabled, kind, source}` まで**にする。kind の解決はログイン判定だけで
     済み、これがいちばん嘘になりやすい部分（97 で踏んだのは「モデル名は合っていたが kind が違った」側）。
  3. モデル名は Console が**既に持っている**経路で埋める——`useModelOptions(kind)`
     （`console/src/lib/agentModels.ts:169`）は `/agents/{kind}/models` を叩いてキャッシュ済みで、
     AI補助タブは §1 のモデル行のためにどのみちそれを引く。同じ一覧からラベルを引けば追加費用は 0。
  4. それでも Y を Agent に答えさせたいなら、**バックグラウンドで暖める**経路
     （既存の 1 分キャッシュをタブを開いた瞬間に非同期で更新し、次のポーリングで出す）にする。
     同期で最大 40 秒待たせる設計だけは避ける。

### 【重大 5】エージェント＝「自動」のとき、モデルをどこに保存するのかを決定 4 が答えていない

- **何が**: 決定 4 は「モデルは kind スコープで保存する（`機能 → CLI → モデル`）」。
  §103.5 のキーは `aiFeatureModels: Record<FeatureId, Record<string, string>>`。
  §103.7 のモックアップ 2 枚目は **エージェント＝「自動（優先順位）」＋モデル＝「既定（文章生成）」**。
- **どこ**: 103 §103.4 決定 1・4、§103.5、§103.7
- **なぜ問題か**: `aiFeatureModels[feature][kind]` を書くには **kind が要る**。
  「自動」のときの kind は実行時まで決まらない（`preferredFrom`、`chat_providers.go:126-133`）。
  だから「自動」のカードでモデル欄に選べるのは論理的に「既定」か「推奨」だけで、
  具体的なモデル ID は選べない。モックアップは暗黙にその規則に従っているが、
  **本文にその不変条件が 1 行も無い。** 書かずに実装へ渡すと、素直な実装は
  「自動のときは 5 CLI ぶんのモデル行を出す」に落ちる——決定 1 が
  「1 機能あたり並べ替え＋CLI 数ぶんのモデル行」として明示的に退けた形そのものである。
  同じ不在は UI の破綻可否（依頼の観点）にも直結する: この規則があれば
  平常時 1 行・展開して 3 行の 8 枚で収まり、無ければ 8 機能 × 5 CLI = 40 行に膨らむ。
- **どう直すか**: §103.4 か §103.5 に不変条件として 1 行書く——
  > **具体的なモデルを選べるのは、その機能にエージェントを明示ピンしたときだけ。
  > 「自動」では「既定」か「推奨」しか選べない**（落ちた先の kind に他 CLI の ID を
  > 渡さないという決定 4 の裏返しであり、同じ理由から導かれる）。

  あわせて、**ピンを別の CLI に変えたときに前の CLI のモデル指定をどうするか**も決める
  （残す＝戻したとき復活する／消す＝§103.8-5 の「空の入れ子を書かない」と揃う）。
  今の文面はどちらとも読める。

---

### 【中 6】§103.9「移行は無い」は成立しない——反例 2 つ

- **何が**: §103.9 の見出しは **「無い。」**。
- **なぜ問題か**:

  **(a) 翻訳キャッシュの鍵にモデルを足すと、設定を 1 つも触っていない利用者が課金される。**
  §103.8-2 は「鍵に解決済みモデルを足す方を推す（鍵が増えるだけで、既存の訳は次の押下で
  作り直される）」と書く。だが実測では、保存されている実体は
  `sessionTranslation{Hash, Lang, Text, CreatedAt}`（`session_translate.go:161-166`）で
  **model 欄が無い**。鍵に model を足すと**既存の訳は全件ミス**になる。そして:
  - 利用者向けの文言がそれを約束している——「訳した結果はそのセッションを消すまで保持され、
    **同じ本文なら二度目以降は無料です**」
    （`console/src/lib/i18n/locales/ja/aiassist.ts` の `aiassist.note_mirror_translate`）。
  - `mirrorAutoTranslate` を ON にしている利用者は、**押さずに**走る
    （`aiassist.note_mirror_auto_translate`——「翻訳の設定のうち、これだけは押さなくても消費します」）。
  - `trimTranslations`（`session_translate.go:218-230`）は件数とバイト数で古いものから捨てるので、
    新旧 2 系統の鍵が同居すると**有効な保持件数も実質半減**する。

    → アップグレードだけで挙動と支出が変わる利用者がいる。**移行が要る。**
    最小の形は「**model が空文字の鍵＝旧鍵として読み続ける**」（書くときだけ新鍵）。
    これなら既存の訳は生き、モデルを変えた人だけが作り直しになる。

  **(b) §103.8-6 が自分で反例を書いている。** 「返信候補のキー名を直すと、オフにしていた
  利用者の挙動が初めて変わる（ボタンが消えるだけだったのが、本当に生成が止まる）」。
  これはまさに「アップグレードで挙動が変わる利用者」である。
- **どう直すか**: §103.9 の見出しを「**移行は 2 件だけ**」に改め、
  表に (a) の鍵の後方互換と (b) を明示の行として入れる。
  「無い」と書いたまま §103.8 に例外を 2 つ置くのは、次に読む人が §103.9 だけ読む形。

### 【中 7】`assistant.ask` を境界の外に置く**理由**が事実と違う

- **何が**: §103.2 の境界——「`assistant.chat` / `assistant.autoturn` / `compact` /
  `assistant.ask` / `assistant.bridge` は**面がチャットなので**アシスタントタブ側で正しい」。
- **どこ**: `workspace/agent/internal/mcpx/mcp_stdio.go:2238`（ツール `ask_assistant`）/
  `mcp_stdio.go:3140`（ディスパッチ）/ `chatx/chat_handlers.go:217-255`
- **なぜ問題か**: `assistant.ask` の面は**チャットではない**。これは**セッションが MCP から呼ぶ
  ツール**で、会話は永続化されず（`chat_handlers.go:252-253`、`ref` は空）、チャット画面には出ない。
  84 §84.2 の「利用者が見る面で分ける」をそのまま当てると、この機能はアシスタントタブに
  属さない。結論（アシスタントタブに残す）自体は妥当だが、**根拠が違う**——
  正しい根拠は「`ask_assistant` はアシスタント定義が主語で、そのアシスタントの
  agent / model で走る（`ResolveChatModel(a.Agent, a.Model)`、`chat_handlers.go:246`）から、
  AI補助の優先順位もティアも**そもそも通らない**」。`assistant.bridge` も同様
  （`bridge_operator.go:76`——面は Discord/Slack）。
- **どう直すか**: §103.2 の境界の一文を、面による分類ではなく
  「**優先順位とティアを通る機能だけが本稿の対象**」という実効の線に書き換える。
  そうすれば `assistant.ask` / `assistant.bridge` も例外なく説明できる。
  （84 の分類原則は「設定の並べ方」の原則であって、「どのコードが対象か」の原則ではない。）

### 【中 8】「呼び出し元 7 箇所」は誤り。実数は 8、並べている行番号は `WithTag` の行

- **何が**: §103.5「`OneShotHeadless` の呼び出し元 **7 箇所**は…」に続けて、
  `session_title.go:122,590,789` ほか **9 個の行番号**を並べている。
- **どこ**: 103 §103.5（行 101-104）
- **なぜ問題か**: 実測（§0.1 の表）では
  - `OneShotHeadless` のテストでない**呼び出し**は **8 箇所**
    （`session_title.go:190`,`:725` / `session_suggest_reply.go:293` / `chat_plan.go:357` /
    `chat_suggest_reply.go:44` / `chat_title.go:75` / `fs_suggest_edit.go:215` /
    `session_translate.go:318`）。
  - 本文が挙げた 9 個は **`usagex.WithTag` の行**であって、`OneShotHeadless` の行ではない。
  - 数が 7 でも 8 でも 9 でもないので、次の人が「どれを数えたのか」を再現できない。
    設計の要は「**全部**が通っている」ことなので、ここは数え方が明示されていないと検証できない。
- **どう直すか**: 「タグを載せる箇所 9・`OneShotHeadless` の呼び出し 8」と両方書き、
  行番号の列がどちらのものかを明記する。**9 つ目の補助機能は無い**ことも 1 行で残す
  （台帳の feature 定数 `usagex/ledger.go:29-52` を全数当たって確認済み、と根拠つきで）。

### 【中 9】§1 の「推奨（現在: X）」は今も Console が自前計算していて、決定 5 と同じ画面で矛盾する

- **何が**: 決定 5「『いまこの機能が使うのは X / Y』は Agent が答える。画面側で計算しない」。
  §103.7 は §1（既定）を「現行のまま」とし、§2 にだけ Agent の答えを置く。
- **どこ**: `console/src/features/settings/parts/aiModelRow.tsx:22-37`（`recommendedModelId`）と
  `:56-57`。Agent 側の本物は `chat_providers.go:1427`（`recommendedUtilityModel`）と
  `chat.go:421`（`recommendedAssistantModel`）。
- **なぜ問題か**: `recommendedModelId` は Agent のロジックの**写し**である。
  定数は今のところ一致する（`gpt-5.6-luna` / `opencode-go/glm-5.2` /
  `opencode/nemotron-3-ultra-free` / `Gemini 3.5 Flash (Medium)` — `chat.go:390,395,404` と突合済み）。
  だが **hidden models の扱いだけ既にずれている**（コード読解による断定、実行はしていない）:
  - Agent は `visibleModel(kind, "haiku")` で、非表示なら **`""` を返して CLI 既定に落ちる**
    （`chat_providers.go:1427-1442`）。
  - Console は `aiModelRow.tsx:57` で `resolvedLabel = live.find(…) || recommended || 既定` と書く。
    `live` は非表示を除いたあとの一覧なので `find` は外れ、**`recommended`（= `"haiku"`）が
    そのままラベルになる**。
  - つまり **haiku を「使わないモデル」に入れた利用者の画面には「推奨（現在: haiku）」と出て、
    実際には claude の既定が走る。**
  §2 に Agent の答えを置くと、**同じタブの上に「画面の計算」と「Agent の答え」が並ぶ**。
  食い違ったとき、利用者にはどちらが本物か分からない——97 の事故（要求値を実行結果として
  出した）を 1 枚の画面の中で再現することになる。
- **どう直すか**: §103.6 に 1 行足す——**`/ai-assist/resolution`（またはその兄弟）が
  §1 の「推奨（現在: X）」のラベルも供給し、`recommendedModelId` を消す**。
  今回やらないなら、**§103.11「やらないこと」に明記**して、次の人が矛盾を再発見しないようにする。
  （どちらでもよいが、黙って両方置くのだけは避ける。）

### 【中 10】`guide/` が配線表に無い

- **何が**: §103.6 の配線表は Agent / CP / Console の 7 行で、**`guide/` の行が無い**。
- **どこ**: `guide/member/12-settings.ja.md` / `guide/ref/settings.ja.md` /
  `guide/ref/features.ja.md`（いずれも AI 補助に言及。`.ja.md` と `.md` の対で存在）
- **なぜ問題か**: `docs/CONVENTIONS.ja.md` §8「機能の完了条件」は
  「利用者から見える変更は、`guide/ref/features.md` に行が在り、影響する読者の棚に節が在る
  まで終わっていない」と書く。今回は**設定画面が 2 段になる**ので、利用者から見える変更である。
  さらに指摘 6(a) を採ると、`aiassist.note_mirror_translate` の
  「同じ本文なら二度目以降は無料です」という**約束の文言**も直す必要がある
  （i18n カタログと guide の両方、しかも ja / en 両方）。
  §103.8-6 の「リリースノートに 1 行」だけでは足りない。
- **どう直すか**: §103.6 に `guide/` の行を足し、§103.10 の検証計画に
  `python3 scripts/docs-check.py` を入れる。二言語同時更新（CONVENTIONS §5）も明記する。

---

### 【軽 11】旧 `replySuggest` を読み側フォールバックに残すのは死にコード

- §103.6 の Agent 行「（旧 `replySuggest` は読み側のフォールバックに残す）」。
- §103.3-3 自身が「**一度も一致したことがない**」を `git log -S` で示しており、
  §0.2 で再確認した——このキーの**書き手はリポジトリ全体で 0 件**。
  発火する経路が存在しないフォールバックであり、しかも値が無いときの既定はどちらも true なので
  挙動差も無い。残すと「昔このキーで保存していた時期がある」という**嘘の証言**になる。
- **どう直すか**: フォールバックは書かず、代わりに新しい関数の doc コメントに
  「`replySuggest` という綴りで読んでいた時期があるが、**それを書いた側は一度も存在しない**」を
  1 文だけ残す（AGENTS.md「規則を残す 1 行の根拠」の形）。

### 【軽 12】ピンの判定順が逆で、外部プロセスを余分に起動する

- §103.5 の擬似コードは `kind := PreferredAssistAgent()` を**先に**呼び、そのあとピンを見る。
- `PreferredAssistAgent()` → `preferredFrom`（`chat_providers.go:126-133`）は順に
  `headlessAgentAvailable` を舐め、**全部落ちていたら `order[0]` を返す**。
  ピンが生きているなら、この走査は最初から不要。
- **どう直すか**: ピンを先に見て、ピンが無い／未接続のときだけ `PreferredAssistAgent()` を呼ぶ。
  ついでに「**全 CLI 未接続のとき、ピンした CLI ではなく `order[0]` のエラーが返る**」という
  既存の挙動（`preferredFrom` のコメントが意図として書いている）が、
  機能別ピンの画面ではどう見えるかを §103.8 に 1 行足すとよい
  ——「codex にピンしたのに claude のエラーが出る」は素直に混乱する。

### 【軽 13】§103.8-1 の codex リトライ抑止は、ほぼ既にそうなっている

- §103.8-1 は「`CodexOneShotWithRetry` は**利用者の明示指定には効かせない**」を
  これから決めることとして書く。
- 実測では `chat_providers.go:1566-1567` が
  `args, autoPicked := codexOneShotArgsFor(selected)` / `autoPicked = autoPicked || autoRecommended`
  で、**明示指定（`configured && !autoRecommended`）なら `autoPicked=false`＝リトライしない**。
  つまり今日の実装が既にその約束を守っている。
- **どう直すか**: 「効かせない」ではなく「**既にそうなっているので壊すな**」と書き、
  機能別ピンが `configured=true` を立てることを条件として明記する
  （ここを `false` のまま通すと、リトライが復活するうえに `AF_TITLE_MODEL_CODEX` が勝ってしまう）。

### 【軽 14】`branchSuggestEnabled` / `assistantTitleSuggest` は `autoTitleSuggest` へフォールバックする

- `uiprefs/prefs.go:129-134`（`BranchSuggest`）と `:302-307`（`AssistantTitleSuggest`）は、
  キーが無ければ `AutoTitleSuggest()` を返す。Console 側も
  `settings.ts:933-936` で同じ継承を移行時に行う。
- §103.2 の表は「1 機能 1 キー」に見えるが、**84 より前の prefs を持つ利用者では
  1 キーが 3 機能に効いている**。§103.7 の「8 枚のカード・1 機能 1 行」の画面では、
  セッションのタイトル提案を OFF にした瞬間に他の 2 枚がどう見えるかが決まっていない
  （サーバの実効は「OFF」だが、Console の `DEFAULTS` は true なのでカードは ON に見える）。
- **どう直すか**: §103.7 に 1 行——カードの ON/OFF 表示も**フォールバック後の実効値**を出す、
  または移行時に 3 キーを実体化する。これも指摘 9 と同じ「表示と実効を一致させる」の系。

---

## 2. 裏取り済みの「問題ではなかった」点

指摘として挙げないが、確認したので記録する。

- **共有セッション（所有者以外）**: 心配は要らない。`control-plane/routes.go:407`
  （`suggest-replies`）と `:414`（`translate`）は**所有者側にしか登録されておらず**、
  `registerSessionShareRoutes`（`:419-455`）に双子が無い。`:410-413` のコメントが
  理由まで書いている（「押すと所有者の Workspace でモデルが走り、所有者のトークンを使うので、
  共有を読んでいる受け手が撃ててはいけない」）。§103.6 の
  「共有側には置かない」は既存の前例どおりで、追加の作業は無い。
- **MCP 経由の経路**: `plan.update` には無い。MCP が触れるのは
  `get_chat_plan` / `set_chat_plan`（`mcp_stdio.go:1892,1899`）＝**計画テキストの読み書き**だけで、
  LLM を回す `plan/refresh` に相当するツールは無い。`ask_assistant` は指摘 7 のとおり別の話。
- **使用量台帳との整合**: 機能別ピンを入れても台帳は壊れない。`kind` は
  「実際に何が走ったか」を分岐の中で埋める作り（`chat_providers.go:1542-1548` のコメントと
  各 `call.Kind = …`）なので、ピンが未接続で落ちた先が正しく記録される。
  §103.10 の「画面ではなく台帳で確かめる」はこの性質に正しく乗っている。
- **テストの seam（`chatx.Deps` の反射チェック）**: `Configure`（`deps.go:115-129`）は
  ゼロ値のフィールドがあれば panic し、`deps_test.go` は 1 フィールドずつ落として
  panic を確かめる。**足したフィールドは自動で網に入る**——§103.5 の主張は正しい。
  ただしこれが守るのは「配線したか」だけで、指摘 1 の「タグを載せたか」は守らない。
- **`ASSISTANT_AGENT_KINDS`**: `console/src/lib/settings.ts:726` は
  `["claude","codex","opencode","cursor","agy"]` で、§103.5 のエージェント選択肢と一致。
  未対応 kind（copilot/kiro/rovo/lcpp）をピンしても `headlessAgentAvailable` の
  `switch` に case が無く false を返す（`chat_providers.go:81-105`）ので、
  優先順位へ落ちるだけで壊れない——決定 7（lcpp を入れない）は安全側。

---

## 3. まとめ

- **事実の申告は 5 件中 5 件とも正しい。** 特に §103.3-3（返信候補のゲート）と
  §103.5（ctx の到達）は、疑ってかかっても崩れなかった。土台の調査は信用してよい。
- **一方で、設計の判断は 5 件が要再考**——そのうち 3 件（重大 1・2・5）は
  「この文書が自分で立てた原則」に照らして直すべきもの:
  - 重大 2 は §103.3-3 で見つけた**同じ失敗の形**を新しいトグルで繰り返している。
  - 重大 9 は 97 の事故（要求値と実行結果）を**1 枚の画面の中で**再現する。
  - 重大 5 は決定 1 が退けたはずの UI へ、不変条件を書き落とすことで戻ってしまう。
- **重大 1 だけは原則ではなく設計思想の話**で、採否は書き手の判断。
  ただし「呼び出し元を 1 行も触らない」という売り文句が実際に節約するのは **8 行**であり、
  その 8 行と引き換えに手放すのはコンパイラの強制である、という値札は本文に書いておくべきである。
