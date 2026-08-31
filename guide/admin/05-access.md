---
audience: "a tenant administrator deciding how people get in"
updated: "2026-08"
---

# 05. Who may sign in, and from where

English | [日本語](05-access.ja.md)

Four screens decide who reaches your tenant and how. Two are yours to change, one is
read-only for you, and one is yours to register but somebody else's to approve.

| Screen | Yours? |
|---|---|
| **Sign-in methods** | register and edit — a deployment administrator approves |
| **Login rules** | read-only |
| **Allowed networks** | yours |
| **Integration OAuth apps** | yours |

## Allowed networks

By default your tenant is reachable from anywhere the deployment itself is reachable
from. **Source networks** narrows that to a list you write: comma-separated CIDR ranges
or single addresses, IPv4 or IPv6. Leaving it empty means no restriction.

The screen shows **your address, as this deployment sees it** — and that is the value a
rule is matched against, *not* what your browser believes its address is. Check it
before you save. If the deployment cannot determine the source of the request, the
screen says so and the rules cannot be applied; that is a deployment-side setting
(how many proxies to trust), so raise it with your deployment administrator rather
than guessing at a range.

> **Lock yourself out and you cannot fix it from here.** Add the range you are
> currently connecting from *before* you save, and confirm it against the address on
> the screen. Recovering from a wrong entry needs a deployment administrator.

Two more things worth knowing:

- The restriction is applied **after** authentication, not instead of it. It narrows
  who may use a valid session; it is not a substitute for sign-in.
- It applies to your tenant. Someone who also belongs to another tenant reaches that
  one under its own rules.

## Integration OAuth apps

Members can always connect GitHub or Bitbucket by pasting a token. Registering your
tenant's **own** OAuth app adds the nicer path: a **"Connect with OAuth"** button on
that provider, so nobody has to mint a token by hand.

To register one:

1. Create an OAuth app in your own organisation on the provider's side. The screen
   tells you where.
2. Copy the **callback URL** the screen shows into that app. This has to match exactly,
   or approval comes back with an error the provider words unhelpfully.
3. Paste the **client_id** and **client_secret** here. Bitbucket calls the same two
   things Key and Secret.

The secret is **encrypted on save and never shown again**. When you edit the
registration later, leaving the secret field empty keeps the stored one — fill it in
only when you actually want to change it. **Remove registration** takes the OAuth
button away again; members who connected with a token are unaffected.

This is per provider and per tenant. It does not widen what members may do with the
provider — that is decided by the app's own scopes and by each member's permissions
there.

## Sign-in methods, and login rules

**Sign-in methods** is where you register your own company's IdP, or a GitHub
organisation, as a way into this tenant. Registering is not enough: a deployment
administrator approves it, and a later change — adding an organisation, changing how
the same account is recognised — sends the row back for approval.

**Login rules** shows the join mode and the domains in force. It is read-only for you.
It exists so you can see *why* an invitation was refused without having to ask anyone.

Both screens have sharp edges that are easier to hit than to notice, and they are
covered in detail in the deployment administrator's chapter on sign-in
([operate/](../operate/README.md)) — most importantly:

- **Narrowing what your tenant accepts can lock out people who belong to another
  tenant too.** The same address at a different IdP is a *different login*. Leave their
  method accepted and just stop showing its button instead.
- **Clearing "show button" keeps a method accepted.** People already using it still
  can; it just stops appearing on the sign-in page. You cannot clear it on every
  method — a sign-in page with no buttons is a dead end, so the setting is ignored.
- If someone holds both accounts, they can link the second method to their own account
  themselves, under their personal account settings. Nothing for you to do.

---

Related: [ref/roles.md](../ref/roles.md) for who may do what ·
[01 Members](01-members.md) for who is in the tenant at all
