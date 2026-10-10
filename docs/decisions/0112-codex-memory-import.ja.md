# 0112. codex のメモリを AF メモリへ取り込む：測れたことと提案するルール

[English](0112-codex-memory-import.md) | 日本語

- 状態：**提案**（2026-10-10）。ドキュメントのみ。codex 向けの取り込みコードはない。統合後のファイル
  （`MEMORY.md`、`memory_summary.md`、`rollout_summaries/*.md`）はワークスペースで**作れなかった**ため、
  それらの形に依存するルールはすべて codex バイナリに埋め込まれたプロンプトからの推定で、推定と明記する。
- フォローアップ：#1950。関連：#1683、#1569、[0108](0108-af-owned-agent-memory.ja.md) 決定 6 の手順 1、
  [0022](0022-agent-memory-management.ja.md) 決定 6、`memoryx/memory_roots.go`、
  `memoryx/agent_memory_claude_import.go`。

## 背景

claude の一回限りの取り込み（#1569）が成り立つのは、claude のメモリがもともと AF の保存形だから：
プロジェクトごとのディレクトリ、1 メモリ 1 ファイル、`name` と `description` を持つ frontmatter。
#1683 は codex にも同じ取り込みを求め、進め方は「まず実ファイルの形を測り、それから決める」。
この記録はその測定の試みと、そこから言えることである。

## 測定

Codex CLI 0.162.1。セッションの作業ディレクトリ配下の使い捨て `CODEX_HOME`（`~/.codex` ではない）に
`[features] memories = true` を書いた（`codex features list` は `memories stable true`、
`external_agent_memory_import` は `under development`・`false`）。

**観測したこと**（`codex exec "say hi"` を 1 回）：

- 初回実行で `memories/` ができる。中に独自の `.git`（phase 2 の差分の基準）と、`raw_memories.md`
  （37 バイト。`# Raw Memories` と `No raw memories yet.`）、`phase2_workspace_diff.md`（生成された
  git 形式の差分。「最初に読み、編集しない」と書かれている）、`extensions/ad_hoc/instructions.md`
  （733 バイト。固定文）、空の `rollout_summaries/`。`memories_1.sqlite` は `memories/` の外にある。
- `codex exec` の実行はモデル API の `401 Unauthorized` で失敗した。使い捨てホームに認証情報はなく、
  実物の `~/.codex` は触れない。統合にはモデル呼び出しが要る（phase 1 は終わった rollout からの抽出で
  アイドル後、phase 2 はサブエージェント）ため統合後の出力は得られず、`MEMORY.md`・
  `memory_summary.md`・`rollout_summaries/<slug>.md`・`skills/` は書かれなかった。どちらのフェーズが
  起動したかは不明で、観測したのは 1 回の実行の 401 だけ。統合後の実ファイルは見ておらず、ここで
  引用も捏造もしていない。

**codex バイナリ内のプロンプトテンプレートから読んだこと**（同じ 0.162.1 のバイナリの文字列。
codex がモデルに「書け」と指示している内容で、観測した出力ではない）：

- `memory_summary.md`：1 行目が厳密に `v1`、続いて `## User Profile`、`## User preferences`、
  `## General Tips`、`## What's in Memory`（配下に `### <プロジェクトスコープ>`、`#### <日付>`、
  `rollout_summaries/<ファイル>` へのポインタ）。10,000 バイト未満。毎セッションの先頭に注入される
  ので、メモリの集合ではなく索引。
- `MEMORY.md`：ブロックの連なり。各ブロックは `# Task Group: <題>` / `scope: <1 行>` /
  `applies_to: cwd=<パス>; reuse_rule=<文>`、続いて `## Task <n>: <題>`（それぞれ
  `### rollout_summary_files` とキーワード行を持つ）、最後に任意で `## User preferences`・
  `## Reusable knowledge`・`## Failures and how to do differently`。箇条書きは `-`、太字なし。
  ブロックは名前順ではなく有用度順で、統合のたびに codex が書き直す。
- `rollout_summaries/<rollout_slug>.md`：rollout ごとの要約。`description:` 行と番号付きの生の証拠
  断片を持ち、`thread_id` と `rollout_path` が入る。
- `skills/<名前>/SKILL.md`：`name` / `description` の frontmatter を持つ再利用手順。
- `extensions/ad_hoc/notes/<YYYY-MM-DDTHH-MM-SS-slug>.md`：ユーザーが codex に覚えさせた逐語のメモ
  （`add_ad_hoc_note` ツール）。拡張の説明は「権威ある情報として扱い、由来の主張には
  `[ad-hoc note]` を付ける」と言う。
- phase 2 は他エージェントのメモリディレクトリを統合する分岐（「imported resources」）も持つ。
  これは本件の上流側の対（claude → codex）であり、必要なものではない。

## 提案するルール（実出力を見るまでは推定）

