# セッション共有

同一テナントの別ユーザーへ、Agent セッションの会話を共有する。共有先が所有者の
Workspace やファイル API へ直接到達する経路は作らず、Control Plane（CP）が毎回 ACL を
評価して、起動中の所有者 Workspace Agent から必要な会話 DTO だけを中継する。

## 1. 共有単位

共有規則は動的で、作成後に同じ範囲へ追加されたセッションにも適用される。

| scope | key | 対象 |
|---|---|---|
| `session` | セッション名 | 指定した1セッション |
| `repo` | `workingCopyId` | プロジェクト全体＝ベース作業コピー直下＋その配下 linked worktree の全セッション |
| `worktree` | `workingCopyId` | 指定した linked worktree の全セッション |

`repo` を「ベース直下だけ」にすると実質何も共有されない: 所有者の作業は Console が
セッションごとに切る worktree 側で進み、ベース直下には古いセッションしか残らないため。
そこで `repo` はプロジェクト単位（ベース＋その worktree）を覆う。判定は worktree の
フォルダ名ではなく親作業コピーの `workingCopyId`（catalog の `parent_working_copy_id`）で
行う — 名前は付け替えられるが、この ID は作業コピーの世代に固定されるので、共有が別物へ
乗り移らない。共有先を worktree 1つに絞りたい場合は `worktree` 規則を使う。

`workingCopyId` は表示名やパスではない。Agent が Git/SVN の管理領域へランダム ID を
作成時に保存する。同じ作業コピーでは不変で、削除後に同名フォルダを作り直しても別 ID に
なるため、古い規則が新しい作業コピーへ復活しない。管理領域がread-only／破損などで永続
random markerを作成・読取できない作業コピーは `workingCopyId` を空にして、repo/worktree
共有の対象にしない（device/inode等の再利用可能なfallbackは使わない）。作業コピーが消えたことを live inventory
で確認した時点で、対応する repo/worktree 規則も削除する。

セッション inventory は transcript 対応 kind のみを含む。shell/SSM は共有できない。
セッション自体が削除されて inventory から消えた場合は catalog、単体共有規則、未処理提案を
削除する。

アーカイブ済みは共有先に出さない。所有者が畳んだ会話は「共有したいもの」ではなく、出し続けると
所有者が消したはずの古いセッションが延々と残っているように見える。ただし共有規則も catalog 行も
消さない（＝アーカイブは共有解除ではない）: 所有者が復元すれば同じ規則のまま再び見えるようにする。
隠す対象は受信側の一覧と、catalog ID 直リンクからの閲覧・RW 提案の両方で、直リンクは
`owner_session_archived`(409) を返す。権限が無い相手には従来どおり 404 なので、存在の有無は
漏れない。共有作成時もアーカイブ済みセッションは対象に選べない。

## 2. 権限

- `RO`: 会話の閲覧のみ。
- `RW`: 閲覧に加え、共有先が操作を「提案」できる。提案は所有者の承認後にだけ Agent へ送る。
- 複数規則が同じセッションへ一致した場合は `RW > RO`。
- 共有作成時は、login ID が同じテナントの active membership かを確認する。自分自身への共有は拒否する。
- 受信側 API は権限が無い catalog ID に一律 `404` を返し、存在確認にも使わせない。
- 共有先には再開・停止・アーカイブ・削除・ファイル操作 API を公開しない。

