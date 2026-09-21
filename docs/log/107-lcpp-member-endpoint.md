# 107. 設定モーダルの llama.cpp カードに、メンバー個人の接続先を足す

- 状態: **実装完了。実機の LAN サーバには接続していない**（親セッションの指示どおり、試験はすべて
  `httptest` の偽サーバで行った。実機確認は親セッション `sikdmnv` と利用者が行う）。
- 依頼: `[agent-fleet:spawn from=sikdmnv]`。設定モーダル > エージェント > llama.cpp のカードで、
  **利用者自身が接続先（LAN の llama-server の URL と API キー）とモデルを設定できる**ようにする。
  設定されていれば、lcpp のセッションは Control Plane を経由せず**その接続先に直結**する。
- 関連: [105](105-lcpp-console-toggle-and-lan-endpoint.md)（llama.cpp カードの on/off・**LAN 切替は
  「1 本差し替え・運用者が切る」という決定 (c) を出した文書**——本稿はその決定に**メンバー個人の
  切替という第 2 の経路を追加する**、106 とは独立の変更）/
  [106](106-lcpp-lan-run-helper.md)（LAN 実行の支援設計・§10 の実機測定が `build_info`・
  `/v1/models` の `meta.n_ctx`・router/単機の非対称の実測元）/
  [0093](../decisions/0093-lcpp-agent-kind.ja.md)（lcpp kind 本体・`internal/harness` の func-var
  継ぎ目）/[0084](../decisions/0084-engine-indicator-and-tenant-gate.ja.md)（`allow_engine_llm`
  ——本稿はこれを**迂回する**。新しい門は作らない）

## 105 との関係——決定 (c) を差し替えたのではなく、上に足した

105 の決定 (c)「LAN 切替は 1 本差し替え（運用者が切る）」は `AF_LLM_URL` / `AF_ENGINES_JSON` の話
のままで、**変えていない**。本稿が足すのは、それとは独立にメンバー本人がセッションの接続先を選べる
第 2 の経路。優先順位は「メンバー個人の接続先 > 配備の `llm` エンジン（105 の経路）」——メンバーが
何も設定しなければ、今日どおり 105 の経路がそのまま効く。

## 決定 1: メンバーの設定は常に勝つ。空にすれば今日の挙動に戻る（利用者指示のまま実装）

`internal/harness` の 3 つの func-var（`workspace/agent/engines.go:71-73` の `init()`)——
`harnessEngineToken`・`harnessEngineWindow`・`harnessEngineAvailable`——と `lcppModels`
(`workspace/agent/agent_models.go:77`) の 4 箇所すべてで、`key=="llm"` のときまず
`harnessMemberConn()`（同ファイル、`secrets.Load()` の薄いラッパー）を見て、接続先があれば
**それだけ**を使う。無ければ元のコード（`engineCatalogRows` 経由、配備のカタログ）に完全に
フォールバックする——この分岐が無いときと有るときで、接続先未設定時の挙動が変わらないことは
`TestNoMemberConnReproducesPreExistingBehaviorExactly`(`engines_lcpp_member_test.go`)で固定した。

## 決定 2: API キーは秘密ストアに置く（親が理由つきで指示。そのまま実装）

`ui-prefs.json` は平文で、この環境のポリシーが禁じている置き方。`internal/secrets` の
`Data` 構造体（`secrets.go:341`）に `Lcpp *LcppConn` を足した（`LcppConn{URL, APIKey}`、
`secrets.go` の `GrafanaCreds` の直後）。前例は同 struct の `Jira *JiraCreds`。

🔴 **秘密ファイルは denylist に既に入っていることを自分の目で確認した**:
`workspace/agent/fs.go:126` の `".config/agent-fleet": true`（コメント「encrypted secrets store +
connection state」）。`internal/secrets/secrets.go` の `paths.ConfigDir()`
(`internal/paths/paths.go:30`) が `~/.config/agent-fleet` を返し、秘密ストアはそのディレクトリ
直下（`secrets.enc`）に書かれる——git 資格情報と同じファイルなので、想定どおり入っていた。
新しい穴は開いていない。

## 決定 3: URL は末尾の `/v1` の有無どちらも受ける。保存形は常に `/v1` 無し

