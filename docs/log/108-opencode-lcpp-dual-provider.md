# 108. opencode で「配備の借用エンジン」と「メンバー個人の LAN llama-server」を両建てできるか——調査のみ

- 状態: **調査のみ。実装は無し、ADR も起こしていない。LAN 実機には一切触っていない。**
- 依頼: `[agent-fleet:spawn from=sjqlfyj]`。opencode セッションで、配備の借用 llm provider
  (Control Plane 経由、ADR 0071)と、メンバー自身の LAN llama-server provider(直結)を
  両建てできるかをコードから検討し、根拠付きで記録する。
- 関連: [105](105-lcpp-console-toggle-and-lan-endpoint.md)・
  [106](106-lcpp-lan-run-helper.md)・[107](107-lcpp-member-endpoint.md)(**lcpp kind の
  ハーネスに足したメンバー個人の LAN 接続——本稿はこれと同型の問いを opencode に対して立てる。
  ただし対象コードは完全に別系統**)/
  [0071](../decisions/0071-self-hosted-inference-engines.ja.md)(opencode provider 化そのもの)/
  [0084](../decisions/0084-engine-indicator-and-tenant-gate.ja.md)(`allow_engine_llm`・
  `EnginesPill` の役ごと 1 枚)/[0090](../decisions/0090-member-facing-model-names.ja.md)
  (`decorateLabel`・モデル表示名)/[0093](../decisions/0093-lcpp-agent-kind.ja.md)(lcpp kind
  本体・`internal/harness`)/PR #858・#862(107 の実装 2 本——**opencode 側のファイルは 1 つも
  含まれていない**ことを確認済み、§0)
- 読んだ版: `2c6aa35bc`(2026-09-22 の develop 先頭)。opencode の実挙動は、この Workspace に
  焼き込まれている実バイナリ `opencode 1.18.31`(`/home/dev/.local/bin/opencode` →
  `../lib/node_modules/opencode-ai/bin/opencode.exe`)を直接調べて確認した(§3)。

## 0. 前提の確認——107 は opencode に触れていない

PR #858(`feat(lcpp): メンバー個人の LAN llama.cpp 接続先を設定モーダルに足す`)と PR #862
(`lcpp: メンバー接続の到達可否をカード/pillに出す`)の変更ファイル一覧を実際に見ると、
`workspace/agent/internal/agents/opencode/` 配下のファイルは**1つも無い**。107 が足した
`secrets.Data.Lcpp`・`harnessEngineToken` の `key=="llm"` 分岐・`lcppMemberFetchModelsCached`は
すべて `internal/harness`(lcpp kind 専用)の話で、`internal/harness` を import しているのは
`workspace/agent/internal/agents/lcpp/*.go` だけ(`agent.go`・`driver.go`・`mcp.go`・`store.go`・
`diff.go` とそれぞれの `_test.go`)——**opencode は import していない**。opencode の「配備の
借用 llm」は完全に別系統(ADR 0071、`workspace/agent/internal/agents/opencode/engine.go` +
`workspace/agent/engines.go` の `syncEngineProviders`)で、107 の仕組みが自動的に効くことは
ない。したがって本稿の問いは**未着手の別問題**であり、107 を読むだけでは答えが出ない。

## 1. provider 2 本は成立するか

**成立する。ただし provider 名(af が書く `"llamacpp"`)と衝突しない別名を選ぶことが必須条件。**

### 1.1 書き込みの仕組み——`provider` はキー名で衝突する連想配列

`opencode.WriteEngineProviders`(`workspace/agent/internal/agents/opencode/engine.go:99-175`)は
`opencode.jsonc`(または `.json`)の `"provider"` オブジェクトを直接編集する。af が書くのは
`providers[e.Provider] = engineProviderEntry(e)`(`engine.go:133`)——**キーは `EngineProvider.Provider`
という単なる文字列**で、配備の `llm` エンジン行は今日 `syncEngineProviders`
(`workspace/agent/engines.go:337-338`)から `e.Provider = "llamacpp"`(CP の `engineCatalogRow.Provider`
がそのまま渡る、`engine_catalog_test.go`・`engines.go:461` の `if e.Provider != "llamacpp"` が示す
とおり lcpp 系のエンジンは常にこの文字列)として書かれる。**もし 2 本目の provider も同じ
`"llamacpp"` という名前で書けば、連想配列のキーが衝突し、後勝ちで一方が消える。**

