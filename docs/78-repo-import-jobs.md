# docs/78 — リポジトリの取り込みを非同期ジョブにする

決定は [ADR 0059](decisions/0059-repo-import-jobs.md)。`git clone` と `svn checkout` を、HTTP
リクエストの寿命から切り離した**名前付きジョブ**にし、進行と結末を観測できるようにする。

## 0. 何が起きたか（2026-08-26 / <prod-deployment> 環境）

大きな SVN リポジトリ（r78028）のチェックアウトで、実際に観測された順:

1. Console が「チェックアウトしました」と報告。一覧にも `r78028` 付きで並んだ。
2. しかし中身は**一部のフォルダしか無い**。手元 PC の作業コピーには 12 個のフォルダがあるのに、
   Console のファイル木には 2 個しか見えない。
3. 行には 未コミット が付いていた（チェックアウト直後なのに）。
4. 更新（svn） → `svn: E155037: Previous operation has not finished; run 'cleanup' if it was interrupted`
5. ロックを解除 → `svn: E200033: Another process is blocking the working copy database … sqlite[S5]: database is locked`
6. 30 分を越えたところで、**作業コピーがフォルダごと消えた**。

### 再現（svn 1.14.2・実測）

チェックアウトを走らせたまま、Console と同じ操作を順に叩くと逐語で再現する:

```
=== svn status（一覧の 未コミット 判定） ===
! L     /tmp/repro/wc          ← L = 作業コピーがロック中。未コミットではない
=== svn update ===
svn: E155037: Previous operation has not finished; run 'cleanup' if it was interrupted
=== svn cleanup ===
svn: E200033: Another process is blocking the working copy database, … sqlite[S5]: database is locked
checkout exit=0                ← 放っておけば最後まで終わる
```

つまり 2〜5 は全部「**まだ走っている**チェックアウトに触っていた」の帰結である。

## 1. なぜ「成功」と報告されたか

| 段 | 実際の挙動 | 結果 |
|---|---|---|
| ALB | idle timeout **60 秒**（[30-ingress.yaml](../deploy/aws/ecs/cfn/30-ingress.yaml)） | 大きな取り込みの応答は必ず切れる |
| `api()` | 非 JSON / 5xx を `{error:{code:"http_504"}}` に畳む（`console/src/core/api/client.ts`） | 切断がエラーとして降りてくる |
| 旧 `clone.ts` | 「エラーだが `~/repos` にフォルダが増えていれば成功」 | `svn checkout` は開始 1 秒で `.svn` を作る＝**必ず成功に倒れる** |
| `GET /repos` | `.svn` があるフォルダを作業コピーとして並べる | 走行中の物も並ぶ |
| `svnRepoEntry` | 行ごとに `svn info` ×2 ＋ `svn status` | 走行中の checkout と `wc.db` を奪い合う。`L` を 未コミット と表示 |
| `handleSvnCheckout` | 30 分の上限、失敗時は `os.RemoveAll(dir)` | 応答を待つ者が居ないまま作業コピーが消える |

同じ穴は git の clone にもあった（`.git` ができるのも clone の最初）。

## 2. モデル

- **ジョブ** = 1 回の取り込み。`{id, kind: git|svn, name, path, url, state, progress, items, error, kept, startedAt, endedAt}`。
- `state` は `running` / `done` / `failed` / `canceled` / `interrupted`。**完了の唯一の根拠はこれ**で、
  HTTP 応答が届いたかどうかではない。
- 終端したジョブは**既読にするまで一覧に残る**（`done` だけ 10 分で自然に消える）。結末を見る前に
  消えると「黙って失敗した」に戻るため。
- 進行は行を数える。`svn checkout` は 1 ファイル 1 行、`git clone --progress` は `\r` 区切りで進捗を出す。
  全部ためると巨大リポジトリでメモリを食うので、**カウンタ＋最終行＋末尾 8KB のリング**だけ持つ
  （末尾はエラー本文に使う）。総数は svn も git も事前に教えてくれないので、**割合は出さない**。

## 3. API

Agent（CP は明示許可リストなので `control-plane/routes.go` にも同じパスの登録が要る）:

| メソッド | パス | 意味 |
|---|---|---|
| POST | `/repos` | git clone を**開始**。`202 {job}` |
| POST | `/repos/svn` | svn checkout を**開始**。`202 {job}` |
| GET | `/repo-jobs` | 取り込みの一覧（進行＋結末） |
| DELETE | `/repo-jobs/{id}` | 走行中なら**中止**、終端済みなら**既読** |

