# 105. llama.cpp をエージェント設定に出す/切る、LAN の llama.cpp へ切り替える——検討のみ

- 状態: **検討（設計文書）。実装はしていない。** 親セッション `so2ydq6` からの依頼を受け、別セッション
  (`sfbkvkc` 系列の続き・本セッション) が調査した。コードは 1 行も変えていない。
- 依頼は 2 つ: (1) 設定モーダルのエージェントタブに llama.cpp (`lcpp`) のカードを出し、オン/オフを
  切り替えられるようにしたい。(2) ローカルネットワークの llama.cpp エンドポイントへ利用者が切り替え
  られるようにしたい。
- 前提: kind `lcpp`（ADR 0093）は 2026-09-21 に採用・develop にマージ済み。決定 10 により
  「ピン・導入・ログイン・ドリフト監視は無い」——既存カードの大半の内容が最初から当てはまらない。
- 関連: [0093](../decisions/0093-lcpp-agent-kind.ja.md)（lcpp 本体）/
  [0076](../decisions/0076-external-image-engine-on-lan.ja.md)（LAN の外部エンジン——**この文書の
  問い 2 の直接の前例**。P3 に `AF_LLM_URL` を名指しで予告している）/
  [0079](../decisions/0079-remote-engine-from-another-deployment.ja.md)（借用エンジン。いまの `llm`
  行はこれで来ている）/
  [0084](../decisions/0084-engine-indicator-and-tenant-gate.ja.md)（テナント別の `allow_engine_llm`
  ——採用済みの「役ごとの on/off」の唯一の前例）

---

## 105.1 問い1: 設定モーダルに llama.cpp のカードを出す・オン/オフ

### 105.1.1 既存カードが何を表示・操作しているか(棚卸し)

`console/src/features/settings/agents/AgentsTab.tsx:30-41` がこのタブの設計方針をコメントに書いている:
各カードは 2 層——**上: 接続(認証フロー + 状態)**、**下: 折りたたみの「動作設定」(クライアント側の
起動既定値 + コンテナ側のトグル)**。7 枚のカード(`AgentsTab.tsx:238-258`: Claude/Codex/Cursor/
Copilot/Kiro/Agy/Opencode)は全部この形で、`conns?.<kind>` (`useConnections`、ワークスペース起動中
のみ取得)を土台にしている。

`KiroCard.tsx` を具体例として読むと、カードが持つ要素は:

1. **接続状態**(`st?.connected`・サインイン/サインアウト・`StatusPill`) — `KiroCard.tsx:133-190`
2. **導入(install)**——855MB のオンデマンド導入ボタンとポーリング — `:48-93,152-171`
3. **バージョンピンとの差分・更新ボタン** — `:191-215`(`inst.updateAvailable`)
4. **`CardSettings`(折りたたみ)の中の `LaunchDefaults`** — `:216-218`。中身は既定モデル・
   既定 effort・**非表示モデル一覧**(`HiddenModelsRow`)・既定起動モード・承認スキップの on/off
   (`AgentCardParts.tsx:76-140`)。

ADR 0093 決定 10 はこう書いている(`docs/decisions/0093-lcpp-agent-kind.ja.md:213-219`):
「ピン・導入・ログイン・ドリフト監視は無い。接続カードは『chat エンジンが目録にある・warm か・
既定モデル』だけ。」——つまり上の 4 要素のうち **1(サインイン)と 2(導入)と 3(バージョンピン)は
lcpp に最初から当てはまらない**。実コードでも `console/src/agents/registry.ts:558-561` が
明記している:「No sign-in exists for this kind (決定 10: no login route, no connection card
auth), so … availability never reads `conns`」。

**残るのは `LaunchDefaults` 相当だけ**。ただしそれも無改修では使えない:
`AgentCardParts.tsx:76` の `LaunchDefaults` の `kind` 引数の型は
`"claude" | "codex" | "cursor" | "kiro" | "agy" | "opencode" | "copilot"` の union で、`lcpp` は
含まれていない。含めれば動く見込みは高い——`hiddenModels` は `Record<string, string[]>`
(`console/src/lib/settings.ts:323`)で kind を制限しておらず、`HiddenModelsRow` も `kind: string`
(`AgentCardParts.tsx:199`)で受けるので、型を足すだけで「既定モデル」「非表示モデル」の 2 行は
機構としては動く。ただし何を出すかは要検討——lcpp のモデル一覧はサインイン後のアカウント連動
カタログではなく、**この配備が借りているエンジンの目録**(`workspace/agent/agent_models.go:73-94`
の `lcppModels`)であり、空の配備ではモデル選択そのものが意味を持たない。

