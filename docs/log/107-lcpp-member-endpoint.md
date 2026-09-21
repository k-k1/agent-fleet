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