`/repos/jobs` ではなく `/repo-jobs` なのは、`DELETE /repos/{name}/branch` と mux で衝突するため
（`/api/repos/jobs/branch` がどちらにも一致する）。

同期だった頃の `201 {repo}` は返らない。`name` が既にある場合の `409 exists` と、取り込み中に同じ
名前を頼まれたときの `409 job_running` は開始時に同期で返る。

## 4. 一覧から外す

`GET /repos` は**走行中の名前をスキップする**。半端な作業コピーは「起動できる・更新できる・
`svn status` を掛けてよい物」ではない。Console はその行を `GET /repo-jobs` から描く。
`DELETE /repos/{name}` も走行中は `409 job_running`（書き込み中のフォルダを消させない）。

## 5. 失敗したときに何を残すか

- **作業コピーになっていれば残す。** svn は `cleanup` + `update` で続きから取れる。行にもそう出す。
- **なっていなければ消す。** URL の打ち間違いや認証失敗で出来た空のフォルダは残しても意味がない。
- 上限は **6 時間**（`repoJobTimeout`）。「人が待てる上限」ではなく「明らかに壊れている」だけを切る値。
  止めたいときは中止できるし、走っていることは一覧で見える。
- 中止も同じ扱い（残る物は残す）。中止は「止めたい」であって「消したい」ではない。

## 6. 中断（Agent が死ぬ）

ECS のタスク入れ替え・idle-stop・OOM で、取り込みは Agent ごと死ぬ。`~/repos/.af-repo-jobs/<name>.json`
に marker を置き、**起動時に生き残っていれば `interrupted` として一覧に戻す**。marker が `~/repos` の下に
あるのは意図的で、作業コピーと寿命を揃えるため（コンテナ作り直しで両方消える）。

中断は「走行中」ではないので、そのフォルダは作業コピーとして一覧に戻る（更新 / 削除できる）。
ジョブの行は「中断されました」と、残っている作業コピーの扱いを言う。

## 7. 取り込み中は Workspace を止めない

`GET /sessions` の封筒に `repoJobs`（走行中の件数）を載せ、reaper の tier2 busy に足す。GET の
ポーリングは活動に数えない規約（docs/19）なので、これが無いと 1 時間の取り込みは自動停止に殺される。
**専用のリクエストは増えない** — reaper は毎スイープでこの一覧を読んでいる。

「なぜ止まらないか」（docs/75 P4）にも `repojob` の holder を出す。reaper が止めないのに画面の
holders が空、という状態を作らないため（docs/75 決定 11）。

## 8. 自動修復の文字列（`svnLocked`）

中断後に svn が出すのは:

```
svn: E155037: Previous operation has not finished; run 'cleanup' if it was interrupted
```

**`svn cleanup` ではなく `cleanup`**。`E155004` 系の文言だけを見ていたので自動修復は一度も走らず、
利用者は毎回手で ロックを解除 するしかなかった。`E155037` と `run 'cleanup'` と
`previous operation has not finished` を追加した。

なお `ctx` が切れている（中止・上限）ときは修復しない。中止した処理を勝手に再開してしまうため。

## 9. Console

- ジョブの行（`RepoJobRow`）がリポジトリ一覧の先頭に並ぶ。**タブを閉じても再読み込みしても同じ行が出る**
  （進行はサーバにある）。中止／既読のボタンを持つ。
- ポーリングは走行中だけ 2 秒、そうでなければ 60 秒。`refresh()` は同時呼び出しを 1 本にまとめる。
- `cloneRepo()` / `svnCheckout()` は開始してジョブの終端まで待つ（呼び出し元の見え方は変わらない）が、
  **判定材料は `state` だけ**になった。フォルダが増えたかどうかは見ない。
- 文言は VCS で分ける: git は クローン、svn は チェックアウト。

## 10. 残っている宿題

- **実機確認**（<prod-deployment>）。ここまでの検証は単体テスト・Go の実 svn テスト（`svnadmin` で作った file:// リポジトリ）・
  手元での svn 1.14.2 の再現で、ALB を挟んだ実環境での通しはまだ。
- 進捗に**総数**が無い（svn も git も事前に教えてくれない）。`svn info --show-item` で対象リビジョンの
  エントリ数を先に引く手はあるが、大きなリポジトリでは前段が重い。
- 取り込み中の Files 木は、増えていく途中のフォルダをそのまま見せる。ここに「取り込み中」の印は無い。