**結論(問1前半): 「サインイン」「導入」「バージョン」を持つ既存カードの型をそのまま複製すると
空欄だらけのカードになる。** 出す価値があるのは「既定モデル」「非表示モデル」の 2 行だけで、
それは他のカードの 1/3 程度の情報量である。**「空のカードを出すくらいなら出さない」という判断も
成り立つ**——依頼文自身がそう示唆している通り。カードを出すなら「接続」の見出しを持たない、
`LaunchDefaults` だけの薄いカードにする、という設計が筋が通る(後述 105.3 で段階として提案)。

### 105.1.2 「オン/オフ」に相当する既存の仕組み——4つを比較

依頼は既存の概念のどれで表現できるかをまず確かめよと明記しているので、実コードで 4 つを比較する。

| 仕組み | 実装場所 | 何を決めるか | 粒度 | 誰が変えるか |
|---|---|---|---|---|
| `registry.ts` の `available()` | `console/src/agents/registry.ts:561`(lcpp は `() => true` 固定) | **起動導線にその kind を出すかどうかの計算結果**。ユーザーの選択ではなく、接続状態などから毎回導出される述語 | kind 単位 | 誰も変えない(計算値) |
| `repoLaunchKinds` | `registry.ts:644` | どの kind をリポジトリ行の起動メニューに**候補として載せるか**の静的順序リスト。`lcpp` は既に入っている | グローバル(全利用者共通の並び) | コード変更でしか変わらない |
| `ui-prefs`(`opencodeCatalog` など) | Agent 側 `internal/uiprefs/prefs.go`。CP は`GET/PUT /api/env/ui-prefs` を素通し中継するだけ(`control-plane/routes.go:787`、ADR 0084 決定 11 の記述) | ワークスペースの home ボリュームに置く不透明な設定。**opencode の「オフ/フリー/自前/opencode.ai」という課金経路選択**(`workspace/agent/internal/agents/opencode/auth.go:29-33,107-121`)がこの上に乗っている | **ワークスペース単位**(利用者ごとではなく、ワークスペースが 1 つなら実質利用者ごと) | 利用者(Console から PUT) |
| `hiddenModels` | `console/src/lib/settings.ts:323` | kind ごとの**モデル**除外リスト。kind そのものの on/off ではない | 利用者のブラウザ設定(client-side) | 利用者 |

**opencode の `UsageOff` が最も近い前例だが、意味が違う。** `auth.go:107-121` の `connected()` は
`UsageOff` のとき**サインイン済みでも接続済みと絶対に読まない**——「セキュリティポリシーが
頼れる固いロック」だとコメントが明言している(`:103-106`)。ただしこれは「**課金経路**の選択」の
1 つであって、lcpp のように課金経路(サインイン)そのものが無い kind には移植できない。「切る」に
対応する状態が opencode には 3 つ(off/free/own/opencode.ai の 4 択のうちの 1 つ)あるのに対し、
lcpp は「エンジンが繋がっているか否か」の 1 軸しかない。

**`available()`/`repoLaunchKinds` は「利用者の意思」を表現する仕組みではない。** 両方とも
起動メニューに**何を候補として見せるか**を決めるだけで、見せないことと使えないことは同じではない
(ADR 0093 の未登録 kind が claude に正規化される仕組みと同じ非対称——「隠す」は「拒む」ではない)。

**唯一、実際に「役ごとの on/off」を実装しているのは ADR 0084 決定 7 の `allow_engine_llm` /
`allow_engine_image` である**(`control-plane/limits.go:107-128`、`internal/tenantsrv/tenants.go:159-166`
に実装済み、ADR 0084 の状態欄は「起草」のままだが**コードは develop に入っている**——ADR の
ステータス表示が古い一例)。ただしこれは:

- **テナント管理者(super_admin)専用**(`limits.go` の comment・決定 7 「書けるのは super_admin
  のみ」)であり、利用者本人が切る設定ではない。
- **粒度が kind ではなく「エンジンの役」**(`llm` / `image`)。`lcpp` と opencode の「自前エンジンを
  使う」経路(`opencode/engine.go:177-188` の `HasEngineProviders`)は**同じ `llm` 役を共有**している
  ので、`allow_engine_llm=false` にすると **lcpp だけでなく opencode 経由の自前エンジン利用も
  一緒に消える**。「llama.cpp カードだけのオン/オフ」を求めるなら、この既存機構では粒度が粗すぎる。

**結論(問1後半)**: 依頼の「オン/オフ」を**利用者が切る個人設定**として素直に実装しようとすると、
既存のどの仕組みにもぴったり収まらない。一番近い形は ui-prefs 相当の**新しいワークスペース単位の
フラグ**(opencode の `UsageOff` と同じ設計——kind の available() をこのフラグで上書きする)だが、
それは「新しい概念を作らない」という要求とは緊張する。**逆に、テナント管理者向けの on/off が
欲しいなら `allow_engine_llm` は既に在るので、そこに乗るのが最短だが、opencode と道連れになる
ことを利用者に説明する必要がある。** どちらを狙っているかで答えが変わるので、これは提案ではなく
利用者に確認すべき分岐点として書く。

### 105.1.3 「オフ」で何が起きるべきか——半端さの具体的な出どころ

依頼が名指しした懸念(「起動画面には無いのに API では作れる」)を実コードで確認した。`lcpp` の
作成経路は 2 つに分かれていて、**それぞれ別のゲートを通る**:

- **Console の起動導線**: `repoLaunchKinds`(`registry.ts:644`)→ `availableKinds()`
  (`registry.ts:635-639`、`available()` を呼ぶ)。ここだけを塞いでも、以下の経路には無関係。
- **`af` の MCP 経由の `create_session`/`list_models`**: `workspace/agent/internal/mcpx/mcp_stdio.go`
  に**ハードコードされた kind 文字列の許可リスト**が 2 箇所ある——`list_models` の
  `:2611`(`a.Kind != "claude" && … && a.Kind != "lcpp"`)と、`create_session` 自体には kind の
  許可リストが無く(`:2772-2830`)、`session_handlers.go` 側で受理される。ADR 0093 の実装記録
  (`docs/decisions/0093-lcpp-agent-kind.ja.md:631-632`)が明記する通り、この許可リストは PR #829
  で**足された**もので、無ければ `create_session`/`list_models` が `lcpp` に使えなかった。

つまり今日の「on」は **(a) `registry.ts` の `available()`/`repoLaunchKinds`** と
**(b) `mcp_stdio.go` の文字列許可リスト**の**2 つが独立に揃って**成立している。「オフ」を
実装する場合、この 2 つを**同じ設定源から**揃えないと、依頼が懸念した状態——Console には出ない
のに `af` の `create_session(kind="lcpp")` は通る——がそのまま再現する。さらに 3 つ目の穴:
**未登録 kind は拒否ではなく `claude` へ黙って正規化される**(ADR 0093 背景、
`workspace/agent/internal/sessionx/agent.go:49` 相当)ので、サーバ側(`workspace/agent`)に
「lcpp は今オフ」を伝える経路が無いと、`mcp_stdio.go` 側だけ塞いでも直 REST
(`POST /sessions` with `kind:"lcpp"`)を叩けば通る可能性がある——確認できていない
(**分からなかった**: `session_handlers.go` に kind 単位の許可/拒否ロジックがあるかどうかは、
今回 `KindSSM` の特別扱い(`:913,921`)以外に見つけられなかった。lcpp 固有の拒否は無いように見えるが、
悉皆確認はしていない)。