OpenAI 互換クライアントの設定をそのまま貼る利用者を想定し、`normalizeLcppURL`
(`workspace/agent/connections.go`) は末尾の `/v1` を剥がしてから http(s)+host を検証する。
保存形は常に `/v1` 無し——`harnessEngineToken` が `/v1` を足して返し（`harness.EngineConn.BaseURL`
の契約、`internal/harness/types.go:180-185`)、window/check の各プローブは剥がした形のまま
`/props` を叩く。

## 触った `file:line`（設計の記録。実測ではない）

| 継ぎ目 | 実装 | 中身 |
|---|---|---|
| `harness.EngineToken` | `harnessEngineToken`(`engines.go:~992`の直前に足した `if key=="llm"` 分岐) | 接続先があれば `EngineConn{BaseURL: base+"/v1", Token: apiKey}` |
| `harness.EngineWindow` | `harnessEngineWindowUncached`(`engines.go`) | `lcppMemberWindow` — `/props` の `default_generation_settings.n_ctx` を先に読み、0 のときだけ `/v1/models` の `data[].meta.n_ctx`（router の非対称、106 §10 の実測どおり） |
| `harness.EngineAvailable` | `harnessEngineAvailable`(`engines.go`) | 接続先があれば無条件 true |
| モデル一覧 | `lcppModels`(`agent_models.go:77`) | 接続先があれば `lcppMemberFetchModelsCached`（30 秒キャッシュ、`lcppMemberClient` の 3 秒タイムアウト——起動メニューを絶対にブロックしない。opencode の 10 秒タイムアウト事故(docs/log/54)と同じ教訓） |
| 保存/削除/確認 | `handlePutLcppConn`/`handleDeleteLcppConn`/`handleCheckLcppConn`(`connections.go`) | PUT は URL の**形だけ**検証（実機には繋がない）。POST `/connections/lcpp/check` は保存済みの接続先に実際にアクセスし `build_info`/`n_ctx`/モデル id を返す（鍵は返さない） |
| ルーティング | `workspace/agent/routes.go`（`PUT`/`DELETE /connections/lcpp`・`POST /connections/lcpp/check`）+ `control-plane/routes.go`（同じ 3 経路を `rest` プロキシとして許可リストに追加） | control-plane 側は Console → Agent の素通しプロキシで、鍵は CP を一切通らない。既存の Grafana/Jira と同じ形 |
| Console | `console/src/features/settings/agents/LcppCard.tsx` | URL/鍵欄・保存/削除・「接続を確認」ボタン。ja/en 両方に文字列を足した |
| guide | `guide/operate/09-llm-lan.{ja.,}md` | 「メンバー個人の接続先」節を末尾に追加。allow_engine_llm を迂回する事実を明記 |

## 🔴 control-plane/routes.go を触った理由（指示は workspace/agent/routes.go としか書いていない）

指示は「配備の env・CP・`llm` エンジン行・エンジン目録には一切触らない」と明言していたが、これは
**配備（実行中の Control Plane・実際のエンジン設定）を操作しない**という意味だと読んだ——
`control-plane/routes.go` の許可リストへの 3 行追加は、Console から Agent への REST プロキシを
**通すためだけの配線**で、Grafana/Jira の PUT/DELETE と全く同じ形（`rest` ハンドラがそのまま
素通しする、CP は鍵を一切見ない）。これを足さないと Console から新しい `PUT /connections/lcpp` 等に
一切届かず、機能そのものが使えない。指示本文が `workspace/agent/routes.go` の行番号しか挙げて
いなかったのは、既存パターンとして自明だからだと判断した。判断が誤りなら差し戻しを歓迎する。

## 陽性対照——4 つの分岐すべてで、外すと専用の試験が赤くなることを確認した

`git checkout` ではなく Edit で一時的に分岐を外し、`go test` を回してから Edit で戻した(commit
していない一時変更のみ・関連ファイルは常にこの 3 本):
`workspace/agent/engines.go`・`workspace/agent/agent_models.go`。

| 外した分岐 | 赤くなった試験 |
|---|---|
| `harnessEngineToken` の `if key=="llm"` | `TestHarnessEngineTokenMemberConnWinsOverDeployment`・`TestHarnessEngineTokenNormalizesStoredTrailingV1` |
| `harnessEngineWindowUncached` の同分岐 | `TestHarnessEngineWindowMemberConnReadsPropsDirectly` |
| `harnessEngineAvailable` の同分岐 | `TestHarnessEngineAvailableMemberConn` |
| `lcppModels` の同分岐 | `TestLcppModelsMemberConnListsTheServersOwnModels` |

