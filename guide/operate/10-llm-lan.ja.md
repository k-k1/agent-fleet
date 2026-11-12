---
audience: "すでに自分の網の中で llama.cpp（またはこれから立てる）を動かしていて、それをセッションのチャット補完に使わせたい配備管理者"
source_of_truth: "Control Plane が起動時に読む環境変数。llama-server 自身の起動引数は上流の `--help`"
---

# 10. 自前の llama.cpp でチャットの推論を担う

[English](10-llm-lan.md) | 日本語

この章の内容は、やりたいことごとのページに移りました。

| やりたいこと | ページ |
|---|---|
| llama-server を起動し、`AF_LLM_URL` で Control Plane に教え、モデルを登録する。借りていたエンジンからの切り替え、メンバー個人の接続先 | [LAN の llama.cpp でチャットする](engines/llama-cpp.ja.md) |
