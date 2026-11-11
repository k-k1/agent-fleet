---
audience: "a deployment administrator on AWS who wants the Control Plane's keys held by KMS, with or without a key of its own for each home"
source_of_truth: "`deploy/aws/ecs/README.md` and `deploy/aws/ecs/cfn/PARAMETERS.md` for the parameters; `af-cp rewrap-keys --help` and `af-cp home-dek-status --help` for the commands"
---

# 12. Keys at rest: AWS KMS and a key for each home

English | [日本語](12-keys.ja.md)

This chapter has moved to one page per task.

| Page | What you can do |
|---|---|
| [Put the keys at rest on AWS KMS](keys/kms.md) | Protect the secrets the Control Plane seals with KMS and re-seal values stored before the switch. Encrypt the `AF_MASTER_KEY` parameter with your own key |
| [Give each home its own key](keys/home-keys.md) | Seal members' credential stores under a key of each home's own, and know what disabling the KMS key makes unreadable |
| [Rotate the bridge tokens](keys/bridge-tokens.md) | Invalidate workspace tokens for one member or for everyone at once |