1. **取り込み元は `MEMORY.md` だけ、`# Task Group:` ブロックで分割。** ブロックが codex の書くものの
   中で「1 メモリ」に最も近く、見出しが自己記述的。`memory_summary.md` は取り込まない（索引で、
   コピーしないファイルを指す）。`rollout_summaries/` は取り込まない（生の証拠を含む rollout ごとの
   要約で量が多く、残す価値のある事実は `MEMORY.md` に統合済み）。`skills/` は取り込まない（手順は
   メモリでなくスキルの仕組みの領分）。`extensions/ad_hoc/notes/` は後の第 2 段の候補：逐語で、
   メンバー自身の依頼で、1 件 1 ファイルなので最も欠落が少ない。
2. **導出する項目。** `name`：ブロック題を小文字にして `[a-z0-9-]` に縮め、`agentMemNameRe`（64 文字）
   に収め、衝突時は短いハッシュを付ける。`description`：`scope:` 行（1 行、`agentMemMaxDescription`
   以内）。無いブロックは claude で description の無いファイルと同じく `invalid`。`type`：空
   （codex に型はなく、推測は作り話になる）。`body`：`# Task Group:` 行から次のブロックの手前まで逐語。
3. **スコープ。** `applies_to: cwd=` の値を、claude の slug と同じ方法（`agentMemImportProjects`：
   `~/repos` 配下の作業コピー、worktree はメインクローンへ畳む）でプロジェクトに対応づける。対応が無い、
   または cwd がファミリー・ワークフローの場合は**ユーザースコープ**を既定とする案（未決事項 7：
   ユーザースコープは全プロジェクトへ配る）で、プレビューでメンバーが
   ブロックごとにプロジェクトかユーザースコープを選ぶ。選択はリクエストに載せる。プレビューで見せて
   いないプロジェクトには置かない。
4. **claude の取り込みと同じルール**：プレビューが先、適用はロック下で再評価。秘密検査に引っかかった
   ブロックはマスクした指摘つきで一覧し、取り込まない。墓標または履歴にある名前は `forgotten` で
   復活させない。`source` は `codex:memories/MEMORY.md#<name>`、`source_hash` はブロックのバイト列の
   sha256。作者の種別とセッションは `unknown`。固定したディレクトリハンドルに対し `O_NOFOLLOW` で開く。
   プレビューと適用それぞれに件数上限。
5. **書き戻さない。** 0108 決定 6 のとおり、AF メモリを `~/.codex/memories` へ写さない。

## 未決事項（実出力が要る）

1. **名前の安定性と復活。** 観測していない：プロンプト上、codex は統合時にブロックを再編でき、題が
   変わりうる。変われば導出名が変わり、旧名の墓標（スコープと名前で持つ）は同じ知識を守らなくなる：
   忘れたブロックが別名で戻りうる。`source_hash` では防げない。規則 4 のとおり見出しを含むブロック
   全体の sha256 なので、題だけ変わっても変わる。claude の取り込みと同じく入力の同一性（プレビュー／適用の
   変更検出）の用途のまま保つ。復活防止には、安定 ID か正規化した指紋という別の、未設計の仕組みが要る。
   実装の前に、題だけを変えたブロックと別スコープへ取り込んだブロックが、忘れた後に戻らないことを
   確かめるテストが要る。統合をまたぐ本文の変化の大きさは、実際の統合を複数回測らないと分からない。
2. **`applies_to: cwd=` が普通はパスなのか。** プロンプトは「cwd のファミリーまたはワークフロー」も
   許す。プロジェクトに対応するブロックの割合で、ユーザースコープを既定にするか例外にするかが決まる。
3. **ブロックの大きさ。** ブロックはグループの全タスクを含む。`agentMemMaxBody` は 200 KiB だが、
   メモリ読み取りツールは短い項目を前提にする。`## Task <n>` で割るかは実際の大きさ次第。
4. **ad-hoc ノート**：取り込むか（規則 1）、`[ad-hoc note]` タグを残すか。
5. **stage-1 の `raw_memories.md` を取り込み元にする価値があるか。** プロンプト上は「一時ファイル」で、
   フェーズの間にしか無い可能性がある。
6. **`external_agent_memory_import`**（codex で開発中）が本件を置き換えるか：これは逆向き
   （claude → codex）で、ここで述べた取り込みは変わらない。
7. **個人名を含むパスとユーザースコープへの拡散。** `cwd=` には個人名や顧客名が入りうり、本文は逐語で
   コピーされる。一般の秘密検査はどちらも確実には検出しない。対応の無いブロックを既定でユーザー
   スコープに置くと、そのパスとプロジェクト固有の本文が他の全プロジェクトへ配られる。これは秘密とは
   別のリスク。実装の前に決める：プレビューで本文・`cwd`・配布範囲を見せ、対応の無いブロックは
   メンバーが明示的にスコープを選ぶこと。パスの除去、ブロックの拒否、本文の匿名化のどれにするか
   （匿名化すると本文は逐語の約束から外れるので、`source_hash` がどのバイト列を覆うかを区別する）。

## 結果

- 動作は変わらず、ユーザーに見えるものも増えないため、ガイドは変更しない。
- 残る作業（取り込みの実装）はプルリクエストから参照するフォローアップ Issue で追い、codex が
  メンバー自身の選んだログインで統合まで走ったワークスペースが得られてから進める。
