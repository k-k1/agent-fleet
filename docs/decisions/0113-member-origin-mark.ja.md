# 0113. Agent が検証できるメンバー由来の印：CP がメンバーの中継書き込みに署名し、Agent は公開鍵だけを持つ

[English](0113-member-origin-mark.md) | 日本語

- Status: **accepted**（2026-10-12）。#1997 で実装し、最初のリリースは v0.31.0。#2014 と #2076（v0.31.0）、および v0.32.0 の 7 ルート
  （設定の書き込みとメモリレビューの実行は #2348、提案の決定は #2353）で拡張した：CP が中継する 17 ルート（`control-plane/member_origin.go` の `memberOriginRoutes`。bundle の取り込みは本文のハッシュで署名）と、
  CP 自身が行う引き継ぎの push に署名し、Agent が検証して（`workspace/agent/internal/memberorigin`）、メンバー専用の
  書き込みに印を必須とした。`restore`、`claude-import`、`muse-import`、`claude-export` は決定により開いたまま
  （2026-10-14 の注）。実際の CP と Agent で印を動かしているのは `e2e/handoff_test.go`（引き継ぎ push）と、
  デプロイ済みの Console で Workspace の再起動後にメンバーが確かめた作成・編集・ピン留め（#1997）。
  **本物のフリートで未確認：** 重なって動く Fargate の 2 タスク（#2018）、ブラウザからの大きな bundle のアップロード、
  ほかの署名ルートの否定側の対照。両側は共通のワイヤーベクタでテストしている。
- Tracking: #1997。Follow-ups: #2018（開いている：重なって動く Fargate の 2 タスク間での再生）。#2014 と #2076 はクローズ済み。関連：[0108](0108-af-owned-agent-memory.ja.md) 決定 4・8（スイッチと、承認手順が境界に
  ならない理由）／[0102](0102-aws-login-through-the-console.ja.md)（ルートが呼び手を区別できないときに結果を
  人の操作に結び付ける）／[07-security §7.2・§7.5](../build/07-security.ja.md)。

## 背景

Agent のすべてのルートは Workspace の `AGENT_TOKEN` を受け付け、すべてのセッションのシェルがそれを持つ
（af MCP サーバーに必要なため）。CP は中継する要求に `X-AF-Relay: cp` を付けるが、これはログ用のヒントで、
誰でも付けられる。したがってメンバーの Console 専用のルートは、メンバーとセッションを区別できない。

AF メモリではこれが 3 か所で問題になる。`entries/pin` と `entries/revert`（忘れさせるも含む）は作者を
ルートによって「メンバー」と記録し、`agentMemory` スイッチの対象外である（スイッチはセッションに許すことを
決めるものだから）。メンバーによる作成・編集のルートは同じことを任意の新しいテキストで行えるため、スイッチが
オフでもセッションがメンバーとしてメモリを書けてしまう。そのため #1985 から外した。

答えを縛る制約：セッションは Agent と**同じ uid** で動き、ファイルシステム・プロセステーブル・ネットワーク
名前空間を共有する。Agent が読めるもの（環境変数、`/proc/<pid>/environ`、ファイル、渡された鍵）はセッション
も読める。Agent に配った共有 HMAC 鍵は、セッションも持っている鍵である。

## 決定

1. **印は CP が作る Ed25519 署名で、Agent は公開鍵だけを持つ。** 検証に秘密は要らないので、セッションが
   Agent と同じものを読めても構わない。秘密鍵は CP のプロセスから出ない。
2. **鍵は保存せず導出する。** 版 *N* のシードは `HMAC-SHA256(トークン署名マスター,
   "af-member-origin-sign/vN")`。メンバーシップごとのブリッジトークンと同じマスター（`tokenSignMaster`：
   `AF_MASTER_KEY` のダイジェスト、またはデプロイごとの乱数の開発用マスター）。新たに用意・バックアップする
   ものはなく、ある版のシードが漏れても次の版はわからない。
