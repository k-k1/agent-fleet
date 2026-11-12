---
audience: "a deployment administrator who wants workspace tokens that may have leaked invalidated"
source_of_truth: "`control-plane/bridge_epoch.go`, `control-plane/bridge_epoch_cmd.go`, `POST /api/admin/rotate-bridge-tokens`"
---

# Rotate the bridge tokens

English | [日本語](bridge-tokens.ja.md)

Invalidate the tokens a workspace uses to call the Control Plane, for one member or for everyone at once.
Rotation works the same on every deployment form. The all-at-once rotation is mostly for AWS deployments (`ecs` / `ecs-ec2`), to retire tokens left in old task definitions and logs.

## How it works

| What you do | What happens |
|---|---|
| Rotate one member's tokens | That member's epoch advances and the old tokens stop verifying. Their running workspace shows "**Restart needed**" |
| Run `af-cp advance-bridge-epochs --yes` | Every member's epoch advances, and the tokens in every running workspace die at once |
| Raise `AF_TOKEN_SIGN_VERSION` | The token signing keys change, and every token dies without touching a stored credential |
| Restart the workspace | It receives new tokens |

## The tokens and how they are delivered

A workspace calls the Control Plane with three credentials:

- The per-membership bridge tokens
- The internal git token
- The dev IdP client secret

They reach a workspace as SSM SecureStrings on ECS, a mode-0600 env file on docker, or a Secret on Kubernetes. They are never in the task definition's `environment`.

Each bridge token carries the membership's **epoch**, and only tokens of the current epoch verify. Rotating means advancing that epoch. With the deployment's Bedrock route on (`AF_PROVIDED_ROUTES=1`), the route issuing token `AF_ROUTE_ISSUE_TOKEN` travels the same way from 0.44.0 and is bound to the same epoch, so rotating retires it too.

## Rotate one member's tokens

A tenant administrator calls `POST /api/admin/rotate-bridge-tokens` with the body `{"tenant_slug": "…", "user_key": "…"}`. The Console has no button for it.

- That member's epoch advances, and the audit log records `membership.rotate_bridge_tokens`
- Their running workspace keeps the old tokens until it is restarted. The workspace bar shows "**Restart needed**"
- Removing a member and re-activating the same membership advance the epoch too

## Rotate everyone's tokens once

After you upgrade to the release that moves the tokens out of the task definition's `environment`, tokens minted before it may still sit in old task-definition revisions, CloudTrail and logs. The upgrade itself invalidates nothing: today's tokens are epoch 0, which every workspace already holds.

1. Check that every workspace runs that release
2. Run `af-cp advance-bridge-epochs` against the Control Plane's database. It only counts the memberships that would move and changes nothing
3. In a quiet window, run `af-cp advance-bridge-epochs --yes`. It advances the bridge and internal git token epochs once
4. Restart the running workspaces. The ones showing "**Restart needed**" are the ones to restart

On ECS, run `af-cp` as a one-off task of the Control Plane's task definition. The command to paste is in the [ECS runbook](../../../deploy/aws/ecs/README.md#running-af-cp-commands-against-the-control-planes-database).

## Rotate the signing keys

`AF_TOKEN_SIGN_VERSION` selects the root the token keys derive from.

| Value | Root |
|---|---|
| Unset, or `0` | The deployment's `AF_MASTER_KEY` |
| `1` and up | A root of their own, derived from `AF_MASTER_KEY`. The token keys are separate from the keys that seal stored credentials |

- Changing the value invalidates every token without touching a stored credential. Restart every workspace after each change
- A value that is not a non-negative integer stops the Control Plane from starting

## Related

- [Put the keys at rest on AWS KMS](kms.md)
- [Give each home its own key](home-keys.md)
- [Upgrade](../run/upgrade.md)