RW 提案本文は最大 32 KiB、1セッション20件、24時間で失効する。本文は tenant key custodian
がある環境では暗号化して CP DB に置き、承認・拒否・失効時に消去する。承認は DB の
`pending → processing` 条件付き更新で一人だけが取得してから Agent へ送る。claim transaction は
期限、catalog、現在の RW ACL を再検証して `processing` をcommitした時点で終了する。Agent HTTPを
DB transaction内に置かず、claimと同じtransactionでowner単位の2分leaseを取得する。ACL／catalog
変更と期限GCもmembership行を短時間lockしてactive leaseを検査するため、Postgresを共有する複数CP
replica間でもAgent操作を横切れない。成功確定とlease解放も同じtransactionで行い、同一process内では
share mutexにより競合リクエストを待機させる。そのためSQLiteの全write lockやPostgres connectionを
外部I/O中に保持せず、共有解除／RO降格との順序は一意になる。ACL変更が先なら提案を失効して本文を
消し、承認が先なら副作用の完了後にACL変更が通る。Workspace の停止も同じ
owner leaseを使う。start／stop／recreate／clean-home／idle reaperはRuntime操作の前に同じ行へ30秒のleaseを
取得し、10秒ごとにoperation ID一致のCASで更新して終了時に解放する。heartbeat／CASを失ったholderは
Runtime contextをcancelし、stop後、wipe前後、start前後のcheckpointを越えて次段階へ進めない。古い
holderによるhome削除もlease contextを各entry間で検査して中断する。解放はoperation ID条件付きなので、
新holderのleaseは消さない。そのため別CP replicaからの
lifecycle変更もAgent操作を横切れない。lifecycle変更が先なら
承認claimをbusyで拒否し、承認が先なら結果の永続化後にlifecycle変更を再試行できる。
idle reaperはこれらのfence待機後に、最新DB activity、接続数／lastSeen、Agentのworking／questionを
再取得する。request activityと長期接続presenceはWorkspaceごとに1行の単調なDB watermark／期限付き
leaseへも記録し、接続中は5秒ごとに更新する。別CP replicaのreaperもこれを参照し、inactive replicaが
他replicaのleaseを短縮することはない。古いsweepのidle判定を使い回さず、待機中または別replicaで
復帰したWorkspaceの停止を見送る。行数はWorkspace数を上限とし、activity量に比例して増えない。
各activity更新は15秒の保護leaseを作り、同じCPでは安全な先頭5秒内の更新をcoalesceするため、previewの
asset／HMR requestごとにSQLite writeは発生しない。idle Stopはowner leaseと結び付くDB stop intentを
activity行と同じWorkspace row lock下でclaimする。activityが先ならclaimできず、claimが先なら新規
ingressを`workspace_stopping`で拒否するため、最終確認と`Runtime.Stop`の間にも受理済みactivityは入らない。
claim直後にもowner leaseを再検証し、pauseから戻った期限切れholderは`Runtime.Stop`へ進まない。claim後に
CPがcrashしてintentだけ残った場合は、次の明示Startが新しいlifecycle lease／host fence取得後に清算する。
Runtimeがrunningでも早期return前に行うため、idle reaper無効環境でも利用者操作で回復できる。
Postgres HAではさらにWorkspace ID由来のsession advisory lockを専用DB connectionで外部Runtime I/Oの
全区間保持する。CPがpauseしてもlockは残り、新holderは旧操作の静止まで待つ。process crash時はconnection
切断で自動解放されるため、Docker／ECSでもlease checkpoint直後のpause gapを越えてStop／Startが交差しない。
待機者は`pg_try_advisory_lock`をpollして未取得connectionを即poolへ返すため、holderのcheckpoint／finalize用
connectionを枯渇させない。取得結果不明またはunlock失敗時は物理sessionを`ErrBadConn`で破棄し、lockを
保持したsessionがpoolへ戻ることを防ぐ。
native Runtimeではcontext cancelだけに依存せず、`dataDir/lifecycle.lock` のkernel flockを
lifecycle／承認の全区間で保持する。期限切れleaseを得た新holderも旧holderのStart／Stopが静止するまで
進めない。Start後にleaseを失えば起動時刻まで一致する自分のPIDだけを回収し、Stop中に失えば既送信の
SIGTERM後の終了を待つがSIGKILL・tmux・pidfile更新には進まない。PID再利用や旧holderによる新Agent停止を
防ぎながら、rollback不能な途中状態を次世代へ跨がせない。

CP は proposal ID を `X-Agent-Fleet-Operation-ID` として Agent へ渡す。Agent は home volume 内に
その ID を副作用前に create-only で永続 claim し、ハンドラ応答を永続化してから CP へ返す。
同じ ID の再送は保存済み応答を再生し、claim だけが残った場合は「結果不明」として実行しない。
したがって、成功後の応答喪失、5xx、CP/Agent crash のどの場合も副作用を二重実行しない。
保存応答bodyは32 KiB、1記録は64 KiB、ledger全体は512件かつ32 MiBに制限する。成功記録は7日で
GCし、判定不能な`processing`証跡は別枠で90日保持する。容量が判定不能証跡だけで埋まった場合は、
証跡を捨てず、新しい副作用をclaim前に拒否する。
CP の `processing` はこの結果不明 lease を表し、自動で `pending` へ戻さない。Agent に完了記録が
あれば次の照会で `approved` へ収束する。提案の24時間期限を跨いでもactive owner lease中の
`processing`は一覧pollで失効させず、lease終了後も判定不能なら失効して本文を消す。