**既存セッションへの影響**: オフにしても、依頼文にある通り「起動できなくする」であって「エンジンを
止める」ことではないはずである(ADR 0076 決定 5 の `off` の意味——「経路を閉じることであって、
エンジンを止めることではない」——と同型)。動いている `lcpp` セッションを打ち切る機構は今回の
コードには見当たらず、そうすべきだという要求も依頼文に無い。**オフ = 新規作成を拒む。既存は
影響を受けない**、という解釈が最も既存の型(ADR 0076/0079/0084 の `off`)と整合する。

---

## 105.2 問い2: LAN の llama.cpp エンドポイントへの切替

### 105.2.1 ADR 0076 の論点と、今回への引き写し

ADR 0076(LAN の ComfyUI・提案・レビュー済み・未実装)は、**この設計課題と同型の先例**であり、
しかも **P3 として `AF_LLM_URL` を名指しで予告している**(`docs/decisions/0076-external-image-engine-on-lan.ja.md:249-250`):
「同じ機構で `AF_LLM_URL`(LAN の llama-server / Ollama、chat 役)。`warmPath` の `/models` は
llama.cpp router 専用なので空にする。この ADR の範囲外。」——つまり**今回の要望はこの ADR が
既に見込んでいた続き**であって、白紙から考える話ではない。

ADR 0076 の論点はそのまま効く:

- **到達性は防御ではない**(決定 7)。LAN では CP/Workspace が LAN に直接出られる網では、
  ComfyUI/llama-server 自体に認証が無い前提だと、到達できることがそのまま権限になる。
- **却下案「Workspace が直接 LAN を叩く」**(却下した案の節)——ゲートウェイを経由しない案は、
  ADR 0071 決定 4(a)(c)(d)(経路が増えない・使用量を CP が数える・変更系 API を全セッションに
  晒さない)を理由に既に却下済み。今回もこの理由はそのまま成り立つ。

**同じ結論になるか、画像と LLM で違うか**: 機構(`lifecycle: external` の宣言・env 変数 1 本・
ゲートウェイ経由・即時失敗)は同じでよい。ただし 2 点、LLM 特有の違いを見つけた:

1. **チャットの経路には「即時失敗」の再試行版が既に無い。** ADR 0076 決定 4 の「external は
   `ensureStarted` を即時に失敗する」は image provider の再試行(16 分)を前提に設計されている。
   chat の経路(`lcpp` の driver・harness)がどう失敗を扱うかは ADR 0093 側の設計であり、
   `engine_waking` の 900 秒待ちや non-streaming の `engine_unavailable` 再試行有無は image と
   同じではない可能性がある——**確認していない**。
2. **モデル目録の単位が違う(後述 105.2.3)。** image 役は ADR 0082/0084 で「1 役に複数行」が
   既に一般化されているが、llm 役はまだ「`llm` という 1 つの鍵」に固定されたコードがある。

### 105.2.2 base URL の決定経路と切替点(現物のコードで確認・行番号は現在の develop)

エンジンの上流 URL は `engineDef.URL`(`control-plane/engines.go`)の 1 本で決まり、
`engineUpstreamTarget`(`control-plane/engine_gateway.go:1439-1455`)がそれに provider 別の
接頭辞(`engineUpstreamPrefix`、`:1509-1514`。comfy は `/`、それ以外——llamacpp を含む——は `/v1/`)
を足して組み立てる。**借用行(`lifecycle: remote`)だけは接頭辞を付けない**(`:1442-1449`)——
向こうの `base_url` を token 応答からそのまま読む(ADR 0079 決定 4)。

いまの `llm` 行は SSM の表(`AF_ENGINES_SSM_PARAM`)由来の借用行(ADR 0079)で来ている。**LAN への
切替点は、ADR 0076 が `image` 役に対してやった合成と同じ場所になる**: `engines.go:619-668` の
`engineComfyEnvRow`(`AF_COMFY_URL` から `{key:"image", lifecycle:"external", ...}` を合成する
関数)に相当する `engineLlmEnvRow` のようなものを新設し、`AF_LLM_URL`(+ 任意の `AF_LLM_API_KEY`)
から `{key:"llm", provider:"llamacpp", lifecycle:"external"}` 行を合成する——これが**現状コードに
存在しない**ことは `grep -n "AF_LLM_URL" control-plane/` が 0 件であることで確認した。**`external`
という lifecycle 自体は実装済み**(`engines.go:126,142`)だが、**`llm` 役に対して合成する経路が
無い**、というのが今日の到達点である。

