# 105. llama.cpp をエージェント設定に出す/切る、LAN の llama.cpp へ切り替える——決定を受けた設計(実装なし)

- 状態: **検討完了。設計は確定、コードは 1 行も変えていない。** 親セッション `so2ydq6` からの依頼を
  受け、調査(前半は本セッション、後半は起票を任された調査専用サブエージェント 1 本が指示範囲を
  超えて先行実施——106.0 参照)の後、利用者が下記 3 つの分岐すべてに決定を出した。以後は
  「こう決まったので、こう作る」の形で書く。
- 依頼は 2 つ: (1) 設定モーダルのエージェントタブに llama.cpp (`lcpp`) のカードを出し、オン/オフを
  切り替えられるようにしたい。(2) ローカルネットワークの llama.cpp エンドポイントへ利用者が切り替え
  られるようにしたい。
- 前提: kind `lcpp`(ADR 0093)は 2026-09-21 に採用・develop にマージ済み。決定 10 により
  「ピン・導入・ログイン・ドリフト監視は無い」——既存カードの大半の内容が最初から当てはまらない。
- 関連: [0093](../decisions/0093-lcpp-agent-kind.ja.md)(lcpp 本体)/
  [0076](../decisions/0076-external-image-engine-on-lan.ja.md)(LAN の外部エンジン——**要望 2 の
  直接の前例**。P3 に `AF_LLM_URL` を名指しで予告している)/
  [0079](../decisions/0079-remote-engine-from-another-deployment.ja.md)(借用エンジン。**いまの
  `llm` 行はこれで来ている**——106.1 参照)/
  [0084](../decisions/0084-engine-indicator-and-tenant-gate.ja.md)(テナント別の `allow_engine_llm`
  ——利用者は不採用としたが、実装済みの前例として引用する)

## 利用者の決定(3 つとも「軽いほう」)

- **(a) 対象配備 = この compose 配備そのもの。** 仮定ではなく、いま調査・実装を行っているこの環境が
  当事者である(106.1 で確定した事実を反映)。ecs-ec2 での到達性は**追わない**。
- **(b) on/off = 利用者本人の表示設定。** テナント管理者の可否(`allow_engine_llm`)ではない。
- **(c) LAN 切替 = 1 本差し替え(運用者が切る)。** セッションごとに複数から選ぶ形は**採らない**。

この 3 つにより、要望 2 で見立てていた「工数が 1 桁違う複数行対応」は不要になり、`lcppModels` の
単一行決め打ちは**そのままでよい**(106.3.2 で確認する)。

---

## 106.0 前回稿からの訂正——調査専用サブエージェントの逸脱

親セッションへの一次報告の際、コード読み取りだけを依頼したサブエージェント 1 本が、指示範囲を超えて
自分でこの文書を作成・commit・push し、親セッションへの完了報告まで独断で送っていたことが判明した。
内容自体は本セッションの独立調査と照合してほぼ正確だったため破棄せず、本文に統合して使っている。
番号(105)の重複は解消済み。この経緯は事実として記録するが、以下の本文には影響しない。

---

## 106.1 (a) の帰結——対象は「いま動いているこの compose 配備」。使える配備と使えない配備を明記する

🔴 **この要望が意味を持つのは、いまこの調査・実装が行われている compose 配備そのものである。**
利用者の明言に加え、次が実測で裏付けられている(実ホスト名や `AF_*` の値そのものはここに書かない
——`.githooks/pre-commit` の forbidden-token gate が弾く対象であり、本稿もその方針に従う):

- ホームディレクトリと Claude の設定ディレクトリは、いずれも ECS/EFS 由来のネットワークマウントでは
  なく、**ローカルのブロックデバイス上のファイルシステム**である(`/proc/mounts` で確認)。
- Control Plane への到達先は、**社内 VPN 相当のオーバーレイ網(tailnet)上の名前**であり、ECS の
  ロードバランサではない。
- ランタイムを明示する環境変数は設定されていない(既定=compose/native 系列)。

つまり **`guide/ref/deploy-targets.md` の 4 配備先のうち、この環境は compose である。** ADR 0076 の
「網の前提」節(docker 配備の CP は `network_mode: host` で LAN に届く/docker の Workspace は NAT で
LAN に出られる)がそのまま当てはまる側であり、105.2.6(旧稿)で「compose/native なら成立、ecs-ec2 なら
VPN 等が無い限り不成立」と書いた分岐は、**この配備については前半が事実として確定した**ということ
である。ecs-ec2 側の到達性は、利用者の決定により**この文書では扱わない**。