削除ロジック(`engine.go:138-146`)は `engineProviderMarker`(`"af-managed": true`、`engine.go:88`)
が付いた**af 自身が書いたエントリだけ**を対象にする——ファイル冒頭のコメントが明言するとおり
「a provider somebody added by hand must survive」(`engine.go:80-88`)。したがって:

- **2 本目を af 自身が書く場合**(§6 のコード実装案)は、`Provider` フィールドに
  `"llamacpp"` 以外の名前(例: `"llamacpp-member"`)を選べば、`WriteEngineProviders` は
  2 つの独立したキーとして両方保持する。マーカー付きなので削除対象判定にも正しく載る。
- **2 本目をメンバーが手で書く場合**(§6 段 0、実装無しで今日できる)も同じ理由で安全——
  af のマーカーが無いので削除ループに一切触れられず、`before`/`after` の差分比較
  (`engine.go:126-149`)にも「af が書いた分」としては数えられない(ただしファイル全体の
  バイト列が変わるので、af 自身の書き込みが「変更あり」と判定されて `serve` に再送されること
  はある——実害はない。§1.4)。

### 1.2 モデル ID・表示名は衝突しない——ID は provider で名前空間が切られている

opencode の model id は `<provider>/<model>`(`engine.go:2-16` のコメント、
`decorateLabel`(`console/src/lib/agentModels.ts:54`)が `opencode-go/`・`opencode/` という
provider プレフィクスで判定しているのも同じ規約)。**provider 名を分ければ、配備側とメンバー
側に同名のモデル(たとえば同じ量子化の `qwen3-coder-30b-a3b`)がいても id は
`llamacpp/qwen3-coder-30b-a3b` と `llamacpp-member/qwen3-coder-30b-a3b` で別物になる**——
リクエストの `model` 欄も、選択の保存も、この id をそのまま使うので衝突しない。

表示名(`name` 欄)は別の話: af が書く方は必ず `<id> (self-hosted)`
(`engineProviderEntry`、`engine.go:273`)で終わる。メンバーが手で書く 2 本目にも同じ文字列を
使うと、画面上は 2 つの選択肢が見分けにくくなる(id は違うが、ラベルは並んで同じに見える)。
**衝突ではなくヒューリスティックな UX 問題**——手で書く場合は `(LAN)` のような別のサフィックス
を勧める。af 自身が 2 本目を書く実装(§6)にするなら、`engineProviderEntry` のサフィックスを
呼び出し側から渡せるようにする一行の変更で済む。

### 1.3 保存/復元——「前回選んだモデル」を横取りする機構は見つからなかった(消極的事実)

`console/src/lib` 配下を `lastModel`・`last_model`・`selectedModel` で検索したが該当ゼロ
(`rg -l 'lastModel|last_model|selectedModel' console/src/lib console/src/features` は 0 件、
§8 に実行コマンドを記録)。起動モーダルは毎回 `defaultOnly()`
(`console/src/lib/agentModels.ts:33`)を先頭に積むだけで、次回起動時に前回の id を自動選択する
永続化層は無い。**したがって「復元」が 2 本の provider 間で取り違える経路は無い**——id が
provider で分かれている(§1.2)ことと合わせ、選択の保存/復元は本件の懸念事項ではない。

### 1.4 PushEngineProviders(ライブ反映)も同じ理由で安全

`PushEngineProviders`(`engine.go:226-256`)は af 自身が知っている `providers` マップだけを
`PATCH /global/config` に載せる(`engine.go:230-236`)。この PATCH は **MERGE**
(`engine.go:223-225` のコメント)なので、メンバーが手で足した 2 本目のキーは一切触れられず
そのまま残る。af 側の 2 本目(§6 実装案)を足す場合も、単に `providers` スライスに
`Provider: "llamacpp-member"` のエントリを増やすだけで、同じ PATCH 1 回に相乗りできる。

## 2. LAN API キーを平文にせず env で渡す——既存の作法と、この件特有の衝突面

### 2.1 既存の仕組みは「任意の env 名 → 値」を秘密ストアに置く、既にある機構