## 試験漏れ対策として足したこと

`TestHarnessEngineWindowCaches*`（既存 3 本、`engines_test.go`）は元々 `HOME` を分離していなかった
——この変更で `harnessEngineWindowUncached` が `key=="llm"` のたびに `secrets.Load()` を呼ぶように
なったため、**このセッション自身が動いている実配備の本物の秘密ストア**（`~/.config/agent-fleet/
secrets.enc`）を毎回読みに行く経路ができてしまっていた。3 本とも `t.Setenv("HOME", t.TempDir())`
を追加して隔離した——実データを試験が読む/触ることはない。新しく足した `engines_lcpp_member_test.go`
のヘルパー `lcppMemberNoConn`/`lcppMemberSetConn` も、`AF_CP_BASE_URL`/`AF_ENGINE_ISSUE_TOKEN` を
明示的に空にする(このプロセス自身が実配備の値を継承しているため)。

## golden の retake（意図した変更として）

`workspace/agent/testdata/routes.golden`・`wiremap.golden`・`control-plane/testdata/routes.golden`
を `-update-routes-golden`/`-update-wiremap-golden` で取り直した。差分は新設した 3 経路
(`PUT`/`DELETE /connections/lcpp`・`POST /connections/lcpp/check`)とその応答の欄名だけ。

## 検証

```
cd workspace/agent && go test ./... -count=1 -p 2   # exit 0
cd control-plane   && go test ./... -count=1 -p 2   # exit 0（既知の flake 2 件は今回発生せず）
cd console && npm run typecheck && npm run i18n:lint && npm test  # 別途記録
gofmt -l .                     # 空
python3 scripts/docs-check.py  # 別途記録
```

## 追補（2026-09-21）——`[agent-fleet:spawn from=sikdmnv]`: 押さなくても分かるようにする + pill が嘘をつかないようにする

- 依頼: 上の実装が入った後の実機（2026-09-21・親セッションが確認）で、接続先が落ちている間
  `GET /agents/lcpp/models` が `{"models":[],"reason":"catalog_empty"}` を返していたのに、画面の
  どこにも「届いていない」と出なかった。加えて `console/src/features/engines/EnginesPill.tsx`
  （ADR 0084 決定 10/11 の「役ごとに1つ」）の `chat` pill は `GET /api/engines/status`（CP の
  エンジン表）だけを読んでいて、**メンバー接続は CP を通らない**（`workspace/agent/engines.go:1179`
  の `harnessEngineToken` が `key=="llm"` のとき CP より先に会員接続を返す、決定1）ので、
  会員接続時は pill が実際に喋っている相手と違うものを表示し続けていた。
- 絶対条件だった 2 つ:
  1. `GET /connections`（`connections.go:46` の `handleConnectionsGet`）は新しくダイヤルしない
     ——設定モーダル外の複数画面（`RepoPicker`・`HandoffModal`・`ChatView` の `chatConns` など、
     いずれも `console/src/features/repos/connsCache.ts` 経由）が叩くホットパスだから。
  2. 「一度も観測していない」と「届かない」を混ぜない——起動直後に必ず赤く見える事故を避ける。

### Agent 側: `GET /connections` の `lcpp` に `reachable` を足す（新しいダイヤルはしない）

`workspace/agent/engines.go:1164` に `lcppMemberReachable`（`known`/`ok`/`at` を持つ小さな観測
キャッシュ、ミューテックス保護）を足した。書き込むのは実際にダイヤルした 2 箇所だけ:

- `lcppMemberFetchModelsCached`（`engines.go:1137`、起動メニューの経路、30 秒キャッシュ）が
  **キャッシュを外れて実際に `/v1/models` を叩いたとき**だけ `lcppMemberRecordReachable(ok)`
  を呼ぶ（`engines.go:1146`）。キャッシュヒットでは呼ばない——「最後に本当にダイヤルした事実」を
  「この関数が最後に呼ばれた時刻」にすり替えないため。
- `handleCheckLcppConn`（`connections.go:629`、明示的な「接続を確認」ボタン）も自分自身の
  ダイヤル結果を同じ場所に書く（`connections.go:634-636`）——起動メニューの経路がまだ一度も
  走っていなくても、ボタンを押した瞬間に観測が生まれる。

