# 0025. ホスト常駐 `af` の自動更新 — stage は自動 / apply（再起動）は明示

[English](0025-native-auto-update.md) | 日本語

- 状態: **採用・実装済み**。設計は [docs/42](../log/42-native-auto-update.md)。
- 関連: [docs/35](../log/35-packaging.md)（パッケージング／install.sh／`af` ランチャ）/
  [docs/34](../log/34-native-runtime.md)（native ランタイム）

## 背景

native パッケージ（`AF_RUNTIME=native`）は WSL2 などのホストで `af start` を常駐させて使う。
更新は「install.sh の再実行＝版ディレクトリを差し替え、`~/.local/bin/af` symlink を張り替え」で
手動に閉じていた。ユーザーは「放っておいても新しくなる」自動更新を望むが、native は systemd **user**
サービスで走ることが多く、更新を素直に自動化すると2つの構造的問題を踏む。

1. **走行中の af-cp はディスクを差し替えても切り替わらない。** 版 dir を swap し symlink を張り替えても、
   既に起動済みの `af-cp`（と読み込み済み console 資産）はそのまま。**ユニットを restart するまで新版は効かない。**
2. **自ユニットは自分を安全に再起動できない。** af-cp 内から `systemctl --user restart` を呼ぶと自分（main PID）に
   SIGTERM が飛び、restart コマンドが途中で殺されうる。

加えて、このフリートは **走行中のエージェントセッションを不用意に殺さない**ことを重視してきた。restart は
af-cp と配下ワークスペースを落とす（systemd では unit の cgroup ごと停止）ため、無条件の自動 restart は取れない。

## 決定

**「更新の取得（stage）は自動・既定 ON、適用（apply＝再起動）は明示（Console 操作／手動／idle）」に分離する。**

- **stage は `af update` に閉じる**: install.sh のロジックを `af` ランチャに内蔵。latest 解決（`releases/latest`
  リダイレクト＝API レート非依存）→ DL → **sha256 検証**（release の SHA256SUMS）→ 版 dir を staging→atomic swap →
  `~/.local/bin/af` 張り替え。**走行中 af-cp は無改変**。`AF_VERSION` ピンを尊重（超えない）。`WS_DATA` 不変。
- **既定 ON は timer による自動 stage**: install.sh が systemd user 利用可なら `agent-fleet-update.timer`＋
  `.service`（`ExecStart=af update --yes`）を enable。updater は**対象ユニットの外**なので、後段の restart で
  自分が殺されない（＝問題2の回避）。opt-out は `AF_NO_AUTOUPDATE=1`。timer は **stage まで**で restart しない。
- **apply は明示**: CP に `GET /api/update/status`（走行版 `buildVersion` vs symlink 先の `VERSION` を比較）と
  `POST /api/update/apply` を追加。Console（設定→環境）が「再起動で適用」を出し、**実行中セッション数を警告**して
  から restart する。restart は systemd 下では `systemd-run --user --collect systemctl --user restart <unit>`
  で**切り離して**実行（自分が SIGTERM されても restart は完遂）。foreground は launcher を `syscall.Exec` で
  置換起動（symlink が新版を指す）。unit 名は sample の `Environment=AF_SYSTEMD_UNIT=%N` で af-cp に渡る。
- **rootfs は自然カスケード**: パッケージ更新で `rootfs.json` が新版になると、apply 後の初回 start で af-cp が
  新 rootfs を遅延取得（旧版は保持）。走行中 ws は次回 ws 再起動まで旧 rootfs。

### 捨てた選択肢

- **検知したら即自動 restart**: 最もシンプルだが走行中の全 workspace/agent セッションを問答無用で切る。
  false-idle/session-kill を重んじる方針に反するため不採用。既定は stage→通知に留める。
- **af-cp 内プロセスで in-place 自己更新（re-exec）**: systemd main-PID の in-place exec は扱いが繊細で、
  launcher/console 資産/rootfs ハンドシェイクをまたぐと脆い。stage は外部（timer/CLI）、apply は明示 restart に分離。
- **install.sh が主サービスまで自動生成・enable**: install がホストで af を勝手に起動する副作用が大きい。
  主サービスは従来どおり README の手順に委ね、timer（stage 専用）だけを既定 ON にする。

## 帰結

- 追加は launcher `af`（`update` サブコマンド＋dist メタ＋自己情報 env 受け渡し）／`build.sh`（`dist.json` 生成）／
  `install.sh`（timer 既定 ON・opt-out）／native README（systemd sample に `AF_SYSTEMD_UNIT=%N`・更新節）／
  CP `update.go`（status/apply・native gated）／Console `EnvTab`（更新セクション）。既存 start/reset/status は無改造。
- **native 専用**: status/apply ルートは launcher が渡す `AF_SELF_LINK` があるときだけ登録。Docker/ECS（イメージ更新）と
  dev ビルドでは未登録＝Console は自動的に非表示。
- **ピン運用**: `AF_VERSION` を主 unit と update unit の両方に置くと、`af update` はその版を目標にし、到達後は無操作。
- **限界（意図）**: systemd の unit cgroup 停止のため、apply（restart）は走行中セッションを中断する。だからこそ
  apply は自動化せず、Console 警告付き／手動／idle に限定する。長寿命ワークスペースを restart から切り離す
  （別 slice/scope）拡張は将来課題。