opencode の provider 認証は最初から「Console でキーを1つ貼ると、暗号化ストアに置き、
起動時に env として注入する」設計(`workspace/agent/internal/agents/opencode/auth.go:14-19`
のコメント)。ストアは `secrets.Data.Opencode map[string]string`
(`workspace/agent/internal/secrets/secrets.go:365`、コメント「provider env var name -> API key」)
——**キー名(env 変数名)も値も自由**、`envNameRe`(`auth.go:23`、`^[A-Z][A-Z0-9_]{1,63}$`)が
書式だけを縛る。Console 側は既に**任意の env 名を入力させる「custom」プリセット**を持っている
(`console/src/features/settings/agents/OpencodeCard.tsx:33` の `["custom", ...]` エントリ、
入力欄は同ファイル 496-499 行付近)。**つまり「メンバーが自分の LAN キーを env 経由で登録する」
UI と保存先は、opencode に関する限りもう存在している**——107 が lcpp kind 用に新設した
`secrets.Data.LcppConn`(`secrets.go:148`)のような専用構造体を、opencode 用に新しく作らなくても
同じ結果になる。

保存先は 107 が確認したのと同じファイル(`~/.config/agent-fleet/secrets.enc`)で、
denylist にも既に入っている(`workspace/agent/fs.go:126`、107 の確認をそのまま踏襲——本稿では
独立に再確認していない)。

### 2.2 注入経路は 2 つ、優先順位の扱いが違う

| 経路 | 関数 | 中身 |
|---|---|---|
| managed(共有 `opencode serve` デーモン) | `env()`(`auth.go:45-81`) | `secrets.Opencode` の全キーを sorted で並べたあと、末尾に `EngineEnv("")...` を **append**(`auth.go:80`) |
| tmux(セッション自身のプロセス) | `BuildLaunch`(`opencode.go:154-172`) | `mergeCommandEnv(env(), EngineEnv(m.Name))`(`opencode.go:172`)——**名前が同じなら override 側(`EngineEnv`)が勝つ**(`mergeCommandEnv`、`models.go:283-304`、コメントに「二重定義を運に任せない」と明記) |

### 2.3 🔴 見つけた衝突面——予約名 `AF_ENGINE_TOKEN` を自分の env 名に選ぶと、tmux 経路では確実に、managed 経路では未検証の形で潰れる

`opencode.EngineProviderKeyEnv` は `"AF_ENGINE_TOKEN"` という**固定の1文字列**
(`engine.go:44`)——今日の唯一の provider(`"llamacpp"`)の `apiKey` は必ず
`{env:AF_ENGINE_TOKEN}` を指す(`engineProviderEntry`、`engine.go:308`)。もしメンバーが
自分の LAN キー用の env 名として**たまたま同じ文字列 `AF_ENGINE_TOKEN` を選んでしまうと**:

- **tmux 経路(確認済み)**: `mergeCommandEnv` の override 側が `EngineEnv(m.Name)`
  (`opencode.go:172`)なので、**af が発行した配備側の一時トークンで、メンバー自身の値が
  黙って上書きされる**。メンバーが自分の LAN provider ブロックに `{env:AF_ENGINE_TOKEN}` を
  書いていたら、実際には CP のベアラーが LAN サーバーに送られる(認証は 401 で落ちるはずだが、
  「なぜ効かないか」が全く自明でない)。
- **managed 経路(未検証)**: `env()` は単純な `append` で、同名エントリの重複除去をしていない
  (`auth.go:45-81` を通しで読んだが dedup は無い)。`[]string` の重複した `KEY=VALUE` を
  実際のプロセス環境にした時にどちらが勝つかは、Go の `exec.Cmd.Env`/`opencode serve` を
  起動する実装依存で、**本調査では実機確認していない**(未検証、と明記する)。

**対策は実装ではなく命名規約**: メンバー(または将来 af 自身が書く 2 本目、§6)は
`AF_ENGINE_TOKEN` 以外の env 名を使う、の一言に尽きる。コードを変えるなら
`engineProviderEntry` の apiKey env 名を呼び出し側から渡せるようにし(§6)、
af 自身が管理する 2 本目には最初から別の予約名(例 `AF_LCPP_MEMBER_TOKEN`)を割り当てて
この地雷を踏めなくするのが筋。

## 3. 使用量——opencode 自身の自己申告を Agent Fleet がそのまま信じる。直結で 0 になる根拠は無い

