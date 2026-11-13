---
audience: "a tenant administrator bringing someone into the team"
source_of_truth: "the Console's tenant settings, Operations › Members, \"Add member\""
---

# Add a member

English | [日本語](add.ja.md)

Put an email address on the roster and that person becomes a member of this tenant when they sign in, with a workspace ready for them.
You can add someone who has never signed in yet.

## How it works

| What you do | What Agent Fleet does |
|---|---|
| Put the email address on the roster | Makes a user key from the address and puts it on the roster |
| Have the person sign in | Gives them a workspace at their first sign-in, based on the roster |

## Put someone on the roster

1. Open **Tenant settings → Operations → Members** and go to **Add member** at the bottom of the roster
2. Enter the email address the person signs in with in the "email" field
3. Press **Add**

![The "Add member" form under the roster: the "email" and "or user_key" fields and the "Add" button](../../assets/admin-add-member-en-light.webp)

- You can add plain members (`member`) only. Adding administrators is the deployment administrator's job ([Roles and what they may do](../../ref/roles.md#adding-administrators))
- Point the person you added at [Find what you want to do](../../user/README.md)
- When the sign-in rules set **Invite domains**, only addresses in those domains can be added. The refusal names the domains allowed

## Email addresses and user keys

Internally a member is told apart by a **user key** (`user_key`). It is made from the email address you enter, so you normally need not think about it.

- Roster cards, the audit log and session lists show the user key. Knowing which key is which person makes them easier to read
- The same person in another tenant has a separate membership and a separate workspace
- You may type a key directly in the "or user_key" field. It becomes the home directory's name, so only lowercase letters and digits joined by single hyphens, 40 characters at most, are accepted
- A key the server made itself (the one shown on a card) is accepted even past 40 characters

## Invite-only and auto-join

How a new person gets into this tenant is decided by the deployment's settings and the sign-in rules.

| Mechanism | Who gets in | Decided by |
|---|---|---|
| Invite-only (the default of a new install) | Only people added to the roster | deployment administrator |
| Auto-join | Everyone allowed to sign in joins the default tenant at first sign-in | deployment administrator |
| Auto-join domains (sign-in rules) | People with an address in the domain join this tenant at first sign-in | deployment administrator |

- Someone already on a roster gets in according to the roster, whatever the mechanism
- Under invite-only, someone not added still signs in and sees a "You haven't been invited yet" page. It shows the address they signed in with; when they ask to be added, use that address. It is not necessarily their display name or the address they usually give out
- Which mechanism applies can be read under **Tenant settings → Sign-in → Sign-in rules**. It is read-only for you
- Which addresses may sign in at all is also the deployment administrator's decision. Someone outside that range cannot sign in even when you add them

## When it goes wrong

### Adding is refused with 409

| Situation | What to do |
|---|---|
| The person signs in through another tenant's own sign-in method (`identity_bound_to_other_tenant`) | They cannot be added to this tenant |
| The user key belongs to someone in another tenant and you also entered an email address (`identity_in_other_tenant`) | Leave the email empty and add by user key only |

Adding never rewrites the email address of someone who already exists. An address is attached to a new person only.

### They cannot sign in with this tenant's method

While another tenant still has the person on its roster, they may not get in through this tenant's own sign-in method.
Have that tenant remove them first (removed members do not count), then have them sign in again.

### You want a removed member back

Add the same email address again and they are back. Their workspace and home were kept.

- An administrator comes back as a plain member
- Schedules that were paused while they were away resume

## Related

- [Check on your members](roster.md)
- [Remove a member](remove.md)
- [Choose how people sign in](../access/signin-methods.md)
