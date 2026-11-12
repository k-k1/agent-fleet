---
audience: "a deployment administrator on AWS who wants the Control Plane's keys at rest held by AWS KMS"
source_of_truth: "`deploy/aws/ecs/cfn/30-ingress.yaml` and `deploy/aws/ecs/cfn/PARAMETERS.md` for the parameters; `af-cp rewrap-keys --help` for the command"
---

# Put the keys at rest on AWS KMS

English | [日本語](kms.ja.md)

This is for AWS deployments (`ecs` / `ecs-ec2`) only. The secrets the Control Plane seals and stores can be protected by an AWS KMS key instead of a key derived from `AF_MASTER_KEY`.
Disabling the KMS key then makes what it sealed unreadable at once. The parameters are in [Turn on optional features with stack parameters](../aws-ecs/options.md#keep-keys-at-rest-on-kms).

## How it works

| What you do | What happens |
|---|---|
| Deploy `10-data` with `CustodianKmsKey=create` | A symmetric KMS key is created and its ARN appears in the `CustodianKmsKeyArn` output |
| Deploy `30-ingress` with `CustodianKmsKeyArn` | The Control Plane seals with KMS. Values stored from then on are protected by KMS |
| Run `af-cp rewrap-keys` | Values stored before the switch are re-sealed with KMS too |
| Disable the KMS key | Everything sealed after the switch becomes unreadable, for every tenant at once |

A key of its own for each member's home is a separate setting on top of this ([Give each home its own key](home-keys.md)).

## What KMS protects

Each value is sealed with a fresh data key from KMS and bound to its tenant; it cannot be opened under another tenant's name.

- MCP connection headers, and the client secrets tenants registered for MCP servers
- Sign-in and Git OAuth client secrets
- The engines' Hugging Face, Civitai and ComfyUI keys
- Session handoffs and share proposals
- Inbox messages and their attachments
- The development sign-in's signing keys and client secrets
- The wrapped key of each workspace's credential store
- Where homes have one, each home's own credential-store key

## Turn it on

1. Deploy `10-data` with `CustodianKmsKey=create` and read the `CustodianKmsKeyArn` output. A symmetric KMS key of your own in the same account works too
2. Deploy `30-ingress` with `CustodianKmsKeyArn=<that ARN>`

The Control Plane gets `AF_KEY_CUSTODIAN=kms` and `AF_KMS_KEY_ID`. Its task role gets only `kms:GenerateDataKey` and `kms:Decrypt` on that key, and only for calls that carry the Control Plane's encryption context.

## What to know before you switch

| Point | Detail |
|---|---|
| `AF_MASTER_KEY` stays required | Values stored before the switch open with it, and other keys are derived from it. Keep it exactly as before |
| Nothing is re-sealed on its own | Values stored before the switch stay readable and stay protected by `AF_MASTER_KEY` alone. Until you [re-seal them](#re-seal-values-stored-before-the-switch), KMS protects only what is stored after the switch |
| No fallback | If KMS cannot be reached or refuses, sealing and opening fail with an error that names KMS. A value KMS sealed is never quietly handled with `AF_MASTER_KEY` |
| Opened keys are cached for 5 minutes | Change it with `AF_KMS_DATA_KEY_CACHE_TTL`; `0` turns the cache off. Disabling the KMS key takes effect within that time, not at once |
| Do not go back to `local` | While values sealed by KMS exist, a `local` Control Plane refuses them with an error that says why |

### Members' credentials are not shredded by this alone

The key of each workspace's credential store (`secrets.enc`) is derived from `AF_MASTER_KEY` and the member (`HMAC(master, userKey)`).

- KMS wraps the key only of a workspace whose key is stored for the first time after the switch. A workspace that already had one keeps the `AF_MASTER_KEY` wrapping, even after a restart
- Either way, anyone holding `AF_MASTER_KEY` can derive the key and open the store. Disabling the KMS key does not shred members' stored credentials
- Taking `AF_MASTER_KEY` out of that path is what [Give each home its own key](home-keys.md) does

## Disable the KMS key

| What you want | What you do | Result |
|---|---|---|
| Make every tenant's values unreadable | Disable the KMS key, or schedule its deletion | Everything sealed after the switch becomes unreadable to the Control Plane at once |
| Cut off one tenant | Add a statement to the key policy that denies `kms:Decrypt` when `kms:EncryptionContext:af:key_ref` is that tenant's id | Only that tenant's values stop opening. It is a revocation an administrator of the key can undo, not a shred |

Disabling the key makes the whole deployment unreadable, so restrict who can administer the KMS key.

## Re-seal values stored before the switch

`af-cp rewrap-keys` re-seals, under KMS, every value sealed with `AF_MASTER_KEY` before the switch: everything in [What KMS protects](#what-kms-protects). Afterwards, disabling the KMS key makes those unreadable too.

1. Check that the Control Plane runs with `AF_KEY_CUSTODIAN=kms`. The command refuses to run under `local`, and a `local` Control Plane cannot open what the command writes
2. With the Control Plane's own environment (the same `AF_MASTER_KEY`, `AF_KMS_KEY_ID`, database settings and task role), run it with `--dry-run` first. On ECS, run it as a one-off task of the Control Plane's task definition; the command to paste is in [Run af-cp commands against the Control Plane's database](../aws-ecs/options.md#run-af-cp-commands-against-the-control-planes-database)
3. Run it without `--dry-run`. Repeat until it exits with status 0
4. When it is done, confirm with `--dry-run` once more

`--dry-run` changes nothing and does not call KMS. For each place it prints how many values are on KMS, how many are in the old format and how many are stored unsealed. Unsealed values were written by a Control Plane that had no `AF_MASTER_KEY`; the command leaves them alone.

### How it rewrites

- Each value is opened with `AF_MASTER_KEY`, sealed by KMS and opened again through KMS to check it reads back, and only then written
- It writes one row at a time, and skips a row that changed meanwhile
- An interruption, a KMS error or a refused `Decrypt` stops it before that row is written. Every row is left either in the old format or on KMS, and both open
- After the rewrite it looks at every place again without changing anything. The exit status comes from that final check
- A run with nothing left to do changes nothing

| Exit status | Meaning |
|---|---|
| Exits with status 0 | When it last looked, nothing was in the old format and every row could be read. The same holds for `--dry-run` |
| Exits with status 1 | Something is still in the old format, a row could not be read or written, or the run stopped. The log names the place and the row id, never a value |
| Exits with status 2 | Configuration: not `kms`, no `AF_MASTER_KEY`, no key id, no database |

`--dry-run` therefore exits with status 1 before the rewrap and with status 0 after it.

### Run it while no administrator is editing

The Control Plane can keep running. But these saves write back the value exactly as they read it:

- Saving a sign-in provider or a Git OAuth app without retyping its secret
- Saving the ComfyUI panel without retyping its key

A save that read the old value before the command rewrote it puts the old value back. The final check catches one that lands during the run; one that lands after it does not.

### `AF_MASTER_KEY` still cannot be dropped afterwards

The master key alone no longer opens the values the command re-sealed. Even so, `AF_MASTER_KEY` cannot be dropped and must not change:

- Credential-store keys can still be derived from it. The command moves only the wrapped copy of the key to KMS
- A workspace started for the first time still gets its key from the same derivation
- The tokens that workspaces use to call back into the Control Plane (Git credentials, Git OAuth, memos, schedules, engines, branch rules, AWS and Google Cloud profiles, documents, MCP) are signed with keys derived from it
- The Control Plane refuses to start with `AF_KEY_CUSTODIAN=kms` and no `AF_MASTER_KEY`

## Encrypt the `AF_MASTER_KEY` parameter with your own key

This is separate from KMS custody above. On ECS the `AF_MASTER_KEY` value sits in the SSM SecureString `/af-cp/master-key`, encrypted with the AWS-managed `aws/ssm` key unless you say otherwise.

1. Set `20-platform`'s `MasterKeyKmsKeyArn` to your key's ARN
2. Then re-put the parameter with `--key-id`

Keep that order. ECS decrypts the value with the **execution role**, so the key must admit the execution role, not the task role. The template grants `kms:Decrypt` on that one key, via SSM only. The steps, the key policy and the back-out are in `deploy/aws/ecs/cfn/PARAMETERS.md` under "`MasterKeyKmsKeyArn`".

It changes only who can read the stored value. The value itself must not change ([Handle the deployment keys](../secure/deployment-keys.md#the-two-keys)).

## Related

- [Give each home its own key](home-keys.md)
- [Rotate the bridge tokens](bridge-tokens.md)
- The threats this answers are in [Understand the threat model and residual risks](../secure/threat-model.md#prepare-for-the-remaining-risks)
- [Turn on optional features with stack parameters](../aws-ecs/options.md#keep-keys-at-rest-on-kms)