3. **配布は起動時の環境変数を再利用する。** `workspaceExtraEnv` がコンテナ起動のたびに
   `AF_MEMBER_ORIGIN_KEYS=v<N>:<公開鍵>,v<N+1>:<公開鍵>`、`AF_WORKSPACE_ID`、`AF_MEMBERSHIP_ID` を注入する。
   どれも秘密ではない。Agent は一度だけ読み、セッションは動いているプロセスの環境を変えられない。
4. **署名する内容。** ヘッダー `X-AF-Member-Origin: <kid>.<base64url(claims)>.<base64url(署名)>`。署名対象は
   `"af-member-origin/v1\n" + claims` で、claims は送ったままの JSON バイト列（正規化しない）：メソッド、
   Agent 側のパス、生のクエリ、本文の SHA-256、Workspace ID、メンバーシップ ID、CP の identity ID、`iat`、
   `exp`（`iat` の 60 秒後）、128 ビットのノンス。Agent は、受け取った要求との不一致、2 分を超える有効期間、
   自分の時計より 30 秒以上先の `iat`、期限切れ、既に見たノンスを拒否する。
5. **リプレイキャッシュと再起動の柵。** Agent はノンスを `exp` まで最大 4,096 件保持する。満杯で期限切れが
   なければ、覚えられない印を受け入れずに 503 で拒否する。満杯のキャッシュをリプレイの窓にしてはならない。
   キャッシュはプロセス内にあり再起動で空になる。前のプロセスが受け入れた印の `iat` は、そのプロセスの時計より
   最大 30 秒（受け入れる時計のずれ）先、つまりこのプロセスの起動から最大 30 秒先である。そこで Agent は
   `iat` が起動 + 31 秒（1 秒は起動時刻の切り捨て分）より前の印をすべて拒否する。CP の時計がどうであれ成り立ち、
   前提は Agent 自身の時計が再起動をまたいで戻らないことだけ。代償として、Agent の起動から約 31 秒（CP の時計が
   遅れていればその分長く）はメンバーの書き込みが 503（「1 分ほどして再試行」）になる。ノンスの永続化は採らない：
   そのファイルは、柵で防ぐ相手のセッションが書き換えられる。
6. **対象のルートと、Agent に届けないもの。** CP が署名するのは
   `POST /api/agents/memory/entries/{pin,revert,member-save}`（`memberOriginRoutes`）だけで、Workspace が
   呼び手自身のメンバーシップのものであるときに限る。Agent へのすべての中継 ── REST、SSE、プレビューの中継
   （HTTP・SSE・WebSocket、パス形式とホスト形式）── で、ブラウザが送った `X-AF-Member-Origin` を捨てる。
   REST と SSE では、プレビューの中継が既にしていたのと同じく、すべての Cookie（Agent のルートは Cookie を
   読まない。Console の `af_session` も含む）とゲートウェイの identity・アクセストークンのヘッダーも捨てる。
   メンバーのログイン Cookie が Workspace に届けば、セッションは CP に何でも署名させられる。署名の前に CP は、
   別のページがメンバーの Cookie 付きでブラウザーに送らせられる要求も拒否する。自前のホストで動くプレビュー
   アプリは same-site なので `SameSite=Lax` でも Cookie は送られ、`text/plain` の no-cors POST はプリフライトが
   要らない。そこで本文は `application/json` に限り（それ以外は 415。クロスオリジンのページは、CP が決して
   応答しないプリフライトなしにはこれを送れない）、`Sec-Fetch-Site` があれば `same-origin`、`Origin` があれば
   Console 自身のホストか `PUBLIC_BASE_URL` のホスト（WebSocket と同じ規則）でなければならない。どちらの
   ヘッダーもない呼び手はブラウザーではなく、ログイン Cookie そのものを要する。Agent も、有効な印があっても
   JSON 以外の本文は拒否する。Agent は同じ
   3 ルートを `memberorigin.Require` で包み、ハンドラーも要求コンテキストに検証済みの claims がなければ拒否
   するので、ミドルウェアなしで登録されたルートは閉じたままになる。
