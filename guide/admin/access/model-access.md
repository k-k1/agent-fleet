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

The page has a tab for each part: **Overview**, **Restrictions** and, where the deployment offers provided routes, **Provided models & budgets**. The sentences on when a change applies stay above the tabs. Switching tabs keeps what you have not saved, and a tab with unsaved changes is marked with ●.

1. Open **Tenant settings → Operations → Model access**
2. On **Overview**, read **Who restricts what**: one line for each of the three layers above yours (the operator, and your deployment's administrator for all tenants and for this tenant), then your own
3. On **Restrictions**, under **This tenant's rule**, set:
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

1. On **Restrictions**, in **Self-hosted endpoints this tenant approves**, enter one exact host per line (no wildcard, port or scheme)
2. Press **Save**

A host is approved only while every layer above lets it through, and a vendor's host is never approvable.
On **Overview**, the **This tenant** line of **Who restricts what** lists each host you entered with ✔ or ✖ and, for one that is not approved, who keeps it out.
Allowing the class and the destination **Self-hosted** is what lets members use an approved endpoint.

## Find out why members cannot see something

**What members can use**, on **Overview**, has one row for each agent, image provider and assistant chat backend: ✔ when members can use it on at least one route, ✖ when it is hidden from them because none of its routes is allowed.
Open a row to see its routes, each with ✔ or ✖ and, for one that is not allowed, who stops it. An agent can be stopped by two people at once, one forbidding each of its routes. Hover a route's name for the id the Control Plane's refusals use.

| What the list shows | Means |
|---|---|
| ✔ with "Usable on 3 of its 4 routes" | Members see it with only the routes left |
| ✖ with who stops it | It is hidden from every member of the tenant. To bring it back, everyone named for at least one of its routes has to allow it |
| Only the hidden items, with a line saying so | The Control Plane is older and does not say what each agent can use |
| A line saying the list could not be read | The Control Plane did not answer it. This is not the same as "everything is usable" |

Members' own switches (an agent a member turned off for themselves) are not on this list, because they live in the member's workspace and the Control Plane cannot see them.

## Set members' budgets for provided routes

Some deployments pay for Amazon Bedrock themselves and offer it to tenants as a **provided route**. Where the deployment does not, the **Provided models & budgets** tab is not on the page.

The deployment administrator grants it to your tenant with a monthly ceiling. Under that, you decide who may use it and how much:

1. Under **Who may use them**, choose **Every member** or **Granted members only**
2. Enter **Members' monthly budget (default, dollars)**, then press **Save**
3. In **Each member's grant and budget**, tick a member to grant them, or enter a budget of their own (leave it empty for the default), then press **Save** on that row

A member whose budget is zero cannot use the route. Amounts are estimates at Bedrock's list price and reset at the start of each month (UTC).
A budget so small that no single call fits is refused, with the reason beside the button; one that fits only calls with a small output is saved with a warning.

## When a change applies

- A change reaches members' new launches within 30 seconds
- A running session on a route that became forbidden is stopped within 30 seconds plus one reconciler tick
- While a workspace cannot reach the Control Plane, a change waits for its next successful pull, so the delay is as long as the outage. Where the member was already restricted or the deployment has a ceiling, new launches are refused after 15 minutes without a successful pull; running sessions are left alone
- A stored credential whose every route is forbidden is kept, not deleted, and is hidden from the member. Loosening the rule makes it usable again without signing in again

## What this cannot stop

A member who signs in to a model by hand from a terminal in their workspace cannot be stopped.
This rule binds Agent Fleet's own launches, not what a member runs by hand.

## For the deployment administrator

The admin modal's **Deployment → Model access** has the layers above the tenants', on four tabs:

- **Overview**: the **Deployment ceiling (operator)**, read-only, with the value the deployment was started with
- **Restrictions for all tenants**: the **Deployment rule**, for every tenant
- **Per tenant**: the **Tenant cap** of each tenant: choose the tenant, then edit its cap. That tenant's own rule is shown below it, read-only, followed by **What the policy hides** from that tenant: each hidden agent, image provider or chat backend with the layers that hide it
- **Provided models**, where the deployment pays for Bedrock: the **Provided-route source**, the IAM role the stack created, its Region and models, and **Use this source**. Switching it off keeps its spend and sessions on record
- On **Per tenant**, under each tenant, the **Provided-route grant**: **Grant to this tenant** with the **Tenant's monthly ceiling (dollars)**. A grant needs a ceiling above zero

The deployment rule and tenant cap editors refuse a rule that would allow something the ceiling forbids.
Their endpoint list names the host patterns a tenant may approve: an exact host or `*.example.com`. **Not narrowed** leaves the layer above in charge; **Only these** with no pattern lets no host be approved.

## Related

- [Read the audit log](../audit.md)
- [Choose who may use the inference engines and which models](../resources/engines.md)