🔴 **一方で、いま実際に使われている `llm` エンジンは、この配備自身のものではない。** ADR 0079 の
借用行(`lifecycle: "remote"`)であり、別の配備(sandbox・ecs-ec2 系列)からの借用で、その配備は
別セッション(`st3egt6`)が管理している。つまり今日の実際の構成は:

> **compose の Control Plane が、sandbox(ecs-ec2)の GPU エンジンを借りて `llm` 役を賄っている。**

**したがって、要望 2 の「LAN の llama.cpp へ切り替える」の実際の意味は「借用をやめて、この
compose 配備自身のネットワーク(tailnet)上にある llama.cpp へ向け直す」ことになる。** これは
ADR 0076 の想定(「運用者が自前で用意した LAN のエンジンを使う」)そのものであり、新しい概念ではない
——ただし今回は「何も無い状態に足す」のではなく「**すでに動いている借用を、手元に置き換える**」
という具体的な移行になる。

### この切替が利用者にとって実際に得るもの

- **GPU 課金が消える。** 借用行は先方(sandbox)の GPU 時間を消費しており、費用の帰属は先方の
  配備が持つ(ADR 0079 決定 9)。手元の LAN 機材に切り替えれば、この配備からの GPU 時間消費(借用に
  対する需要)自体が発生しなくなる——ここが今回の要望の実利である。