## 3. 閲覧と停止状態

CP DB の `shared_session_catalog` はセッション名、表示情報、状態だけを持ち、会話本文を複製しない。

- 所有者 Workspace が起動中: CP が Agent の `/sessions/{name}/messages` を都度中継する。
- 所有者 Workspace が停止中: 左ペインの共有行と状態は表示するが、会話本文は取得できない。
  共有先の閲覧操作で Workspace を自動起動しない。
- セッション停止（再開可能な停止中）: 所有者 Workspace が起動中なら会話を閲覧できる。
- セッションアーカイブ: 共有先の一覧から消え、閲覧もできない（規則は残るので復元で戻る）。
- セッション削除: catalog から消え、共有先にも表示されない。

受信側の見え方は「他人の会話を読んでいる」ことが分かる形にする。左ペインはプロジェクト＝
working copy ＝セッションのツリーで、プロジェクト単位・working copy 単位に折りたためる
(所有者側ツリーと同じ localStorage 永続)。共有元は worktree を切るたびにノードが増えるので、
畳めないと rail が埋まる。セッション行のアイコンは所有者側と同じ kind 色付きアイコン
(どのエージェントの会話か、共有先には他に手掛かりが無い)。会話本文の user ターンの名前は
「あなた」ではなく共有元の login id を出す(読み手が書いたわけではない)。

**所有者の名乗りは login id = メールアドレス**で統一する。プロジェクト見出し(左ペイン)、
セッション行の tooltip、共有ビューのヘッダー、user ターンの発言者名のすべてが同じ文字列。
CP は受信一覧の DTO に `ownerUserKey`(正規化キー)と `ownerEmail` の両方を載せ、**表示は
`ownerEmail`、グルーピングと localStorage の鍵は `ownerUserKey`** と役割を分ける — user_key は
`sanitizeUser` を通した `a@x.com` → `a-x-com`(衝突時は接尾辞付き)で、人が名乗る文字列ではない
一方、同一性の鍵としては安定している。email を持たない identity(管理者が user_key だけで
足した場合)だけ user_key へ落とす。プロジェクト見出しの所有者表記は共有元が1人でも常に出す
(誰の会話かは共有先にとって一番の手掛かりで、隠すと自分のツリーと見分けが付かない)。

見出しの sticky は所有者側ツリー(project.css)と**同じクラス名を共有しているため、詳細度が
同値の規則が衝突する**。`.proj-node.wt > .proj-node-head`(所有者側: 絞り込みバー分だけ下げた段)は
共有ツリーの worktree ノードにもそのまま当たり、同値だと勝敗がバンドル順で決まって負ける
(worktree 見出しだけ 43px 低く貼り付き、プロジェクト見出しとの間の空帯をセッション行が
素通りする)。共有側の規則は `.wt` を明示して詳細度を1段上げ、順序に依存させない。

面の色は設定モーダルの
「表示」で独立に選べる(テーマ inherit/dark/light ＋ 背景色) — 他人の会話を読んでいる面を自分の
ミラーと違う色にできると、どちらを見ているかが色で分かる。仕組みはミラーと同じ
(data-theme のスコープ＋ --chat-bg / --chat-accent)。

会話ブロックはミラーと同じ transcript 描画層をそのまま通す。ボタン等の下地スタイルも
**同じ面として揃える** — mirror.css の element globals を `.mirrorview` だけに閉じていたため、
同じフッターのコピーが共有側だけ主ボタンのように浮いていた。「最新へ」も同じピルを出す
(上へ読み返している間だけ)。

作業コピーの名乗りも所有者側の repo 行に合わせる: worktree はブランチ名で呼び(フォルダ名は
`<base>@<ランダム slug>` で、どの作業か分からない)、ベースはフォルダ名＋現在のブランチ。
そのため catalog はブランチも保持する。**これは転写 DTO が落とす turn の `branch` とは別物**で、
あちらは会話の描画に要らない座標なので通さない、こちらは作業コピーの表示ラベル、という区別。