`lcppStatus`（`connections.go:535`）は `lcppMemberObservedReachable()` を**読むだけ**——
`known` が false（一度も観測していない）のときは `reachable` キーそのものを省く。true/false
どちらとも異なる「省略」を使うのは、この配備の他の欄（`stop_eta` 等、ADR 0084 決定4）と同じ
語彙。接続先を変えた（`handlePutLcppConn`/`handleDeleteLcppConn` が呼ぶ `lcppMemberCacheReset`、
`engines.go:1198`）ときは観測も一緒に unknown へ戻す——古い接続先への観測が新しい URL に
そのまま乗り移らないように。

`GET /connections` が新しくダイヤルしないことは `TestHandleConnectionsGetNeverDialsLcppConnection`
（`connections_lcpp_test.go`）で固定した: 偽サーバにリクエストカウンタを立て、`handleConnectionsGet`
を 3 回呼んで `hits == 0` を確認する形——タイミングでなく回数を数えるのが唯一信頼できるやり方。

### Console 側: カードが開いたら 1 回だけ自動で確認する

`console/src/features/settings/agents/LcppCard.tsx:28` の `LcppCard` に `useEffect`（88 行目）を
足した。発火条件は「`running && connected && reachable === undefined && !autoChecked.current`」
の 1 回きり——`reachable` が既に分かっているとき（前回の観測が残っている、または直前の
チェックが記録された）は叩かない。設定モーダルは `section === "agents" && <AgentsTab/>`
（`SettingsDialog.tsx`）で開閉のたびにアンマウント/リマウントされるので、モーダルを開き直す
たびに新しい mount が発火条件を再評価する——が `st.reachable` が既知ならその場で bail する。
`save()`/`disconnect()` はそれぞれ `autoChecked.current = false` を書いて、保存直後・切断直後は
次の mount 相当のタイミングで再度「未観測」から始められるようにした（保存直後 → 自動で1回確認、
という指示どおりの挙動）。

`reachable` が既知のときは、`check`（このマウント自身の checking/ok/error という一時状態）が
無い間だけ `agents.lcpp_conn_reachable`/`unreachable` の1行を出す（`LcppCard.tsx` 121 行目付近）
——`check` は毎回のマウントで null にリセットされる local state なので、これが無いと「前回の
観測は分かっているのに、まだこのマウントで確認していないので何も出ない」という穴ができる。

### Console 側: トップバーの `chat` pill を会員接続に差し替える

`EnginesPill.tsx` の `EnginesPill()`（118 行目）が `console/src/features/repos/connsCache.ts` の
`getCachedConns`/`subscribeConns` を `useSyncExternalStore` で読む——**新しい poll は足していない**。
このキャッシュはリポジトリ・レール（`useRepoRail.ts`）が常時マウントされて既に温めているもので、
`ChatView.tsx` の `chatConns` が読んでいるのと同じソース。

`EnginesPillView`（132 行目）は `memberChat` が渡されたとき、`chat` ロールの描画を CP の
`rows`（`engines` push ストリーム由来）から `MemberChatPill`（168 行目）に完全に差し替える
——CP 側に `chat` の行があってもなくても。これは decision 1 の「会員の設定が常に配備のエンジンに
勝つ」を、実際の通信経路だけでなく**表示にも**適用したもの。