7. **署名のない pin と revert は拒否する。** 互換の猶予は設けない：Console はこれらのルートに CP 経由でしか
   届かず、CP が署名するので、正当な呼び手が署名なしで送ることはない。CP より古い Agent はヘッダーを無視する
   （pin と revert は従来どおり、`member-save` は 404）。Agent より古い CP は印を付けず、Agent が拒否する ──
   機能はオフになり、開きはしない。
8. **作者は検証できたときだけ ── この 3 ルートでは。** pin・revert・`member-save` は、有効な印の後ろでだけ
   作者 `member` / `console` を書く。`member` を記録するすべてのルートがそうなったわけではない：claude と muse
   の取り込み（`agent_memory_claude_import.go`、`agent_memory_muse_import.go`）も `member` としてコミットし、
   まだ署名の対象外なので、取り込みの作者は #2014 で印の対象になるまで、検証された人ではなく通ったルートを
   表す。`member-save` はツールと同じ保存（版の確認、秘密情報のスキャン、上限）を通り、pin・revert と同じく
   スイッチの対象外で、プロジェクトを新規に始めず、プロジェクトの `project.json` はセッションが書いたままに
   する。CP は `memory.entry.save` として、ID 形式のプロジェクトのヒントだけで監査する。
9. **ローテーション。** CP の環境変数 `AF_MEMBER_ORIGIN_KEY_VERSION`（既定 1）で署名鍵を選ぶ。Workspace は
   最後に起動したときの版とその次の版の公開鍵を持つ。したがって版を 1 つ上げても、直前の版で起動した
   Workspace は検証を続けられる。再起動前に 2 回目を上げると続けられない（v1/v2 で起動したものは v3 を検証
   できない）。2 つ以上上げる、または `AF_MASTER_KEY` を替えた場合も同じで、その Workspace は再起動まで
   メンバーの書き込みを拒否する。古い秘密鍵は二度と使われない。`0` で署名を止める（何も注入せず、メンバーの
   書き込みはすべて拒否）。

## 脅威モデル

- **対象**：`AGENT_TOKEN` を持ち、任意のヘッダーを付けられ、Agent の環境変数とファイルを読め、Workspace 内で
  流れる印を盗み見られるセッション（とそこで動くもの）。印を作れない（秘密鍵がない）、別のルート・クエリ・
  本文・Workspace・メンバーに流用できない（すべて署名対象）、リプレイできない（再起動をまたぐ場合は決定 5 の柵。ノンスと期限）。
- **印がしないこと。** 印は Agent 自身のルートが何を受け付けるかを決めるだけで、ストアを改ざん不能にはしない。
  シェルを持つセッションは、所有者の uid としてストアのファイルと履歴を直接書け、次の起動で実行される
  `~/.local/bin` のバイナリも置き換えられる。これはどの作者欄についても以前から成り立っていた（0108 の決定 8
  が同じ限界を記録している）し、Workspace 内の仕組みでは塞げない。この Workspace では `ptrace_scope=1` を
  確認しており、セッションは Agent のメモリを読めないが、そもそもそこに秘密鍵はない。
- **CP の認証と同じ強さ。** CP はメンバーと解決した相手のために署名する。`AUTH=dev` ではすべての呼び手を
  信頼するので印は何も足さない。`oauth` ではメンバーのセッション Cookie が要り、中継はそれを Workspace に届けない（決定 6）。
- **1 つの Workspace の Agent が同時に 2 つ。** 再起動の柵が守るのは再起動したプロセスで、空のキャッシュを
  持つ同時に生きている 2 つ目ではない。`ecs-ec2` では起こらない：サービスは `MaximumPercent` 100 /
  `MinimumHealthyPercent` 0 で動き、古いタスクが止まってから新しいタスクが始まる。Fargate（`ecs`）のサービスは
  ECS 既定のローリングデプロイ（200 / 100）のままなので、稼働中の Workspace を強制再デプロイすると 2 つの
  タスクがしばらく重なりうる。一方が受け入れた印をもう一方も受け入れうるが、届けばの話である：Workspace の
  セキュリティグループは Agent のポートを CP のセキュリティグループからだけ許す（07-security §7.2）ので、
  一方のタスクのセッションはもう一方の Agent に送れず、CP の中継は渡された印を捨てる。そこでの歯止めは
  キャッシュではなくネットワーク分離で、実際の Fargate では未検証。