`AF_COMFY_URL` と表の行の優先順位規則(ADR 0076 決定 2「表の行が無いか external なら env が勝ち、
managed なら表が勝つ」)も `llm` にそのまま持ち込める——ただし今の `llm` 行は managed ではなく
**remote**(借用)なので、「借りているクラウドのエンジンと LAN のエンジンのどちらを勝たせるか」は
ADR 0076 が想定していない新しい組み合わせであり、優先順位の再定義が要る(**未検討**、この文書でも
検討していない)。

### 105.2.3 認証: 現状の門と、LAN 直結で失われるもの

現状(借用行)は `POST /internal/engine/token` がメンバーシップを検証してセッション別トークンを
発行し、`engine_gateway.go` の `serve()` がそれを検める(ADR 0079 決定 3)。**`external` 行では
この門がまるごと無くなる**——素の llama-server には認証が無いのが普通で(ADR 0076 決定 7 の記述が
そのまま LLM にも当てはまる)、ゲートウェイから上流への bearer(`AF_LLM_API_KEY` 相当・
`apiKey` 欄)は**運用者が前段に reverse proxy を置いて検査する場合にだけ**意味を持つ
(ADR 0076 決定 7 の 2 つの手のうち「今日使えるのは reverse proxy だけ」という限定も、egress の
enforce が未出荷なままなら今回も同じはずである——**この配備で `guide/operate/04-secure.md` の
現状が変わっていないかは未確認**)。

失われるものを具体的に言うと: (1) メンバーシップが外れた利用者のトークンを即座に無効化する
仕組み(ADR 0079 決定 3 の `liveMembership` ライブ解決)、(2) セッション単位のトークンで
「誰が何を叩いたか」を後から見分ける仕組み、(3) llama-server 自体の変更系 API
(`/slots/{id}?action=save|restore` など)を検査なしで全セッションに晒さないという保証。
LAN の llama-server にも認証機構(`--api-key`)自体はあるが、`apiKey` 欄を経由して bearer を
1 本運ぶだけなので、**メンバーごとの失効はできない**(ADR 0076 の ComfyUI と同じ限界)。

### 105.2.4 窓とモデル目録——ここが image と最も違う点

決定 7(ADR 0093)により窓は目録の `context_tokens` と `/props`(borrowed row では router 対策で
`/v1/models` の `meta.n_ctx`)から来る。LAN のエンドポイントは目録に行が無いので、この 2 つを
どこから得るかを設計しないといけない。

**ここで image 役と llm 役の非対称を見つけた**(コードで確認・**問い2の中で最も重要な発見**):
ADR 0082/0084 決定 11 により **image 役は「1 役に何行でもよい」がすでに一般化されている**
(自前 1 行・LAN の ComfyUI・借用 1 本、を `imageProviderOrder` で並べて選ぶ)。**llm 役はそうなって
いない**——`workspace/agent/agent_models.go:73-94` の `lcppModels` はこう書かれている:

```go
for _, e := range engineCatalogRows(ctx) {
    if e.Key != "llm" || e.api() != engineAPIChat {
        continue
    }
    ...
    return list  // 最初に見つかった1行を返して終わり
}
```

**`e.Key != "llm"` は role(`api()`)ではなく鍵の文字列そのものを見ている。** つまり今日、
`llm` という名前の行が 1 つある前提で書かれており、`llm-lan` のような**2 本目の chat 役の行**を
足しても `lcppModels` はそれを一切見ない(最初に `e.Key=="llm"` に一致した行しか見ない構造上、
2 本目は永久に無視される)。ADR 0084 決定 11 が image に対してやった一般化(役の粒度でピルを出し、
行の粒度で並べる)は **llm 役にはまだ来ていない**。

これは「切り替え」の意味を 2 通りに分ける:

- **(A) 1 本だけ差し替える。** 運用者が `AF_LLM_URL` を設定/変更し、CP を再起動する
  (ADR 0076 決定 2 と同じ——env は起動時 1 回読み)。`llm` という同じ鍵の中身が借用エンジンから
  LAN エンジンに入れ替わる。既存コード(`lcppModels`・driver・使用量)は無改修で動く可能性が高い
  ——「今使うエンジンをどれにするか」という**運用者の設定**であって、利用者がセッションごとに
  選ぶものではない。
- **(B) 利用者がセッションごとに複数のエンドポイントから選ぶ。** これは image 役がすでに持つ
  能力(`imageProviderOrder`)を llm 役にも作ることを意味し、`lcppModels` の書き換え(role で
  絞る・複数行を束ねてモデル一覧を作る・モデル id の衝突をどう見分けるか)が要る、**明確に
  大きい作業**である。

依頼文の「利用者が切替えられる様にしたい」は**表面上は (B) を求めているように読める**が、
運用コスト・実装コストは (A) と (B) で 1 桁違う。**これも利用者に確認すべき分岐点として書く**
(105.3 の段階化はこの分岐を明示する)。

契約テスト(`workspace/agent/internal/harness/live_contract_test.go`、ビルドタグ `manuallive`)は
`/props`・`input_tokens`・`/chat/completions/control`・streaming `usage` の 4 本の API 面を固定
している(§0093 決定 10 の代替)。これは**llama-server 自身の API を対象にしている**ので、LAN の
エンドポイントが正規の llama-server であれば同じ契約で検証できるはずだが、**運用者が独自にビルド
した版がこれらのエンドポイントを持つ保証は無い**(コメント自身が「エンジンイメージの版が変われば
挙動が変わる」と書いている——`docs/decisions/0093-lcpp-agent-kind.ja.md` 決定 10)。LAN 版は
バージョン固定の仕組み(ADR 0093 段2負債7の pin)の外にあるので、**ドリフトの検知手段が今日より
さらに弱くなる**。

### 105.2.5 費用と運用

決定 8(ADR 0093)の実装(`workspace/agent/usage_price.go:175` の `usagePriceOf`)は、**kind が
`KindLcpp` なら常に `price=0`・`src="gpu-billed"` を返す**——`external` 行かどうかを見ていない。
LAN の場合、GPU 費用はこの配備が払っているものではなくなるが、**この配備は元々 GPU 費用を
払っていない体(借用行は先方が払う)なので、`price=0` という結果自体は LAN でも変わらず正しい**。
ただし `src="gpu-billed"` という文言は運用者から見て正確ではなくなる(実際は「利用者の自前機材」)
——これは ADR 0076 決定 8 が image 役でやった区別(external 行には費用を付けない、という同じ
結論)と同じ形だが、**文言レベルでの手当ては今回新たに要る**。生トークン数の二重計上
(`feature=session` と `feature=engine.llm` の両方に現れる、ADR 0093 段2実装記録)は LAN でも
同じ既存条件のまま引き継がれる。

### 105.2.6 「利用者の LAN」と「Workspace が到達できる網」が同じ保証があるか

**ここが企画全体の前提であり、確かめられた範囲と確かめられなかった範囲を分けて書く。**

- **確認できたこと**: ADR 0076 の到達性の記述(`docs/decisions/0076-external-image-engine-on-lan.ja.md:49-56`)
  は明確に **docker 配備**を対象にしている——「docker 配備の CP は `network_mode: host` なので
  LAN に届く」「docker の Workspace コンテナは専用ブリッジから NAT で LAN に出られる」。つまり
  ADR 0076/ADR 0093 P3 が想定する「LAN」とは、**CP 自体が利用者の LAN と同じ物理ネットワーク上に
  ある自前配備(compose/native)**を指している。
