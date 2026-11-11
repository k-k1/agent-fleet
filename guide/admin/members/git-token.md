---
audience: "a tenant administrator whose member's internal Git token may have leaked"
source_of_truth: "the Console's tenant settings, Operations › Members, the member detail's \"Rotate git token\""
---

# Replace a member's Git token

English | [日本語](git-token.ja.md)

When the token a member uses for the tenant's internal Git may have leaked, you can replace it with a new one.
The old token stops working at once, whoever holds it.

## How it works

| What you do | What Agent Fleet does |
|---|---|
| Press **Rotate git token** in the member detail | Issues a new token and invalidates the old one at once |
| If it says the workspace did not take it, have the workspace restarted | Hands the new token straight to a running workspace |

Members fetch from and push to the internal Git (the "Internal repos" tab) with a token their workspace holds.
Use this when the token may have got out of the workspace: pasted into a log, copied to another machine.

## Replace it

1. Open that member's card under **Tenant settings → Operations → Members**
2. Press **Rotate git token** under **Operations** (the screen is shown in [Remove a member](remove.md#the-steps))
3. Press **Rotate** in the confirmation

Nothing but the token changes. The rotation is recorded in the audit log.

## Read the result

| Message | What next |
|---|---|
| New git token issued and handed to the running workspace. | Nothing. The member carries on working |
| New git token issued. The workspace gets it at its next start. | Nothing. A stopped workspace gets it at its next start |
| New git token issued. The workspace is starting and gets it when the start finishes. | Nothing |
| New git token issued, but the running workspace did not take it. … | Restart the workspace. Until then, clones in it can neither fetch nor push |
| New git token issued. Internal git is not enabled on this deployment. | Nothing |

## A removed member's token

A removed member's token can be replaced as well. If their old token may be out there, replace it before inviting them back:
being invited again restores the same membership, and with it the same token.

## Replacing everyone's token

The tenant administrator's screen does one member at a time. To change every member's token at once, the deployment administrator changes the deployment's master key.

## Related

- [Use the built-in Git](../../user/code/internal-git.md)
- [Remove a member](remove.md)
