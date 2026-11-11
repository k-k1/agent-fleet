---
audience: "a deployment administrator who already runs llama.cpp somewhere on the network (or is about to) and wants sessions' chat completions to use it"
source_of_truth: "the environment variables the Control Plane reads at startup; llama-server's own `--help` for its startup flags"
---

# 10. Chat inference on your own llama.cpp

English | [日本語](10-llm-lan.ja.md)

This chapter has moved to pages organised by task.

| What you want to do | Page |
|---|---|
| Start llama-server, point the Control Plane at it with `AF_LLM_URL`, register the models; switching from a borrowed engine, a member's own connection | [Chat on a llama.cpp on your LAN](engines/llama-cpp.md) |