- **確認できなかったこと(分からなかった、と明記する)**: この依頼が想定している配備形態が
  compose/native(自前ホスト)なのか ecs-ec2(AWS 上でホスティングされるマネージド配備)なのかは、
  依頼文からは判別できない。**もし ecs-ec2 配備を指しているなら、Workspace コンテナは AWS の
  VPC の中にあり、利用者の自宅/オフィスの LAN とは別のネットワークである**——両者の間に
  VPN/Direct Connect のような明示的な経路が無い限り、「LAN の llama.cpp」に ECS 上の Workspace
  から到達する手段は無い。ADR 0079 が却下した案の 1 つ「エンジンのインスタンスへのトンネル/VPN」
  (却下理由:「到達性がそのまま権限である」「インスタンスは寿命が短い」)はエンジン間の話だが、
  同じ理由(何を認証にするか)は利用者の自宅ネットワークへの経路にも刺さる。
  - この worktree のセッション自身が動いている環境(Agent Fleet Workspace コンテナ)を見ても、
    「per-user container」「ECS 的な性質」を示す記述が組織方針(`/etc/claude-code/CLAUDE.md`)に
    多数あり、**少なくともこの Workspace の一部運用形態は ECS 系である**——ただし依頼元の配備が
    具体的にどれかは本調査の範囲では確認していない。
  - したがって: **compose/native 配備なら ADR 0076 と同じ機構(NAT ブリッジ)がそのまま使え、
    「利用者の LAN」と「Workspace の到達網」は事実上同じ**。**ecs-ec2 配備なら、追加のネットワーク
    経路(VPN 等)が無い限り成立しない**——これは推測ではなく、ADR 0076 決定 7 の記述の対象範囲が
    docker/native に限定されていることの裏返しである。**どちらの配備を対象にするかを、実装に
    入る前に利用者に確認する必要がある。**

---

## 105.3 問い3: 見積りと段階化

ADR 0093 決定 9 と同じ形(段階 + 次への門)で書く。**やる場合の見積りであって、着手を勧めている
わけではない**——105.1/105.2 で見つけた通り、両方の要望とも「利用者の意図がどちらの粒度か」が
確定していない。

### 要望1(Console のオン/オフ)

| 段 | 内容 | 次への門 |
|---|---|---|
| 0 | **利用者に確認**: (a) オン/オフは利用者本人の設定か、テナント管理者の設定か。(b) テナント
  管理者向けでよいなら `allow_engine_llm`(ADR 0084・実装済み)がそのまま使える——ただし
  opencode の自前エンジン利用も道連れで消えることの説明が要る。(c) 利用者本人の設定を望むなら、
  新しい ui-prefs 相当のフラグ(opencode の `UsageOff` と同型)を作ることになり、それは
  「既存の概念で表現する」という要求そのものと緊張する——**その緊張を許容するかどうかも
  利用者の判断**。 | 無し——出す(コード変更ゼロ) |
| 1 | (b) を選んだ場合: Console 側で `allow_engine_llm` を利用者(自分のテナントの管理者)から見える
  形にする改修は不要(ADR 0084 が super_admin 専用と決めている——テナント管理者にすら見せない)。
  この段は実質「何もしない」で終わる。 | — |
| 1' | (c) を選んだ場合: 新フラグの設計(ui-prefs か settings.ts か)・`registry.ts` の
  `available()` を上書きする配線・`mcp_stdio.go` の許可リストとの整合(105.1.3 の 2 ゲート
  同期)・薄い `LcppCard`(`LaunchDefaults` のみ、105.1.1)。見積り: **2〜4 セッション日**
  (カード自体は小さいが、2 ゲートを1つの設定源に揃える設計とテストに時間がかかる)。 | 実装前に
  ADR 化を検討(後述) |

**やらない判断の条件**: テナント管理者向けの粗い on/off で運用上困っていないなら、105.1.1 が
指摘した通り「空のカードを出すくらいなら出さない」で止めてよい。lcpp を使わないテナントは
`allow_engine_llm=false` で足りる。

### 要望2(LAN エンドポイントへの切替)

| 段 | 内容 | 次への門 |
|---|---|---|
| 0 | **利用者に確認**: (a) 対象配備は compose/native か ecs-ec2 か(105.2.6)。(b) 「切替」は
  運用者が設定する 1 本の差し替え(A)か、利用者がセッションごとに選ぶ複数エンドポイント(B)か
  (105.2.4)。 | 無し——出す |
