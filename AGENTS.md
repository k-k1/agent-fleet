# Agent instructions

## コミット

コミットを作成するときは、`CONTRIBUTING.md` の「Commits & PRs」を必ず確認し、
記載されたコミットメッセージ形式と帰属規約に従うこと。

特に以下を必須とする。

- タイトルは `<type>(<scope>): <日本語の要約>` の形式にする。
- タイトルの要約と本文は日本語で書く。
- バグ修正や挙動変更の本文には、真因、修正方法、検証結果を記載する。
- エージェントが作成または実質的に関与したコミットには、メッセージ末尾に空行を
  挟んで `Co-Authored-By` トレーラを付ける。
- `Co-Authored-By` には CLI 名ではなく、実際に作業を実行したモデル名を記載する。
- メールアドレスには、モデル提供元の `noreply@<提供元ドメイン>` を使用する。
- `Claude-Session:` トレーラは追加しない。ただし、Claude Code が Remote Control
  接続時に自動追加する場合は許容する。
- コミットを実行する直前に、完成したコミットメッセージ全体が規約を満たしている
  ことを再確認する。

## Console のテストを走らせるとき

**必ず `console/` をカレントディレクトリにして実行する。** リポジトリルートから
`npx vitest` を叩くと、ルートには `package.json` も `node_modules` も無いため npx が
別の vitest をダウンロードし、`console/vite.config.js` を読まないまま起動する。設定が
効かないので environment は node になり、DOM テストは `document is not defined` で落ち、
`--project` は「プロジェクトが見つからない」になる。設定の不具合と紛らわしいので注意する。

```
cd console
npm test                       # 全プロジェクト
npx vitest run --project=node  # 純ロジック（既定）
npx vitest run --project=dom   # レンダーテスト（jsdom）
npx vitest run src/features/viewer/FileView.dom.test.tsx   # ファイル指定でも可
```

テストは2プロジェクトに分かれている（`console/vite.config.js`）。jsdom 環境の構築は
テストファイル1本あたり約1.3秒かかるため、既定は node のままとし、実際にコンポーネントを
マウントするテストだけ `*.dom.test.tsx` で opt-in する。

## このリポジトリの UI を利用者に見てもらうとき

Console（`console/`）の変更を利用者に確認してもらう場合は、開発サーバーを
`127.0.0.1:<port>` で listen させ、**ポートとパスを明示**して
「プレビュー → ペインで開く」を案内する。

- HMR を伴う Vite dev サーバーや WebSocket を使う画面はブラウザペイン（ペインで開く）を
  優先する。単純な HTTP 表示なら軽量プレビューでよい。
- ブラウザペインは利用者向けの Console 表示機能であり、**エージェントに開閉・閲覧用の
  ツールは無い**。ペインに何が映っているかは推測しない。
- 自分で「確認した」と言えるのは、headless Chromium（`/usr/bin/chromium`）で自ら描画を
  検証したときだけ。利用者のペイン表示と、エージェント自身の headless 検証は区別する。
- 確認が終わったら開発サーバーは止め、常駐させない（共有ホストはメモリ制約が厳しい）。
- API キー・cookie・Console ログに現れた秘密をログや文書へ転記しない。

操作上の正（用語・推奨フロー・状態・制約）は `docs/31-container-browser-pane-ux-contract.md`
を参照する。