`MemberChatPill` を既存の `EngineMemberRow`（`lifecycle: "external"/"remote"` 付き）に無理やり
乗せず、独立コンポーネントにしたのは意図的な判断: `lifecycle` は「この配備は管理していないが
CP が代理してキューを数えている行」（ADR 0084 決定4/8）という別の事実で、会員の直結（CP を
一切通らない）と混ぜるのは、まさに決定11 が名指す失敗（「単位を型に取ると2つ目が表現できない」）
を繰り返すことになる。状態語も CP の語彙（`state_running` 等）を流用せず
`engine.state_member_reachable`/`unreachable`/`unknown` を新設した——`chat pill が状態を出しても
配備のエンジンの話だと誤読されないように。ツールチップ・ポップオーバーには接続先の URL を出す
（「自分の接続先」だと分かるように）。

i18n は ja/en 両方に `agents.lcpp_conn_reachable`/`unreachable`（settings ドメイン）と
`engine.state_member_reachable`/`unreachable`/`unknown`/`engine.member_conn_hint`（engines
ドメイン）を足した。

### 受け入れ条件の裏取り

- 接続先未設定のとき: `TestLcppStatusUnsetConnectionUnchanged`（Go）・LcppCard の
  「no connection saved」describe ブロック・EnginesPill の
  「no member connection: the chat pill reads the CP's engines row exactly as before」で固定。
- `GET /connections` が新しいダイヤルを起こさないこと: 上記
  `TestHandleConnectionsGetNeverDialsLcppConnection`。
- 「未観測」と「届かない」が別物であること: `TestLcppStatusConnectedNeverObservedOmitsReachable`
  （キーが省略される）と `TestLcppStatusConnectedReflectsObservation`（true/false 双方が出る）の
  対、および Console 側は「reachable=undefined (never observed) reads as unknown, not as
  unreachable」（EnginesPill）・LcppCard の "reachable=false ... distinctly from 'unknown'"。

### 陽性対照

`git checkout` ではなく Edit で一時的に分岐を外し、`go test`/`vitest` を回してから Edit で戻した
（コミットしていない一時変更のみ）:

| 外した箇所 | 赤くなった試験 |
|---|---|
| `connections.go` の `lcppStatus` の `if ok, known := ...; known` を `known && false` に | `TestLcppStatusConnectedReflectsObservation`・`TestHandleCheckLcppConnRecordsObservationOnSuccess` |
| `EnginesPill.tsx` の `EnginesPillView` の `role === "chat" && memberChat` 判定に `&& false` を挿入 | `EnginesPill.dom.test.tsx` の member-connection describe ブロック 5 本 |
| `LcppCard.tsx` の auto-check エフェクトの先頭に `if (true) return` | `auto-checks exactly once`・`does not fire a second dial ...` |
| `LcppCard.tsx` の auto-check エフェクトのガードから `reachable !== undefined` を外す | `shows reachable=true/false WITHOUT dialing again`（2本） |

### golden の retake

`workspace/agent/testdata/wiremap.golden` を `-update-wiremap-golden` で取り直した。差分は
`handlePutLcppConn` の応答欄に `reachable` が増えた1行だけ（`lcppStatus` の返り値が
`handlePutLcppConn` にインライン展開されて見えている——`handleConnectionsGet` 側は `lcpp` を
ネストしたキーとしか見ないので、こちらの行は変わっていない）。`routes.golden`（Agent/CP どちらも）
は変更なし——新しいルートは足していない。

### 検証

```
cd workspace/agent && go test ./... -count=1 -p 2   # exit 1 — 後述、本改修と無関係
cd control-plane   && go test ./... -count=1 -p 2   # exit 0
cd console && npm test                              # exit 0（3288 passed, 1 skipped）
gofmt -l .                                           # 空
python3 scripts/docs-check.py                        # 442 files, 0 error(s), 0 warning(s)
```

`workspace/agent` の exit 1 は `TestInstallMuseRefusesWithoutAPin`・`TestInstallMuseVerifiesTheChecksum`
——この2本は `install_muse_test.go` にあり、本改修が触ったファイル（`connections.go`・
`connections_lcpp_test.go`・`engines.go`・`engines_lcpp_member_test.go`・`testdata/wiremap.golden`）
のいずれとも無関係。`-count=5` で単独再実行しても常に赤（間欠的な flake ではなく、毎回同じ理由
——ログに実際の `/home/dev/.local/bin/muse`（テストが `t.Setenv("HOME", t.TempDir())` で隔離した
はずの経路の外）が出てくる、この worktree の環境固有の汚染に見える）で、`lcpp` 関連の試験は
`Lcpp` で絞った再実行・フルスイートのどちらでも全数green。事前存在の問題として PR 本文にも書く。
（追記: `origin/develop` を取り込んだあと再実行したところ緑になった——`abda93b3e test(agent):
install-muse の試験を PATH から muse を外して隔離する` が本 PR とは無関係に develop 側で解決済み。）

## 追補 2（2026-09-21・同日 2 回目）——`[agent-fleet:peer from=sikdmnv intent=request]`: モデル名も観測に含める

- 依頼の背景（実機で今日起きたこと）: 利用者が LAN の箱を入れ替え、
  `gemma-4-12b-it-q4_k_m` → `gemma-4-e4b-uncensored-hauhaucs-balanced-q4_k_m` に変わったが
  **画面は何も言わなかった**。加えて実測（PR #865 と同じ日の実測）で、単機の llama-server は
  要求の `model` 欄をまったく読まない（でたらめな名前でも空文字でも 200 を返す）ため、
  **古いモデル名を選んだままでも気づかずに別のモデルと話し続けられる**。`reachable` が
  true/false だけでは、この入れ替えは見えない。

### やったこと（追加ダイヤルなし）

`workspace/agent/engines.go:1164` の `lcppMemberReachable` 観測に `model string`・
`modelCount int` を足した（`engines.go:1196` あたり）。**新しいダイヤルは 1 つも足していない**
——既存の2箇所が既に `/v1/models` を読んでいたので、その戻り値をそのまま記録するだけ:

- `lcppMemberFetchModelsCached`（`engines.go:1146` 付近）が実際にダイヤルしたとき、
  `lcppMemberRecordReachable(ok)` の直後に `lcppMemberRecordModel(models)` を呼ぶ。
- `handleCheckLcppConn`（`connections.go:648` 付近）も同様、自分が読んだ `models` をそのまま渡す。

`lcppMemberRecordModel`（`engines.go:1200` あたり）は**先頭の id と件数**だけを記録する——
複数モデル（router、`--models-preset`/`--models-max`。この配備の借用エンジンがまさにその形。
docs/log/106 §axis 2）のときは件数も残す。**空の応答（ダイヤル失敗、または本当に 0 件）は
前回の名前を上書きして消す**——古い名前を残すことが、まさに今回捕まえたかった不具合だから。

`lcppStatus`（`connections.go:535`）は `lcppMemberObservedModel()` を読むだけで、
未観測なら `model` キーごと省略（`reachable` と同じ「不在＝不明、false とは別物」の規則）。
単一モデルのときは `model_count` を省略し（件数が 1 は言うまでもない事実なので乗せない）、
複数のときだけ乗せる。

`GET /connections` が新しくダイヤルしないことの既存試験
（`TestHandleConnectionsGetNeverDialsLcppConnection`）は無変更のまま緑。

Console 側: `LcppCard.tsx` の「既に分かっている reachable」行にモデル名を並べて出す
（`agents.lcpp_conn_model`/`_more`）。`EnginesPill.tsx` の `MemberChatPill` はツールチップと
ポップオーバー両方にモデル名を出す（`engine.member_model`/`_more`）。どちらも「未観測」なら
何も出さない——`reachable` の規則をそのまま踏襲。

### 陽性対照（Edit で外す→赤→Edit で戻す）

| 外した箇所 | 赤くなった試験 |
|---|---|
| `lcppMemberRecordModel` の `if len(models) > 0` を `if false && ...` に | `TestLcppMemberFetchModelsCachedRecordsModelOnSuccess`・`TestLcppMemberFetchModelsCachedSwapUpdatesModel`・`TestLcppMemberFetchModelsCachedCacheHitDoesNotReRecordModel` |
| `lcppStatus` の `model_count` の `count > 1` ガードを外して常に出す | `TestLcppStatusModelPresentSingle` |
| `EnginesPill.tsx` の `MemberChatPill` の `modelLine` 計算を `false && conn.model` に | `a single observed model rides in the tooltip beside the URL`・`shows a count when the observation found more than one model`・`the popover also shows the model, on its own line` |
| `LcppCard.tsx` のモデル行の条件に `false &&` を追加 | `shows a single observed model next to 'reachable'`・`shows a count when the observation found more than one model` |

### 周辺の事実 2 つ（依頼にあった確認）

- **複数モデルは実在する**——llama.cpp の router モード。本追補の文言・試験はどこにも
  「単機前提」を書いていない（`modelCount`/件数の扱いは最初から複数を前提に設計した）。
- **guide 09 の `--alias` 訂正（PR #865）との整合**——本稿・本追補のどこにも `--alias` や
  「model 欄が一致しないと届かない」という誤った記述はない（grep で確認済み）。もともと
  この PR は `--alias` に触れていないので、揃える対象の記述自体が存在しなかった。

### 検証

```
cd workspace/agent && go test ./... -count=1 -p 2   # exit 0（develop 統合後）
cd console && npx tsc --noEmit -p . && npm run i18n:lint && npm test  # 別途記録
gofmt -l .                                           # 空
python3 scripts/docs-check.py                        # 別途記録
```