| 1 | (A)・compose/native 前提なら: ADR 0076 の型をそのまま `llm` 役に写す——
  `engineLlmEnvRow`(`AF_LLM_URL`/`AF_LLM_API_KEY` の合成)、`external` lifecycle の `llm` 行への
  適用、chat 経路の即時失敗/再試行の設計(105.2.1 の未確認点)、契約テストの LAN 版での成立確認
  (105.2.4)、guide の追補。ADR 0076 の P0 の見積り(4 レーン)に相当。見積り: **5〜8 セッション日**
  (image より軽いのは Console 側の改修がほぼ無いこと——lcpp カード自体が薄いため——、重いのは
  chat 特有の失敗経路の設計)。 | 実機 1 回(運用者の LAN で `create_session(kind="lcpp")` が
  1 往復する)——ADR 0076 と同じく、この 1 回はセッションの仕事ではなく運用者の手順 |
| 2 | (B)を求めるなら追加で: `lcppModels` を role 基準の複数行対応に書き換え、`imageProviderOrder`
  相当の並び設定を llm 役に作る、モデル id の衝突回避。見積り: **段 1 に加えてさらに 4〜6
  セッション日**。 | — |
| 3 | ecs-ec2 配備を対象にするなら、まず 105.2.6 の到達性の前提(VPN/Direct Connect 等)を
  ネットワーク設計として別途確定させる必要があり、**これは Agent セッションの調査範囲外**
  (組織のネットワーク構成の意思決定)。 | — |

**やらない判断の条件**: 対象が ecs-ec2 で、利用者の LAN への経路(VPN 等)が無い/作る予定が
無いなら、この要望はネットワークの前提から崩れるので着手しない。compose/native でも、
lcpp の実運用(20 ターン級の実作業)がまだ限定的(ADR 0093 決定 9 門 (c) の「族で 2 倍以上の
費用差」のような未解決点が残る段階)なら、借用エンジン 1 本の運用を先に安定させてから LAN を
足す方が手戻りが少ない。

---

## 105.4 ADR にするかどうか

**ADR を起こすべきだと考えるが、起票はしない(利用者の判断)。** 理由:

- 要望2は ADR 0076 の予告(P3)を実際に埋める話であり、ADR 0076 自身が「型」を規定している
  ので、新設するとしても**ADR 0076 の決定を llm 役に拡張する追補**という形になり、白紙の ADR
  よりも「0076 決定 X は llm でも成り立つ/成り立たない」という比較の形の文書が適している——
  これは ADR の書式(決定・却下案・上書きする既存の決定)そのものである。
- 要望1は、105.1.2 で見た通り「利用者本人の設定か管理者の設定か」という**製品判断**が先に
  要る。ADR はその判断が決まってから、実装方式(新フラグかどうか)を固定する文書として書くのが
  筋である——判断より先に ADR を書くと、決定 9 の門のような「利用者が承認する」ステップが
  文書の途中に挟まる形になり、ADR 0093 のときと同じ運びになる。

したがって: **105.3 の「段 0」(利用者への確認)の答えが出た時点で、要望2は ADR 0076 への
追補(番号は 0076 のままか、新規にするかは利用者の判断)、要望1は必要であれば軽量な docs/log の
実装記録で足りる、という提案にとどめる。**

---

## 105.5 分からなかったこと(まとめ)

- 依頼元の配備形態(compose/native か ecs-ec2 か)。105.2.6 の到達性の結論はこれに完全に依存する。
- 「オン/オフ」「切替」が利用者本人の設定を指すのか、テナント管理者の設定を指すのか。
- `session_handlers.go`(または REST 直叩き)に lcpp 固有の kind 拒否ロジックがあるかどうか
  (105.1.3)。`mcp_stdio.go` の許可リスト以外は悉皆確認していない。
- lcpp の chat 経路(driver/harness)が external 行の cold-start 失敗(`engine_unavailable`)に
  対して image と同じ再試行をするかどうか(105.2.1)。
- この配備の `guide/operate/04-secure.md` の egress enforce 出荷状況が ADR 0076 執筆時
  (2026-09-11)から変わっていないか(105.2.3)。今回は確認していない。

---

*本文書のために新しく測ったものは無い。根拠は (a) ADR 0076/0079/0084/0093 の記述、
(b) 2026-09-21 にこのリポジトリのコードから読んだ事実(file:line で示した)、を出所ごとに
書き分けた。*