### 3.1 Agent Fleet 側の集計は opencode の自己申告を無条件に信用する設計

`usageMeasuredForKind("opencode")`(`workspace/agent/usage_fold.go:206-207`)は
`usagex.MeasuredExact`——lcpp kind(`usage_fold.go:215-216`、CP gateway が
`stream_options.include_usage` を注入して初めて exact になる、という条件付きの exact)とは
違い、opencode は最初から「exact」の宣言で、Agent Fleet 側に独自の検算ロジックは無い。

実際の集計元は HTTP レスポンスではなく、**opencode 自身の sqlite ストアに書かれた
メッセージ行**: `parseMessage`(`workspace/agent/internal/agents/opencode/transcript.go:571-648`)
が `data.tokens.{input,output,cache.read,cache.write}`(構造体定義 `transcript.go:577-584`)を
そのまま `t.InTok`/`t.OutTok`/`t.CacheRead`/`t.CacheCreate`(`transcript.go:638-639`)に写す。
Agent Fleet は opencode が「使った」と書いた数字を疑わずに使う——**opencode が自分の
リクエストに usage を積まなければ、opencode 自身が 0 を書き、Agent Fleet もそのまま 0 を
記録する**、という意味で「Agent Fleet 側は空欄を作らない」。

### 3.2 🔴 実機で確認した事実(推測ではない): opencode の openai-compatible クライアントは
`stream_options.include_usage` を常に送る

この Workspace に入っている実バイナリを直接調べた:

```
$ file /home/dev/.local/bin/opencode
… -> ../lib/node_modules/opencode-ai/bin/opencode.exe
$ /home/dev/.local/bin/opencode --version
1.18.31
$ strings -a /home/dev/.local/bin/opencode | grep -o 'stream_options:{include_usage:!0}'
stream_options:{include_usage:!0}
```

該当箇所はミニファイされたバンドル内の `OpenAIChat.fromRequest`(バンドル内のローカル名は
`Q7`)で、チャットリクエストの body を組み立てる関数がリテラルで次を含む:

```
stream:!0, stream_options:{include_usage:!0}, max_tokens: …
```

これは **provider の baseURL が CP 経由(`https://<cp>/engine/llm/v1`)かメンバーの LAN
(`http://<lan>:8080/v1`)かに関係なく、opencode 自身の `@ai-sdk/openai-compatible` 実装が
無条件に付ける**。同じバンドル内の `X7` という関数がレスポンスの
`x.prompt_tokens`/`x.completion_tokens`/`x.total_tokens`(および
`prompt_tokens_details.cached_tokens`)を読んで `ox`(内部の usage オブジェクト)に変換して
いるのも確認できる——**この経路は 106 が発見した `internal/harness` の `client.go` の欠陥
(`stream_options.include_usage` を送っていなかった、CP の `askForStreamUsage` が代わりに
注入していた——[106](106-lcpp-lan-run-helper.md) §7.2.3・§10)とは完全に別物で、opencode
は最初からこの欠陥を持っていない。CP を経由しない直結でも usage が来ないと考える理由は無い**。

### 3.3 未検証で残ること(誠実に区別する)

- **llama-server が実際にこの `include_usage:true` を honor し、最終チャンクに `usage`
  を乗せて返すこと自体は 106 §10 で実測済み**(build `b11067`、`PromptTokens=44
  CompletionTokens=108`)——別のサーバー・別のビルドでも同じかは未確認。
- **opencode の `X7`(usage パーサ)が実際に自分のメッセージ行の `tokens.input/output` に
  正しく反映することは、このバンドルの静的な読みからの推論であり、実際に LAN の
  llama-server 相手にターンを 1 回通して確認してはいない**——LAN 実機には触れない、という
  本依頼の制約どおり。
- CP 側の使用量集計(`control-plane/engine_usage.go`、ADR 0084 決定 9 のテナント showback)は
  `/engine/<key>/v1/...` を通る**借用側のトラフィックだけ**を見ている。メンバーの直結は
  CP を一切通らないので、**CP 側の課金/showback はメンバーの LAN 消費を最初から観測しない**
  ——決定 4 で述べる `allow_engine_llm` の抜け道と同じ形の「CP から見えない」で、実害は
  「メンバー自身のハードウェアを自分で使う分には元々 CP が知る必要のない消費」という整理で
  106 の付録 11 の結論(要望2は近道であって新しい機構ではない)と同じ位置づけになる。