会話の取得は、初回だけ tail ウィンドウ、以降は `since=<cursor>` の増分、上方向は
`before=<firstLine>` のページング。**受け取ったターンは idx(転写内の絶対位置)を鍵に冪等に
マージする** — 追記だけにすると、伸びている最中の assistant ターンを毎ポーリング送り直す
store 系エージェント(opencode/codex)で同じ回答が積み上がり、ページの重なりも二重に出る。
ウィンドウ幅はミラーと同じにする: claude では行単位なので、狭すぎると1回の応答の途中から
始まってしまい、その応答を引き起こした user の発言が画面外に落ちる。

一覧画面を経由しない保存済み catalog ID への直接アクセスでも、所有者 Workspace が起動中なら
live inventory を同期してから ACL を評価する。削除済みセッション／作業コピーの古い規則で
履歴閲覧や RW 提案を続けることはできない。

この inventory 同期は所有者単位でキャッシュする。窓は2段: 直リンク経路(1セッションを開く
明示操作なので、消えたばかりのセッションを出さないよう詰める)が 10 秒(`shareCatalogTTL`)、
共有一覧の定期ポーリングが 60 秒(`shareListCatalogTTL`)。一覧はすべての受信者のタブで
回り続けるため、ここを詰めると他人の Workspace への往復が定常的に流れ続ける。代わりに
**共有セクションのリロードボタン**(`?refresh=1`)が間引きを飛び越えて取り直す — ただし
連打が増幅器にならないよう下限 3 秒(`shareForcedCatalogTTL`)は残す。

一覧の行には所有者側と同じ状態チップ(進行中 / 入力待ち / 質問中 / プラン待ち / 停止中)を
出す。素は Agent が catalog wire で返している live state で、CP は catalog の `activity` 列に
持つ(`state` 列は生存 running/stopped なので別)。鮮度は上の 60 秒＋リロード次第で、所有者
自身の一覧(数秒間隔)ほど新しくはない。所有者 Workspace が停止中のときは、その1事実で全行が
止まっているので行ごとのチップは出さない。

なぜ間引くのか: 同期は所有者 Workspace の Agent へ 2 往復(`/sessions/catalog` + `/repos`)し、
さらに catalog の全置換を所有者ごとの mutex 下で行う。受信者ごとの履歴ポーリング全てに乗せると
共有履歴読み出しの支配的コストになり最初の描画が目に見えて遅れ、5 秒間隔で回る一覧に乗せると
同じ mutex を取る共有の作成／解除がその裏で待たされる（実際に「共有の開始・停止が遅い」として
現れた）。**間引くのは在庫の突き合わせだけで、認可は間引かない** — 共有ルールの評価
(`ListSessionSharesByRecipient` + `effectivePermission`)は毎リクエスト DB を参照するので、
共有解除は即時に効く。遅れるのは、所有者側でセッションや作業コピーが消えたことの検知と状態
チップの鮮度だけ。同期に失敗した場合は stamp を捨てるので、失敗を「同期済み」と誤認して
居座ることはない。

履歴応答と復号したRW提案一覧は `Cache-Control: private, no-store` とし、共通 ETag middleware も `no-store` 応答を
バッファ／検証子化しない。CP は `cwd`、`path`、`filePath`、JSONL の所在など Workspace 内の
座標を除く。共有履歴は除外リストではなく専用allowlist DTOへ変換し、未知のfieldと
`file`／`files`／`file_path`等の全構造化座標を既定で落とす。本文、ツール要約・出力、diff本文、
質問内容だけを通す。受信一覧にも所有者の絶対パスを返さない。添付ファイルやファイル API は
v1 の共有対象外である。

受信側はこの DTO をミラーと同一の描画層(`console/src/features/mirror/transcript/`)へ流し込む
ので、ツール利用の折りたたみ・プラン・思考・委譲・コンパクション要約は所有者と同じ形で出る。
座標を持たない表示フラグ(`compact`)は allowlist に含める — 座標ではないうえ、これが無いと
コンパクション要約が巨大な通常ターンとして描かれてしまう。一方 `branch` は会話の描画に不要な
ので通さない。受信者が「何をできるか」は `TranscriptCaps` で表現し、**能力が無い操作要素は
描画しない**(押せない要素を出さない)。開く先の座標が無い編集差分とプランは、ペインではなく
その場で展開する。