- **パス形式のプレビューは Console のオリジンにある。** `/preview/{port}/` で配信されるアプリは Console と
  同じオリジンで動くので、`Origin` でも `Sec-Fetch-Site` でも区別できず、この 3 ルートに限らず Console の API を
  すべてメンバーとして呼べる。[0062](0062-preview-subdomain.ja.md) の決定 7 がプレビューを専用ホストに移した
  理由がこれで、上の対策が守るのはホスト形式で、パス形式は守らない。
- **対象外**：侵害された CP（マスターを持つ）と、盗まれたマスター鍵。

## 失敗時の振る舞い

| 状況 | 結果 |
|---|---|
| この変更より前に、または署名オフの CP で起動した Workspace | 再起動まで 3 ルートが 503 `member_origin_unavailable` |
| 印がない、`AGENT_TOKEN` / `X-AF-Relay` だけ | 403 `member_origin_required` |
| 再起動の柵の内側で発行された印（Agent 起動から約 31 秒） | 503 `member_origin_unavailable`。1 分後の再試行は通る |
| 別の要求・Workspace・メンバー・鍵の印、期限切れ、リプレイ | 403 `member_origin_invalid` |
| 本文が CP のバッファ 2 MiB を超える | CP で 413。Agent には何も届かない |
| 他のメンバーの Workspace への要求 | CP で 403。何も署名しない |
| 本文が `application/json` でない、`Sec-Fetch-Site` が `same-origin` でない、`Origin` が Console でない | CP で 415 / 403。何も署名しない |

## 却下した案

- **Agent の環境変数やファイルに置く共有 HMAC 鍵。** すべてのセッションが読める（同じ uid）ので偽造できる。
- **Agent の鍵を別プロセス・別 uid に置く。** Workspace にそのようなプロセスはなく、root のヘルパーを足すのは
  root なしのイメージに反する。
- **Agent が CP に確認する要求ごとのトークン。** メンバーの書き込みごとに CP への往復が要り、セッションも自分の
  資格情報で届く CP のエンドポイントが要る。署名はどちらも要らない。
- **pin と revert の署名なし猶予期間。** 正当な呼び手はすべて CP で、CP は署名する。

## 追記 2026-10-13: そのほかの Console 専用メモリ書き込みをルートごとに判定（#2014）

決定 6 は、残りの Console 専用メモリ書き込みを署名なしのまま残した。各ルートの呼び出し元（Console、`mcpx` の
アシスタント MCP ツール、`af-memory` CLI、スクリプト、Agent 自身の経路）をすべて洗い出した。メモリのスイッチに
連動する Agent の自動インポート／書き戻し（#1972）は未実装で、実装されればこれらのルートではなく
インポーターをプロセス内で呼ぶ。

| ルート | 呼び出し元 | 判定 |
|---|---|---|
| `POST /agents/memory/snapshots` | Console のみ（自動スナップショットのループはプロセス内で `memorySnapshot` を呼ぶ） | **gate する**（`memberOriginRoutes` と `memberorigin.Require`） |
| `POST /agents/memory/import/apply` | Console のみ（`memoryTransfer.tsx`） | **gate する** |
| `POST /agents/memory/restore` | Console と、アシスタントの MCP ツール `restore_memory_snapshot`（`mcp_stdio.go`） | 開けたまま: 正当なエージェント側の呼び手がいる。gate するなら Console 専用の別パスが要る |
| `POST /agents/memory/import` | Console（multipart アップロード） | 開けたまま: CP が署名するのは 2 MiB 以下の `application/json` 本文だけで、印の対象を変えるのは今回の範囲外 |
| `POST /agents/memory/claude-import`、`/muse-import` | Console と、端末での利用が文書化されている `af-memory import` / `muse-import`（`afmemory.go`） | 開けたまま: `af-memory` はセッションからの呼び出し。書き込みを外すかは製品判断 |
| `POST /agents/memory/claude-export` | Console と `af-memory export`（`export.go`） | 開けたまま（同じ理由） |

