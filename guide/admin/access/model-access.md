---
audience: "a tenant administrator who decides which model routes the team's members may use, and the deployment administrator who sets the layers above"
source_of_truth: "the Console's tenant settings, Operations › Model access, and the admin modal's Deployment › Model access"
---

# Choose which models members may use

English | [日本語](model-access.ja.md)

Decide which model routes your members may use: for example no subscriptions, only Amazon Bedrock, or every metered key except one provider.
What you forbid disappears from your members' screens: the launch dialog, the agents and credentials pages, image generation and the assistant.

## How it works

A **route** is where an agent's model calls go and with which credential: a Claude subscription, an Anthropic API key, Amazon Bedrock through the member's AWS profile, and so on.
Every route has a **class of credential** and a **destination**, and a rule is written with those two:

| Class of credential | Means |
|---|---|
| Subscriptions and logins | A plan or account login (a Claude or ChatGPT subscription, Copilot, Cursor, Kiro, an opencode.ai account) |
| Members' API keys | A metered key the member stores (Anthropic, OpenAI, Gemini, OpenRouter and so on) |
| Members' cloud accounts | Amazon Bedrock through the member's own AWS profile |
| Provided by the deployment or tenant | A route the Control Plane holds the credential for |
| Self-hosted | This deployment's engine, and self-hosted endpoints you approved |

The destination is who receives the prompts: Anthropic, OpenAI, Google, Amazon Bedrock, this deployment's engine, an approved self-hosted endpoint, and the other vendors.

A rule is set in layers, and **each layer can only narrow the one above it**:

| Layer | Who sets it |
|---|---|
| Deployment ceiling | The operator, in the deployment's configuration. Read-only on screen |
| Deployment rule | The deployment administrator, for every tenant |
| Tenant cap | The deployment administrator, for one tenant |
| Tenant rule | You, the tenant administrator |
| A member's own switches | Each member, for themselves |

A route is usable only when every layer allows it. Nothing you allow can reopen what a layer above forbids.

## Set your tenant's rule

1. Open **Tenant settings → Operations → Model access**
2. Read **What the deployment allows**: the three layers above yours, and the routes they leave to your rule
3. Under **This tenant's rule**, set:
   - **Classes of credential**: **All**, or **Only these** with the classes you allow
   - **Destinations**: **All**, or **Only these** with the destinations you allow
   - **Routes allowed as exceptions**: a route your classes or destinations would forbid, which you still allow. "No subscriptions except Copilot" is the class rule plus Copilot here
   - **Routes denied**: a route your classes and destinations would allow, which you still forbid. A route in both lists is denied
4. Press **Save**

**Only these** with nothing ticked allows nothing; it is not the same as **All**.
When the Control Plane refuses a rule, the reason is shown above the button and your edits stay, so you can correct the one value.
Every save is recorded in the audit log with the rule before and after ([Read the audit log](../audit.md)).

## Approve a self-hosted endpoint

A member's own llama.cpp, or another endpoint they type in, cannot be vouched for by Agent Fleet: under any restriction it is refused unless its host is approved.

1. In **Self-hosted endpoints this tenant approves**, enter one exact host per line (no wildcard, port or scheme)
2. Press **Save**

A host is approved only while every layer above lets it through, and a vendor's host is never approvable.
**Approved self-hosted endpoints, in effect** lists each host you entered and, for one that is not approved, which layer keeps it out.
Allowing the class and the destination **Self-hosted** is what lets members use an approved endpoint.

## Find out why members cannot see something

**What the policy hides**, at the bottom of the page, lists the agents, image providers and assistant chat backends your members no longer see because none of their routes is allowed.
Each one names the layers that hide it, and each of its routes names its own: an agent can be hidden by two layers at once, one forbidding each of its routes.

| What the list shows | Means |
|---|---|
| Items with their layers | Those are hidden from every member of the tenant. To bring one back, every layer named for at least one of its routes has to allow it |
| **These layers hide nothing.** | No agent, image provider or chat backend is hidden by the layers above |
| A line saying the list could not be read | The Control Plane did not answer it. This is not the same as "nothing is hidden" |

An agent that keeps at least one allowed route is not listed: members see it with only the routes left.
Members' own switches (an agent a member turned off for themselves) are not in this list, because they live in the member's workspace and the Control Plane cannot see them.

## When a change applies

- A change reaches members' new launches within 30 seconds
- A running session on a route that became forbidden is stopped within 30 seconds plus one reconciler tick
- A stored credential whose every route is forbidden is kept, not deleted, and is hidden from the member. Loosening the rule makes it usable again without signing in again

## What this cannot stop

A member who signs in to a model by hand from a terminal in their workspace cannot be stopped.
This rule binds Agent Fleet's own launches, not what a member runs by hand.

## For the deployment administrator

The admin modal's **Deployment → Model access** has the layers above the tenants':

- The **Deployment ceiling (operator)**, read-only, with the value the deployment was started with
- The **Deployment rule**, for every tenant
- The **Tenant cap** of each tenant: choose the tenant, then edit its cap. That tenant's own rule is shown below it, read-only, followed by what the policy hides from that tenant

Both editors refuse a rule that would allow something the ceiling forbids.
Their endpoint list names the host patterns a tenant may approve: an exact host or `*.example.com`. **Not narrowed** leaves the layer above in charge; **Only these** with no pattern lets no host be approved.

## Related

- [Read the audit log](../audit.md)
- [Choose who may use the inference engines and which models](../resources/engines.md)