**引き継ぎ提案(propose_session_handoff)も共有対象**にする。転写に残るのはツール行と定型の
完了文だけで、次セッションへ渡す本文(表示名＋引き継ぎプロンプト)は所有者 Workspace の別ストア
(`session-handoffs`)にある。ミラーはそれを「提案された時点」へ差し込むカードとして描くので、
同じ描画層を通す共有ビューで出さないと、共有先には「引き継いだらしい」ことしか読めない。
CP は転写と同じく専用 allowlist へ通し(`id`／`title`／`prompt`／`created_at`／`launched_at`
だけ)、ファイルの置き場所などの座標は返さない。**取得は転写とは別のポーリング**にする —
転写応答に相乗りさせると CP から所有者 Agent への往復が毎回2倍になり、共有履歴読み出しの
支配的コストがそのまま倍増する。代わりに間隔を粗く(5秒)し、転写と同じ毎分120回のバケツで
数える。カードは読むだけで、編集・破棄・起動は出さない(能力が無い操作要素は描画しない)。
**カードは転写より後に届く**ので、末尾追従は転写だけでなく提案の到着でも回す — でないと
末尾にいたまま高さだけが増え、その分(実測 +263px)着地が上へずれる。

**表示位置はミラーと同じ規則にする**: 初めて開いたら末尾、途中まで読んで離れたら次に開いた
ときはその位置。位置は px ではなくターン(`[data-turn-idx]`)を基準に持ち、タブが生きている
間だけ覚える(`mirror/scrollMark.ts` をそのまま使い、鍵は `shared:<catalog id>`)。転写の高さは
ほぼ全部が遅れて確定するので、**ResizeObserver で「追従中は末尾／復元中はアンカー」を保ち
続ける**のも同じ — これが無いと開いた直後に末尾の 2,096px 手前で止まったままになる(実測・
400ターン)。追従の判定は生の距離ではなく、自分が最後に書いた `scrollTop` と比べた「読者が
上へ動かしたか」で決める(距離で見ると自分のピンの下で伸びた分を読者の操作と誤読して、以後の
再ピンが全部止まる)。

**1点だけミラーと事情が違う**: ミラーはセッションを持ち替えても unmount しないので、離脱時に
DOM から位置を測り直せる。共有ビューはペインを閉じた／自分のセッションへ切り替えた時点で
unmount され、後片付けが走る頃には ref が外れていて測れない(rect が全部 0)。そのため
スクロールが止まるたびに位置を控えておき、離脱時はそれを使う。

ただし、会話本文・Agent の回答・ツール出力そのものは共有対象であり、その中に利用者が書いた
秘密情報があっても確実な自動検出はできない。共有ダイアログはこの点と、受信者が表示内容を
保存したコピーは共有解除後に回収できない点を明示する。

## 4. 容量と負荷

会話本文を CP に複製しないため、共有専用の保存 quota は設けない。データ量は所有者 Workspace の
既存ディスク quota にだけ計上される。共有に必要な CP 永続データは ACL、軽量 catalog、未処理の
小さな RW 提案だけである。1セッション20件のpending上限はcatalog行をロックした同一transactionで
count＋insertするため、並行POSTでも超過しない。

一方、起動中なら無制限に読めるという意味ではない。履歴は cursor/limit で増分取得し、CP は
受信者×セッションごとに毎分120回へ制限し、1応答の decode 上限を16 MiBにする。Console は
表示中2.5秒、一覧5秒間隔で更新し、タブが非表示なら一覧 polling を止める。

## 5. API と監査

- 所有者: `GET/POST/PATCH/DELETE /api/session-shares...`
- 受信者: `GET /api/shared-sessions`、`GET .../{id}/messages`、`GET .../{id}/handoff-proposals`
- RW: `POST .../{id}/proposals`
- 所有者承認: `GET /api/session-share-proposals`、`POST .../{id}/approve|reject`

共有作成・解除・権限変更・提案作成・承認・拒否を audit log に記録する。監査詳細には提案本文や
会話本文を入れず、対象ID、操作種別、関係 membership だけを残す。