## 4. ADR 0084 `allow_engine_llm` は直結に効かない——確認した具体的な理由

`allow_engine_llm` は `tenantLimits.engineRoleAllowed`(`control-plane/limits.go:122-133`)
一箇所に集約されており、呼び出し元は 2 系統だけ:

1. `control-plane/engine_gateway.go` の `catalog`(276-306 行、295 行目でフィルタ)——
   `engineCatalogRows`(workspace/agent側)がここを呼んで `syncEngineProviders` の元ネタにする。
   **拒否されたテナントには `llm` 行自体が返らない**ので、af は `"llamacpp"` provider を
   そもそも書かない。
2. 同ファイルのトークン発行/チャット中継本体(258・504・642 行目)——実際の
   `/engine/llm/v1/chat/completions` 中継。

**メンバーが自分の LAN サーバーへ直結する 2 本目の provider は、上記のどちらも一切呼ばない**
——`opencode.jsonc` の provider ブロックが直接 `baseURL: "http://<lan>:8080/v1"` を指すだけで、
CP への往復が構造的に存在しない。したがって `allow_engine_llm` を `false` にしても、
2 本目には**何の効果も無い**——107 が lcpp kind の直結について書いたのと全く同じ結論
(`workspace/agent/engines.go:976-981` のコメント、および107本文の「本稿はこれを迂回する」)
が、コードを変えず opencode に当てはめても成り立つ。

🔴 **これは利用者/製品側の判断が要る点として明示する**: 107 で lcpp kind についてはこの
迂回を受け入れる決定が既に一度下っている(precedent)。opencode についても同じ整理を
踏襲してよいかは、この調査では決めない——「メンバー自身のハードウェアなので、テナント管理者の
制御が及ばなくて構わない」という立場を、lcpp kind だけでなく opencode にも広げてよいかの
確認を、実装に進む前に取ってほしい。

## 5. トップバー pill・接続状態の提案

### 5.1 既存の `EnginesPill` は「役ごとに1枚」(ADR 0084 決定10/11)——kind を見ていない