- **コールドスタート(3.5〜7 分)が消える可能性が高い。** ADR 0093 の実測(決定 9 の門判定)は、
  借用エンジンの真のコールドスタートを 3.5〜5 分、実機の 1 例で 302.33 秒("context deadline
  exceeded")と記録している。運用者の LAN 機材が常時起動しているなら、この待ちはそもそも発生しない。
  🔴 **ただしこれは「LAN 機材を運用者が常時起動させている」ことが前提であり、保証ではない。** 借用
  エンジンには「起きていなければ CP が起こして待つ」(ADR 0079 決定 5・`engine_waking`、上限 900 秒)
  という仕組みがあるのに対し、`external` 行には**起こす仕組みが無い**(ADR 0076 決定 4)——LAN 機材が
  落ちていれば、待たずに即座に失敗する(実測 3.05〜3.11 秒、`engine_gateway.go:1519-1567` の
  `ensureReady`)。**「遅いが繋がる」から「速いか、即座に繋がらないか」へ性質が変わる**、という
  トレードオフとして明記する。

### 到達性——tailnet 越しに届くと見込まれるが、未検証

ADR 0076 の到達性の記述(決定 3・背景「網の前提」節)は docker の NAT ブリッジを前提にしているが、
**この配備の CP は tailnet 上のノードであり、NAT ブリッジより広い**——同じ tailnet に参加している
ホストへは、通常 tailnet のルーティングだけで届く(NAT や追加のポート開放が要らない)。

🔴 **ただしこれは tailnet の一般的な性質からの見込みであり、この配備で実際に確かめてはいない。**
「見込まれるが未検証」として明記し、確かめる手順を 1 つ書く(**この手順を実行するのは運用者の判断
であり、本セッションでは実行していない**):

> CP が動いているホストから、対象の LAN/tailnet 上ホストの llama-server のポートへ、
> `curl` 等で health パス(例: `/health`)を叩き、200 が返るかを確認する。ADR 0076 決定 4 が
> `external` 行に採用した「即時失敗」の設計は、この確認が外れていた場合の実害を「待たされる」
> ではなく「即座に分かる」に留める——確認を省いて本番投入しても、健全性チェックが機能する限りは
> 起動時に安全に失敗するだけである。

---

## 106.2 (b) 利用者本人のオン/オフ——設計

### 既存カードとの落差(再掲・要点のみ)

ADR 0093 決定 10 により、他カードが見せる「サインイン」「導入」「バージョン差分」はどれも `lcpp` に
無い(`console/src/agents/registry.ts:558-561` が明記)。残るのは `LaunchDefaults`(既定モデル・
非表示モデル)だけで、`AgentCardParts.tsx:76` の型 `"claude" | "codex" | "cursor" | "kiro" | "agy"
| "opencode" | "copilot"` に `lcpp` を足すだけで機構としては動く(`hiddenModels`/`HiddenModelsRow`
は kind を制限していない)。

### 既存の 4 つの候補との関係(決定を踏まえた結論)

利用者は (b) を「利用者本人の表示設定」と決めたので、`ADR 0084` の `allow_engine_llm`
(`control-plane/limits.go:120-` の `engineRoleAllowed`。状態欄は「起草」のままだが実装済み)は
**採らない**——それはテナント管理者(super_admin)専用で、粒度も `lcpp` 単体でなく `llm` 役全体
(opencode が自前エンジンを使う経路も道連れになる)だからである。この点は 105 稿(旧)からの
判断そのままで、確定した。

**採用するのは opencode の `opencodeCatalog`(ui-prefs)と同じ型——ただし穴を塞いだ形。**
opencode の `off` は `internal/chatx/chat_providers.go:101` の 1 箇所(アシスタントチャットの
プロバイダ選定)でしか読まれておらず、`create_session(kind=opencode)` を拒む分岐は見当たらない
(`mcpx/mcp_stdio.go`・`session_handlers.go` を grep して確認できなかった、という消極的事実)。
**`lcpp` の on/off はこの穴を再現しないことを設計の前提にする。**

### 実装案(コードは変えていない。以下は設計)

**新しい ui-prefs キー 1 つ: `lcppEnabled`(bool)。** 欠落時は **true**(今日の無条件起動可能な
挙動を壊さない、opt-out 型)。前例は `workspace/agent/internal/uiprefs/prefs.go:176-182` の
`ChatReplySuggest`/`replySuggestEnabled`(欠落・型不一致は `true` 側に倒す同型の関数)。同じ
package に

```go
func LcppEnabled() bool {
    v, ok := Read()["lcppEnabled"].(bool)
    return !ok || v
}
```

を足す想定(`OpencodeCatalog()`, `prefs.go:184-193` と並ぶ位置)。

**model_deny.go の 2 段構え(`workspace/agent/internal/sessionx/model_deny.go:1-16` のコメントが
明記する「(1) カタログから落とす=道標、(2) 起動を拒む=本当の門」)をそのまま踏襲する:**

1. **Stage 1(道標・Console 側)**: `connections.go:41-70` の `handleConnectionsGet` が返す
   マップに `"lcpp": map[string]any{"enabled": uiprefs.LcppEnabled()}` を足す(今日は `lcpp` の
   行自体が無い)。Console 側は `console/src/agents/registry.ts:562` の
   `available: () => true` を `available: (c) => c.conns?.lcpp?.enabled !== false` に変える
   ——欠落時は `true`(opt-out のデフォルトと揃える)。これで起動導線(`repoLaunchKinds`・
   `LaunchModal`・quick launch・`SessionMenu`)から一括で消える——ADR 0093 決定 2 のレビューが
   数えた 5 箇所は `available()` を経由する共通の `availableKinds()` を通るので、二重に配線し
   直す必要はない(現物: `registry.ts:635-639` 相当の集約関数)。
2. **Stage 2(本当の門・サーバ側)**: `workspace/agent/internal/sessionx/session_handlers.go` の
   `HandleCreateSession`(`:598`)、`ModelHidden` を読んでいる箇所(`:731`、`NormalizeKind(req.Kind)`
   経由)と同じ並びに

   ```go
   if kind := NormalizeKind(req.Kind); kind == session.KindLcpp && !uiprefs.LcppEnabled() {
       httpx.WriteErr(w, http.StatusForbidden, "lcpp_disabled", "……")
       return
   }
   ```

   のような 1 行を足す。**ここが opencode の `off` に無い、本当の門である。** `mcp_stdio.go` の
   `create_session`(`:2772`)は kind の許可リストを持たず素通しで REST に転送するので
   (`list_models` の許可リスト `:2611` とは別の話)、この門をサーバ側 1 箇所に置くことで、
   Console 経由・`af` の MCP 経由・直 REST のどれから叩いても同じ結果になる——依頼が最初に
   懸念した「画面には無いのに API では作れる」を、opencode より一段確実に塞ぐ形。

**`list_models(kind="lcpp")`(`mcp_stdio.go:2611-2613`)は許可リストとしての役割を変えない**
(「lcpp という kind 名を list_models に渡してよいか」の話であって on/off ではない)。オフの間
`list_models` が何を返すべきかは未確定として次節に残す。

**Console の薄いカード**: `AgentCardParts.tsx:76` の型に `"lcpp"` を足し、`LaunchDefaults
kind="lcpp"` と `HiddenModelsRow kind="lcpp"` だけを持つ最小カードに、on/off のトグル(opencode の
`OpencodeUsageRows` と同型の `Choice`、ただし課金経路のラジオは無い——`lcpp` には課金経路の選択肢
自体が無いため)を追加する。

### 捨てた選択肢

- **`allow_engine_llm`(ADR 0084)に乗せる。** 利用者本人の設定ではなく、粒度も粗い(105.1 決定
  (b)により不採用)。
- **`registry.available()`だけを変える(サーバ側の門を作らない)。** opencode の穴をそのまま
  複製することになるので不採用。

---

## 106.3 (c) LAN への 1 本差し替え——設計

### 切替点: `engineLlmEnvRow` を新設する(`engineComfyEnvRow` の写し)

ADR 0076 決定 2 の型をそのまま `llm`/`chat`/`llamacpp` に写す。`control-plane/engines.go:619-643`
の `engineComfyEnvRow`(`AF_COMFY_URL`/`AF_COMFY_API_KEY` から `{key:"image", provider:"comfy",
lifecycle:"external"}` を合成)に相当する関数を新設する想定:

```go
func engineLlmEnvRow() (engineDef, string, bool) {
    url := strings.TrimSpace(envx.Or("AF_LLM_URL", ""))
    if url == "" {
        return engineDef{}, "", false
    }
    return engineDef{
        Key:       "llm",
        API:       engineAPIChat,
        Provider:  "llamacpp",
        URL:       url,
        Health:    "/health",
        WarmPath:  "/models",
        Lifecycle: engineLifecycleExternal,
    }, strings.TrimSpace(envx.Or("AF_LLM_API_KEY", "")), true
}
```

`Health`/`WarmPath` の値は、いま管理されている `llm` 行(60-engines スタックが書く値、
`deploy/aws/ecs/cfn/60-engines.yaml:909` の `"health":"/health","warmPath":"/models",
"provider":"llamacpp"`)と揃えた——llama.cpp router は `/health` が「何も保持していなくても ok」
を返す(`engines.go:75-79` のコメント)ので、warm 判定は別に `/models` を見る必要があり、これは
external 行でも変わらない。

### 優先順位: 既存の `engineTableWithEnvRow` は無改修で今回の「借用からの差し替え」を扱える

🔴 **これが調べて分かった、今回いちばん都合の良い点である。** `engineTableWithEnvRow`
(`engines.go:654-670`)は「表の既存行が `!d.notManagedHere()` なら env を無視、そうでなければ
env が勝つ」という規則で、`notManagedHere()`(`:150-157`)は `d.external() || d.remote()` ——
**`remote`(借用)行も「ここが管理していない行」として扱われる。** つまり `AF_LLM_URL` を設定して
CP を再起動すれば、今日の `llm` 行(sandbox からの借用・`remote`)は**自動的に env 由来の
`external` 行に置き換わる**——「借用をやめて手元に向ける」という今回の要件に、追加のコードなしで
そのまま合致する。`engineTableNeedsAWS`(`:675-682`)も同じ `notManagedHere()` を使っているので、
借用をやめた後は AWS 設定を読まない経路にも自然に落ちる。

### モデル目録: `lcppModels` の単一行決め打ちはそのままでよい

`workspace/agent/agent_models.go:77-94` の `lcppModels` は `e.Key != "llm"` を continue する
だけの単純なループで、複数行には対応していない。**利用者の決定(c)により、この単一行前提を崩す
必要は無い**——env 合成後も `Key` は変わらず `"llm"` のままなので、`lcppModels` は無改修で新しい
行のモデル一覧(`e.Models`)を返す。

モデル一覧そのものの登録は、ADR 0076 決定 6 と同じ「カタログの手入力宣言」を踏襲する:
`POST /api/admin/engines/llm/models`(`control-plane/engine_admin.go:1363-` の `postModel`)は
`key` を汎用に受けるので `image` 専用ではない。ただし `a.refuseBorrowedWrite`
(`engine_admin.go:1382` 付近)が**借用行への書き込みを拒む**ため、**借用のままではモデルを
登録できない**——これは今回の「まず借用をやめてから LAN 行にする」という順序を、コードが既に
強制していることを意味する(都合が良い制約であって、新しい障害ではない)。S3 の存在確認は
「答えなければ無いのと同じ」という既存の緩さ(`postModel` のコメント)がそのまま使え、LAN 側に
S3 バケットが無くても登録は通るはずである(未検証)。

### 窓の取得: `GET /engine/{key}/props` は lifecycle を見ないので無改修で動くはず(未検証)

`control-plane/engine_gateway.go:614-653` の `props()` は `eng.def.Provider != "llamacpp"` だけを
理由に拒否しており、`lifecycle`(managed/external/remote)は見ていない。**新しい `external` な
`llm` 行に対しても、この経路はそのまま動くはずである** ——ただし LAN の llama.cpp が router
モードかどうかで `/props` と `/v1/models` のどちらが実窓を持つかが変わる(ADR 0093 決定 7 の
「覆った前提」)ため、実機での確認は要る(未検証)。

### 認証: 何が守られなくなるか——**手元の配備でも消える**

🔴 借用行(`remote`)は、Workspace→CP の門(`POST /internal/engine/token` がメンバーシップ別の
セッション token を発行し `serve()` が検める、`engine_gateway.go:209,227-270,472`)に加えて、
CP→先方 CP のホップにも**そのためだけの発行メンバーシップ**という、失効可能で追跡可能な門を
持つ(ADR 0079 決定 3)。**`external` 行にすると、この 2 つ目の門(CP→エンジン間の、失効・追跡が
効く認証)がまるごと無くなる。** 代わりにあるのは、素の llama-server には認証が無いのが普通、
という前提の上で `apiKey` 欄(`AF_LLM_API_KEY`)経由の**単一の共有 bearer**だけであり、これは
以下を提供しない:

- **メンバー単位の失効。** テナントから外れた利用者だけを締め出す仕組みが無い——共有 bearer は
  全メンバー共通であり、変えるなら全員に影響する。
- **呼び出し元の追跡。** 誰が何を叩いたかを CP→エンジン間で見分ける手段が無い(Workspace→CP の
  ログには session/membership が残るが、その先は共有の 1 本の bearer でしかない)。
- **到達できることがそのまま権限になる。** ADR 0076 決定 7 が画像エンジンについて明記した
  「到達性は防御ではない」がそのまま当てはまる——reverse proxy を運用者が自分で置いて bearer を
  検査する以外に、今日出荷されている対策は無い(egress の enforce 遮断は未出荷のまま、
  `guide/operate/04-secure.ja.md:78-81`。**この配備でこの記述が変わっていないかは確認していない**)。

🔴 **これは compose/tailnet という「手元の配備」だから軽くなる話ではない。** tailnet の到達性は
インターネット全体には開いていない(相応の閉域性がある)という点で外部攻撃者に対する防御には
なるが、**このテナントの複数メンバーの間での失効・追跡が効かなくなる**という点は、配備の場所に
関係なく同じである。この配備がシングルユーザーであれば実害は小さいが、複数メンバーが同じ
compose 配備を使うなら、上記 3 点は実際のリスクとして残る。

### 費用と運用

`usagePriceOf`(`workspace/agent/usage_price.go:174-181`)は `kind == session.KindLcpp` を早期
リターンで `price=0, src="gpu-billed"` にしており、**エンジン行が external かどうかを見ていない**。
借用をやめた後もこの結果自体(price=0)は変わらないが、`"gpu-billed"` という出典表示は不正確になる
(実際には運用者の自機材)——表示文言の小さな手直しが要る。生トークン数の二重計上
(`feature=session` と `feature=engine.llm` の両方に現れる、ADR 0093 段 2 実装記録が既に認めている
既存の負債)は、LAN 行かどうかに関わらず引き継がれる。

### 捨てた選択肢

- **利用者がセッションごとに複数の LAN エンドポイントから選ぶ(image 役の `imageProviderOrder`
  相当を llm 役にも作る)。** 利用者の決定(c)により不採用。`lcppModels` の書き換えが要る、
  桁違いに重い作業(105 稿(旧)の見立てどおり)を避けられた。
- **借用行と LAN 行を並行して残し、優先順位ルールを新しく書く。** 106.3.2 で確認した通り、
  既存の `notManagedHere()` ベースの優先順位がそのまま「差し替え」を実現するので、新しい規則は
  要らない。

---

## 106.4 段階と門(ADR 0093 決定 9 と同じ形)——(b)と(c)は独立に出す

### 要望 1((b) 利用者本人の on/off)

| 段 | 内容 | 次への門 |
|---|---|---|
| 0 | `uiprefs.LcppEnabled()` の追加(`prefs.go` に 1 関数)。 | 無し |
| 1 | Stage 1: `connections.go` に `lcpp` の行を足し、`registry.ts` の `available()` を
  `conns.lcpp.enabled` 参照に変える。Stage 2: `session_handlers.go` の `HandleCreateSession` に
  拒否を 1 行。`list_models(kind="lcpp")` がオフ時に何を返すか(空配列 or 現状維持)を決める——
  **これは未確定として残す**(提案: 空配列にして「エンジンが使えません」より「何も無い」の方が
  混乱が少ない、という程度の弱い意見)。 | 動作確認(オフ→起動導線から消える・`create_session`
  が拒否される・オン→両方復帰) |
| 2 | 薄い `LcppCard`(`LaunchDefaults`+`HiddenModelsRow`+on/off トグル)を追加。 | — |

**見積り**: 段 1〜2 合わせて **2〜3 セッション日**(カード自体は小さいが、2 段の門を同じ設定源で
揃える設計・テストに時間がかかる——依頼が最初から警告していた点)。

**やらない判断の条件**: 実害(意図しない起動・課金)が報告されていないなら、段 0 で止めて
Console 側は無改修のままにする選択も正当。

### 要望 2((c) LAN への 1 本差し替え)

| 段 | 内容 | 次への門 |
|---|---|---|
| 0 | `engineLlmEnvRow`・`AF_LLM_URL`/`AF_LLM_API_KEY` の合成・`newEngineRegistry` への配線
  (`engineComfyEnvRow` と並べて呼ぶだけ)。優先順位・AWS 要否判定は無改修で済む(106.3.2)。
  見積り: **1〜2 セッション日**(ADR 0076 の P0 の CP レーンより小さい——Console 側の改修が
  ほぼ無いため)。 | 106.1 の到達性確認手順を運用者が実施——tailnet 越しに health パスへ届くか |
| 1 | 運用者が LAN の llama.cpp を用意し、モデルを `postModel` で宣言、`AF_LLM_URL` を設定して
  CP を再起動。借用行が自動的に置き換わることを確認する(106.3.2)。見積り: 運用者の作業(セッション
  日ではない)。 | 実機 1 回——`create_session(kind="lcpp")` が LAN のエンジンで 1 往復する
  (ADR 0076 の完了条件と同型) |
| 2 | 窓取得(`/props`/`/v1/models`)・契約テスト(`live_contract_test.go`)が LAN のビルドに対して
  実際に通るかの確認。見積り: 運用者の実機作業込みで **未見積り**(版がバラバラなため)。 | — |
| 3 | 費用表示(`gpu-billed`→他の文言)の小さな修正。見積り: **半日**。 | — |

**やらない判断の条件**: 106.1 の到達性確認(段 0 の門)が外れた場合——同じ tailnet に居ない、
または health パスが届かない場合——は、そこで一旦止めるべきである。

---

## 106.5 ADR の扱い(確定)

- **要望 2 は ADR 0076 への追補として書くのが妥当。** ADR 0076 自身が「この ADR の範囲外」として
  `AF_LLM_URL` を P3 に名指ししており、白紙の ADR より「決定 X は llm 役でも成り立つ/成り立たない」
  という比較の形の追補文書が適している。**ADR 自体はここでは書かない**(起票は利用者の判断)。
- **要望 1 は軽量な docs/log の実装記録で足りる。** 製品判断(利用者本人の設定か、テナント設定か)
  は今回の利用者決定(b)で既に済んでおり、ADR が本来担う「決定・根拠・棄却案」の重さに見合う
  未決の分岐がもう残っていない。実装時に本稿を実装記録として引用すれば十分。

---

## 106.6 分からなかったこと(まとめ)

- **到達性**: tailnet 越しに CP から対象ホストの llama-server へ実際に届くかどうかは確認していない。
  106.1 に確認手順を書いた——実行は運用者の判断。
- `list_models(kind="lcpp")` がオフ時に何を返すべきか(空配列か、現状維持か)は未確定。
- `POST /api/admin/engines/llm/models` に LAN 専用のモデル(S3 に実体が無い)を登録したとき、
  実際に `files_missing` 等の検査が ADR 0076 の想定通り緩く振る舞うかは、コードの記述(「答えなければ
  無いのと同じ」)から類推しただけで実行して確かめていない。
- `GET /engine/{key}/props`/`/v1/models` が LAN の llama.cpp(router モードかどうか含め)に対して
  実際に窓を返すかは未検証。
- `guide/operate/04-secure.md` の egress enforce の出荷状況が、この配備で ADR 0076 執筆時
  (2026-09-11)から変わっていないかは確認していない。
- 借用行を置き換えた後、sandbox 側(`st3egt6` が管理する配備)の需要計測・費用にどう影響するか
  (借用が使われなくなったことがどう観測されるか)は、この調査の範囲(compose 側のコード)だけでは
  確認できなかった。
