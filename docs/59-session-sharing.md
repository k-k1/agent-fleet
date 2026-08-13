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

会話の取得は、初回だけ tail ウィンドウ、以降は `since=<cursor>` の増分、上方向は
`before=<firstLine>` のページング。**受け取ったターンは idx(転写内の絶対位置)を鍵に冪等に
マージする** — 追記だけにすると、伸びている最中の assistant ターンを毎ポーリング送り直す
store 系エージェント(opencode/codex)で同じ回答が積み上がり、ページの重なりも二重に出る。
ウィンドウ幅はミラーと同じにする: claude では行単位なので、狭すぎると1回の応答の途中から
始まってしまい、その応答を引き起こした user の発言が画面外に落ちる。

一覧画面を経由しない保存済み catalog ID への直接アクセスでも、所有者 Workspace が起動中なら
live inventory を同期してから ACL を評価する。削除済みセッション／作業コピーの古い規則で
履歴閲覧や RW 提案を続けることはできない。

この inventory 同期は所有者単位で 10 秒間(`shareCatalogTTL`)キャッシュし、**直リンク経路も
共有一覧(`GET /api/shared-sessions`)も同じ throttle に乗せる**。同期は所有者 Workspace の
Agent へ 2 往復(`/sessions/catalog` + `/repos`)し、さらに catalog の全置換を所有者ごとの
mutex 下で行うため、受信者ごとの履歴ポーリング全てに乗せると共有履歴読み出しの支配的コストに
なり、最初の描画が目に見えて遅れる。共有一覧は受信側のタブごとに 5 秒間隔で叩かれるので、
ここを素通しにすると同じ mutex を取る共有の作成／解除がその裏で待たされ、操作が体感で
遅くなる（実際に「共有の開始・停止が遅い」として現れた）。一覧側で共有される副作用として、
一覧をポーリングしているタブから共有セッションを開くと在庫は既に fresh で、最初の描画が
同期を待たない。**間引くのは在庫の突き合わせだけで、認可は
間引かない** — 共有ルールの評価(`ListSessionSharesByRecipient` + `effectivePermission`)は
従来どおり毎リクエスト DB を参照するので、共有解除は即時に効く。上限 10 秒だけ遅れるのは、
所有者側でセッションや作業コピーが消えたことの検知である。同期に失敗した場合は stamp を
捨てるので、失敗を「同期済み」と誤認して居座ることはない。

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
- 受信者: `GET /api/shared-sessions`、`GET .../{id}/messages`
- RW: `POST .../{id}/proposals`
- 所有者承認: `GET /api/session-share-proposals`、`POST .../{id}/approve|reject`

共有作成・解除・権限変更・提案作成・承認・拒否を audit log に記録する。監査詳細には提案本文や
会話本文を入れず、対象ID、操作種別、関係 membership だけを残す。
