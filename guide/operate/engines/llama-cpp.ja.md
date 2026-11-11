---
audience: "自分のネットワークの llama.cpp を、compose / native / docker の配備のセッションのチャットに使わせたい配備管理者"
source_of_truth: "Control Plane が起動時に読む環境変数。llama-server の起動引数は上流の `--help`"
---

# LAN の llama.cpp でチャットする

[English](llama-cpp.md) | 日本語

自分のネットワークで動く llama.cpp（llama-server）を、`compose` / `native` / `docker` の配備のチャットのエンジン（`llm`）として使わせます。
画像の ComfyUI と同じ形で、セッションは Control Plane を通して使います。

## 全体像

| あなたがやること | 起きること |
|---|---|
| GPU のあるマシンで llama-server を起動する | OpenAI 互換の API がポートで待ち受ける |
| `AF_LLM_URL`（と鍵）を設定して Control Plane を再起動する | Control Plane が外部管理の `llm` の行を 1 本作り、セッションのチャットをそこへ中継する |
| 推論エンジンの画面でモデルを登録する | メンバーがそのモデルでセッションを起動できる |

llama-server はあなたのもののままです。Control Plane は起動も停止もせず、費用も付けません。
使用量はこれまでどおり記録されます。Control Plane のゲートウェイが答えのトークン数を読み、メンバーの使用量に書きます。ストリーミングでも同じです。

## llama-server を起動する

ここのコマンドは LAN のマシンで実行します。ワークスペースの中ではありません。

| 入手のしかた | Linux | Windows |
|---|---|---|
| コンテナ | `ghcr.io/ggml-org/llama.cpp:server-cuda`（この配備が使うものと同じイメージ） | — |
| パッケージ | `winget install llama.cpp` | 同じ |
| 配布物 | `llama-bNNNNN-bin-ubuntu-cuda-*.tar.gz` | `llama-bNNNNN-bin-win-cuda-*.zip` と `cudart-*.zip`（CUDA ランタイムは別の配布物） |

CUDA 対応の Linux の配布物は、新しいビルド（b11065 以降）にしかありません。NVIDIA の GPU なら、コンテナがいちばん確実です。

起動コマンドの形:

```
llama-server -hf <repo>:<quant> --alias <モデルの id> -c <窓> -ngl 99 --jinja \
             --host 0.0.0.0 --port 8080 --api-key <鍵>
```

| 引数 | 理由 |
|---|---|
| `--alias` | `/v1/models` に出る名前になります。付けないと `-hf` / `-m` のパスがそのまま出て、起動メニューや記録に読めない名前が残ります。1 つのモデルを出すときは、要求の `model` が合っていなくても答えます。複数のモデルを出す router モードでは、名前が合わないと届きません |
| `-c` | 登録するモデルの `context_tokens` 以上にします。目安は 8192 以上です |
| `--host 0.0.0.0` | 付けないと loopback にしか応答しません |
| `--port` | 明示します。llama.cpp は既定のポートを変える予定を告知しています |
| `--api-key` | **付けないと、LAN に認証なしで開きます** |
| `--jinja` | 既定でオンですが、古いビルドのために明示します |

- gated な Hugging Face のリポジトリには `HF_TOKEN=...`（または `-hft <token>`）が要ります
- `--sleep-idle-seconds <N>` で、使われていないときにモデルを降ろして VRAM を返せます
- 動作を確かめたファミリーは Qwen3・GPT-OSS・Gemma です。`llama-3.1` は tool call が通らないので、ツールを使わない用途に限ってください

## Control Plane に場所を教える

Control Plane が起動のときに 1 回だけ読みます。変えたら Control Plane を再起動します。

| 変数 | 意味 |
|---|---|
| `AF_LLM_URL` | ポートまで含めた URL を、Control Plane から見た形で書きます（例: `http://192.168.1.20:8080`）。設定すると `llm` の行ができます |
| `AF_ENGINE_API_KEY_LLM` | 任意。`Authorization: Bearer` として送ります。llama-server の `--api-key` と同じ値にします |

| 形態 | 置き場所 |
|---|---|
| compose / docker | compose ファイルの隣の `.env` |
| native | `af start` の環境、または systemd unit の `Environment=`（[native の手順書](../../../deploy/native/README.md)） |

- `AF_LLM_URL` は近道です。`AF_ENGINES_JSON`（AWS ならエンジンのスタックの表）に `lifecycle: "external"` の `llm` 行を自分で書いても同じになります
- 鍵はどちらの書き方でも `AF_ENGINE_API_KEY_LLM` から読みます。`AF_LLM_API_KEY` という変数はありません

## モデルを登録する

**別の配備のエンジンを借りている間は、`llm` のモデルを登録できません。** 必ず次の順にします。

1. `AF_LLM_URL`（と `AF_ENGINE_API_KEY_LLM`）を設定して、Control Plane を再起動します。
2. 管理 → 「**推論エンジン**」で、`llm` の行にモデルを登録します。**id** は `--alias` に渡した名前にします。

ファイルを取り込む仕組みはありません。id・`base_model`・窓（`context_tokens`）を、llama-server の起動コマンドと合うように書きます。登録の画面は [推論エンジンと読み上げ](../07-engines.ja.md#モデルの取り込みとテナントとの関係) にあります。

## 借りていたエンジンから切り替えるとき

[別のデプロイのエンジンを借りる](borrow.ja.md) から切り替えると、次のことが変わります。

| | 借りていたとき | LAN の llama.cpp |
|---|---|---|
| 認証 | メンバーシップごとのトークン。メンバーシップを消せば止まる | 共有の bearer 1 本。1 人だけを締め出す方法はなく、回転させると全員が締め出される |
| 止まっているとき | 冷間起動を待つ。遅いがつながる | 待たない。すぐに `503 engine_unavailable` で失敗する |

## 困ったとき

### チャットがすぐに失敗する

Control Plane は自分が持っていないエンジンを待ちません。llama-server が答えないと、要求はすぐに `503 engine_unavailable` で断られます。本文には試した URL と health のパス（`/health`）が入ります。

1. そのマシンで llama-server が動いているか
2. Control Plane のマシンから、そのアドレスとポートに届くか
3. `AF_ENGINE_API_KEY_LLM` が `--api-key` と同じか

### パネルに出るもの・出ないもの

| 項目 | 表示 |
|---|---|
| warm | パネルを開いたときの生存確認です。`/models` を見ます（`/health` はモデルを読み込んでいなくても ok を返すため） |
| 稼働の履歴 | 出ません。外部管理の行には、履歴を取る制御ループがありません |
| 費用 | 付きません |

## メンバーが自分の llama-server につなぐとき

ここまでは配備全体の設定です。これとは別に、メンバーは設定 → エージェント → llama.cpp の「**自分の接続先**」で、自分の llama-server に直接つなげます（[lcpp を使う](../../user/agents/lcpp.ja.md)）。

- 設定すると、そのメンバーのセッションでは配備の `llm` エンジンより優先されます
- Control Plane を通らないので、テナント管理者がチャットの推論をオフにしていても止められません。メンバーが自分のネットワークのサーバーにほかのツールを向けるのを止められないのと同じで、意図した動作です

## 関連

- [自前の ComfyUI で画像を生成する](comfyui.ja.md)
- [別のデプロイのエンジンを借りる](borrow.ja.md)
- [推論エンジンと読み上げ](../07-engines.ja.md)
- [配備形態](../../ref/deploy-targets.ja.md)