`EnginesPill`(`console/src/features/engines/EnginesPill.tsx:123-131`)は `chat` ロールが
1枚、`image` ロールが1枚という設計で、**どのセッション種別が今開いているかとは無関係**——
`conns?.lcpp?.connected` が真なら、`chat` ロールの pill は無条件に `MemberChatPill`
(139-152行目)に総取り替えされる。107 の追補(PR #862)がこれを足した時点では、
「lcpp kind のメンバー接続」以外にこのロールを使う経路が無かったので問題にならなかった。

### 5.2 🔴 opencode に 2 本目を足すと、この設計はそのままでは食い違いを起こす

`conns.lcpp` は**lcpp kind のハーネス専用**の接続情報(`secrets.Data.Lcpp`)で、opencode の
provider ブロック(§1)とは何の関係も無い。したがって:

- メンバーが「lcpp kind 用の LAN 接続」だけを設定していて、今 opencode セッションを開いている
  (opencode は配備の借用 `"llamacpp"` provider を使っている)場合、**トップバーの `chat` pill
  は関係の無い lcpp 接続の到達性を表示し続ける**——今見ている opencode セッションが実際に
  話している相手(CP 経由の借用エンジン)とは別の状態を見せる。これは本調査で見つけた
  **既存の粒度の粗さ**であり、107 のバグではない(107 は lcpp kind だけを前提に設計された)。
- opencode 用の 2 本目(§6)を新設した場合、同じ `MemberChatPill`/`conns.lcpp` の形に
  無理に乗せると、ADR 0084 決定11 が名指す失敗(「単位を型に取ると、2つ目が表現できない」
  ——`EnginesPill.tsx:170-172` のコメントが既にこの言葉でlcppとexternal/remoteの混同を
  戒めている)を今度は「lcpp kind 用の接続」と「opencode kind 用の接続」の間で再現する。

### 5.3 提案(実装はしない、方向性のみ)

- `MemberChatConn` を **kind ごとに複数持てる形**に一般化するか(`console/src/types/session.ts`
  の `connections` 応答に `opencode_lcpp` のような別キーを足す)、
- または pill 自体を「今アクティブなペインの kind」で条件分岐させる(今日の設計方針——
  役ごとに1枚——からは外れるので、決定11 の教訓に照らすと本命はこちらではなく前者)。
- 最小の非コード対応(§6 段 0)を選ぶ場合、pill・Settings の opencode カードのどちらにも
  **何も表示されない**——`opencode.jsonc` の手書き provider は Agent Fleet の Go/Console
  層から見えない(誰も poll していない)ので、メンバーが得られる唯一の状態表示は opencode
  自身の起動メニュー(`opencode models` の結果)とターン失敗時のエラーだけになる。この
  「何も知らせない」という制約は、段0を選ぶ場合の既知のトレードオフとして明記しておく。

## 6. 見積りと段階化

| 段 | 内容 | 費用 | 次への門 | ロールバック |
|---|---|---|---|---|
| 0 | **コード変更ゼロ。** メンバーが `opencode.jsonc` の `provider` に `"llamacpp-lan"`(仮名)を手で追加し、OpencodeCard の「custom」プリセット(`OpencodeCard.tsx:33,496-499`)で任意の env 名(`AF_ENGINE_TOKEN` 以外)に自分の LAN キーを保存する | **0日**(今日できる) | メンバーが実際に両方の provider を launch メニューで選べる(実機1回) | ファイルを戻すだけ |
| 1 | `engineProviderEntry`/`engineConfigKey` の apiKey env 名を呼び出し側から渡せるように一般化(`engine.go:44,308`) | 0.5〜1日 | 既存の `WriteEngineProviders` 試験(`engine_test.go`)が無改修のまま緑 | Go のみ、revert 容易 |
| 2 | `secrets.Data.Lcpp` を再利用し、af 自身が2本目の `opencode.EngineProvider`(`Provider: "llamacpp-member"`)を書く経路を足す。モデル一覧は既存の `lcppMemberFetchModelsCached`(`engines.go:1137-1146`)を再利用——新しいポーリングは足さない | 1日 | 段0で確認した実機と同じ launch メニューが、コード生成で再現される | 経路を1つ削るだけ、段0はそのまま生きる |
| 3 | Console 可視化——§5.3 のどちらかの形で pill/カードに反映 | 1〜1.5日 | メンバーが押さなくても両方の状態が分かる | i18n・dom テストのみ |
| 4 | 実機受け入れ(利用者のLAN機で1往復・usage が非ゼロで記録されることを確認・tool call 生存確認) | 半日+運用者の実機 | §3.3 の未検証点がすべて実測で決着する | — |

**ADR は今は起こさないことを提案する(最終判断は利用者)**——新しい概念は増えておらず、
107 が一度下した「メンバー自身の直結は `allow_engine_llm` を迂回してよい」という決定を
opencode に広げるかどうかという**1行の確認**(§4末尾)さえ取れれば、106 が lcpp 側について
出した結論(ADR 不要、docs/log に1行残せば足りる)と同じ位置づけになる。

## 7. 実機測定テンプレート(空欄——本セッションでは実測していない)

依頼にあった、lcpp MCP のツールコール系実機検証で埋めるための雛形。**数値は一切ここでは
埋めていない**——実機を持つセッション/利用者が測った時にこの表を埋める前提。

| 日付 | af tool 名 | 承認モード(承認停止 / AutoApprove) | `input_tokens` の有無(harness の decision 7 経路) | spawn+handshake 秒 | Gemma tool call(成功/失敗・内容) | web MCP(成功/失敗・内容) | 備考 |
|---|---|---|---|---|---|---|---|
| | | | | | | | |
| | | | | | | | |
| | | | | | | | |

## 8. 検証

```
python3 scripts/docs-check.py   # 443 files, 0 error(s), 0 warning(s)（本稿を1本追加した後）
rg -l 'lastModel|last_model|selectedModel' console/src/lib console/src/features   # 0 件（§1.3の消極的事実）
rg -n 'EngineProvider{' workspace/agent/engines.go workspace/agent/internal/agents/opencode/*.go
rg -rln '"github.com/k-k1/agent-fleet/workspace/agent/internal/harness"' workspace/agent/internal/agents/*/*.go   # lcpp のみ 6 ファイル
gofmt -l .   # 空（本稿はコード変更なし）
```

コードは1行も変更していないため `go test ./...` は対象外(触っていないパッケージを回しても
本稿の主張の裏取りにはならない)。
