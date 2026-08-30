# docs/42 — ホスト常駐 `af` の自動更新

決定は [ADR 0025](../decisions/0025-native-auto-update.ja.md)。native パッケージ（[docs/34](34-native-runtime.md) /
[docs/35](35-packaging.md)）をホストで常駐させたまま、放っておいても新しい版が**用意**され、任意のタイミングで
**適用**できるようにする。設計の核は **stage（取得）と apply（再起動）の分離**。

## 0. なぜ分けるのか（systemd の2つの罠）

native は `af start` を systemd **user** サービスで常駐させることが多い（`ExecStart=%h/.local/bin/af start`）。
ここで単純に自動更新すると：

1. **走行中の af-cp は差し替わらない。** 版 dir を swap し symlink を張り替えても、起動済みの `af-cp` は
   そのまま。**restart するまで新版は効かない。**
2. **自ユニットは自分を安全に再起動できない。** af-cp から `systemctl --user restart` すると自分（main PID）に
   SIGTERM が飛び、restart が途中で殺されうる。

さらに restart は af-cp と配下ワークスペースを落とす（unit の cgroup ごと停止）ため、**走行中セッションを
問答無用で切る自動 restart は取らない**。→ 「stage は自動・既定 ON／apply は明示」に分ける。

## 1. 二層のバージョン（前提）

`af` は2つの軸で版管理される（docs/35）。

1. **native パッケージ**（`af-cp`＋console＋`af` ランチャ）＝ `~/.local/opt/agent-fleet/<v>/`。install.sh 再実行＝
   版 dir 差し替え＋`~/.local/bin/af` symlink 張り替え。
2. **rootfs**（ワークスペース image・パッケージ内 `rootfs.json` にピン）＝ `af start`（af-cp）が起動時に
   `WS_DATA/shared/rootfs/<R_VER>` へ遅延取得、旧版は `.ok` 付きで保持。

`ExecStart` は symlink を辿るので、**restart さえすれば新版を拾う**。この間接参照が apply の仕組みの土台。

## 2. stage — `af update`

install.sh の取得ロジックを `af` ランチャに内蔵した stage 専用サブコマンド。

```bash
af update --check   # 新版の有無だけ報告（DL しない）
af update           # DL + sha256 検証 + stage（latest、または AF_VERSION 固定）
```

- **目標版**: `AF_VERSION` があればそれ（ピン）。無ければ `https://github.com/<repo>/releases/latest` の
  リダイレクト先タグから latest（GitHub API レート非依存）。
- **検証**: release の `SHA256SUMS` と照合してから展開（改ざん・切断を起動前に検出）。
- **配置**: `~/.local/opt/agent-fleet/<target>/` に staging→atomic swap → `~/.local/bin/af` を張り替え。
- **無改変**: 走行中 af-cp は触らない。`WS_DATA` も不変。**restart しない**。
- **配布座標**: パッケージ内 `dist.json`（`build.sh` が生成、`repo` / `url_base`）を参照。`AF_DIST_REPO` /
  `AF_DIST_URL_BASE` で上書き可（install.sh の既定と一致）。
- **前提**: install.sh の symlink 配置でのみ自己更新可。手展開コピーは検出して明示エラー（新 tar を手展開せよ）。

## 3. 既定 ON — systemd user timer（stage 専用）

install.sh は systemd user バスが使えるとき、**日次 timer を既定で enable** する。

```
~/.config/systemd/user/agent-fleet-update.service   # Type=oneshot / ExecStart=<prefix>/bin/af update --yes
~/.config/systemd/user/agent-fleet-update.timer     # OnCalendar=daily / Persistent=true / RandomizedDelaySec=1h
```

- updater は**対象ユニット（agent-fleet）の外**なので、後段の restart で自分が殺されない（§0 の罠2を回避）。
- timer は **stage まで**。restart はしない＝走行中セッションは切れない。
- opt-out: `AF_NO_AUTOUPDATE=1`（install 時）／`systemctl --user disable --now agent-fleet-update.timer`（後から）。

## 4. apply — Console 操作／手動 restart

「stage 済みの新版を実際に動かす」＝主サービスの restart。ユーザーが都合の良いときに叩く。

### 4.1 CP エンドポイント（native gated）

launcher が `af start` の env に自己情報を渡す：`AF_SELF_PKG`（走行中パッケージ）／`AF_SELF_LINK`
（`~/.local/bin/af` symlink）／`AF_SYSTEMD_UNIT`（systemd unit の `Environment=AF_SYSTEMD_UNIT=%N` から継承）。
`AF_SELF_LINK` があるときだけ以下を登録する（Docker/ECS・dev では未登録＝Console 非表示）。

- `GET /api/update/status` → `{current, installed, restartRequired, systemd}`。
  `current`＝走行版（`buildVersion`）、`installed`＝symlink 先パッケージの `VERSION`。両者差＝`restartRequired`。
- `POST /api/update/apply` → restart を起動。
  - **systemd**: `systemd-run --user --collect --unit=agent-fleet-apply systemctl --user restart <unit>`。
    restarter を**切り離す**ので、自分（af-cp）が SIGTERM されても restart は完遂する（§0 の罠2の本命対処）。
  - **foreground**: 応答フラッシュ後に launcher を `syscall.Exec`（symlink は既に新版を指す）。ワークスペースの
    子プロセスは PID 同一のため生き残り、新 af-cp が reconcile する。
  - stage 済みが無ければ `409 no_staged_update`。

### 4.2 Console（設定 → 環境）

`EnvTab` の先頭に「Agent Fleet の更新」セクション。`GET /api/update/status` を読み、

- `restartRequired` のとき：`v<installed>`＋「再起動で適用」バッジと **「再起動して適用」ボタン**。押すと
  **実行中セッション数**（`sessions.filter(alive)`）を確認ダイアログに出し、承諾で `POST /api/update/apply`。
- そうでなければ「最新です」。

### 4.3 手動 restart

```bash
systemctl --user restart agent-fleet     # systemd users
# または foreground は Ctrl-C 後に af start
```

## 5. rootfs のカスケード

パッケージ更新で `rootfs.json` が新版になると、**apply 後の初回 start** で af-cp が新 rootfs を遅延取得
（旧版は保持）。走行中ワークスペースは次回 ws 再起動まで旧 rootfs。初回 DL コストは apply 直後に一度払う。

## 6. ピン運用

`AF_VERSION=<v>` を **主 unit と update unit の両方**の `Environment=` に置くと、`af update` はその版を目標にし、
到達後は無操作。自動更新をその版で止めたいときに使う。

## 7. 限界

- **apply は走行中セッションを中断する**（systemd の unit cgroup 停止）。だからこそ自動化せず、Console 警告付き／
  手動／idle に限定する。長寿命ワークスペースを restart から切り離す（別 slice/scope）拡張は将来課題。
- **native 専用**。Docker/ECS はイメージ更新が本筋なのでこの機構は無効（ルート未登録）。
- **systemd user バス不在**（linger 無しでログアウト時など）では timer が enable されない＝手動 `af update`。

## 8. 検証

- CP: `go build ./...` 緑、`update_test.go`（stagedVersion／status の restartRequired・up-to-date／apply の
  409）緑。
- launcher / install.sh: shellcheck 緑（新規コードに指摘なし）。
- Console: typecheck / i18n:lint / vitest(394) / vite build 緑。
- 残: 実フリート（実 systemd user・実 release tar）での stage→通知→Console apply の通し目視。