したがってインポートについては決定 8 がそのまま成り立つ: インポートの作者 `member` は、来たルートを示すだけで
検証済みの人ではない。gate したルートは CP と Agent で同一に並ぶ。
`TestMemberOriginRoutesAgreeWithTheControlPlane`（Agent モジュール）は CP の表を構文木から読み、実際の mux を
突いて確かめるので、片側だけの変更は失敗する。

`import/apply` が読み込む本文は、保存の上限ではなく 2 MiB（CP が署名する本文の上限）まで。本文には選んだ slug が
すべて並び、大きなバンドルでは保存の上限を超え得るため。

Follow-ups: #2076

## 追記 2026-10-14: import は本文ハッシュで印を付ける。restore と CLI 系ルートは開けたまま（#2076）

- **`POST /agents/memory/import` を gate した。** アップロードは multipart で最大 64 MiB
  （`AF_MEMORY_IMPORT_MAX`）あり、CP が JSON の印のために読み込む 2 MiB を大きく超える。そこで印は従来どおり本文の
  SHA-256 に署名するが、ハッシュはストリーム中に計算する。CP はアップロードをファイルに退避しながらハッシュし、署名して
  そのファイルを中継する（上限 130 MiB、超えれば 413）。Agent は受け取った本文を退避してハッシュし、そのハッシュで印を検証してから
  ハンドラにファイルを渡す（`memberorigin.RequireUpload`）。別オリジンのページは multipart の POST をプリフライトなしで送れるため、
  決定 6 の JSON Content-Type による防御は使えない。代わりに CP はカスタムヘッダ `X-AF-Console`（CP が応答しないプリフライトを
  強制する）と、従来どおりの `Sec-Fetch-Site` / `Origin` の検査を要求する。印の暗号とクレームは変えていない。
- **`POST /agents/memory/restore` は開けたままで、印では守れない。** 正当なエージェント側の呼び手は、アシスタントの
  `restore_memory_snapshot` ツールである。これは別プロセスの af-mcp で動き、`AGENT_TOKEN` でループバック経由に Agent を呼ぶ。
  Agent はそのプロセスをセッションのシェルと区別できず、ツールが届くルートはセッションにも届く。トークンだけの別ルートを
  作っても穴が移るだけである。形だけの gate より、ユーザー承認つきの復元フローを残すことを選んだ。
- **`claude-import`、`muse-import`、`claude-export` は開けたまま**（オーナー判断、#2076）: `af-memory` CLI がセッションから
  呼ぶ用途が正式にあるため。したがってこれらのルートでは決定 8 がそのまま成り立つ（作者 `member` は来たルートを示すだけ）。

上の表は本追記で更新される: `import` は **gate する**。`restore` と CLI 系の 3 ルートは、ここに記した理由で開けたまま。

上の追記へのレビュー反映（同日）:

- アップロードの印の有効期間は、リクエスト開始時ではなく CP が本文を読み終えた時点から数える。遅いアップロードが
  署名時点で既に期限切れ寸前になることを避けるため。
- CP の退避は上限付き（同時 4 本、メンバーシップごとに 1 本、超過は 429）。退避先が CP の容量制限つき一時ボリュームのため。
- **ハッシュ方式の限界。** Agent の退避ファイルと import ハンドラ自身の一時コピーは、Agent と同じ uid の任意のプロセスが
  書き換えられる。印が決めるのは誰が import を始められるかであり、検証から読み込みまでの間の同一 uid による書き換えから
  取り込まれるバイトを守るものではない。ストアへの直接書き込みと同じ限界（脅威モデル「印がしないこと」）であり、新しい能力ではない。
  開いた／unlink 済みの記述子も `/proc/<pid>/fd` から開けるため、コードでは塞がない。
