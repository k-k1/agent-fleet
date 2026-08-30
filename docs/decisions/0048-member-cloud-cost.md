# 0048. Show per-member AWS cost as **actual spend** derived from cost allocation tags (no apportionment; shared cost is not distributed)

English | [日本語](0048-member-cloud-cost.ja.md)

- Status: **adopted** (2026-08-17). The record of the investigation is [docs/67](../log/67-member-cloud-cost.md).
- See also: [0045-ec2-persistent-workspace.md](0045-ec2-persistent-workspace.md) decision 8
  (one slot is dedicated to one user, so instance hours fall directly on a person) and
  decision 21 (the precedent of **not putting an item on screen that does not work** and **not adding
  IAM just for a display** — which this ADR explicitly overturns) /
  [0044-workspace-sizing.md](0044-workspace-sizing.md) decision 3 (**a feature shipped off by default
  might as well not exist**) /
  [0029-usage-accounting.md](0029-usage-accounting.md) (the **token** ledger. This ADR is AWS
  infrastructure cost — a different axis with a different name) /
  [0043-login-idp.md](0043-login-idp.md) decisions 24/25 (what reaches outside the tenant belongs to
  the operator)

## Context

The CP already tags AWS resources with `af-membership` and others, but **they are all Inactive as
cost allocation tags** and the bill is not attributed to anybody. All the Console has is showback in
running seconds (`usage.go`, whose header reads `No external billing.`) and the token ledger — **there
is not a single $ anywhere.**

## Decision 1 — activate the cost allocation tags **before** designing the rest

There is no backfill, so delaying by a day loses that day's actual spend permanently. With the user's
confirmation, `ce:UpdateCostAllocationTagsStatus` made **`af-membership` / `af-role` / `af-pool` /
`af-slot-size` Active** (2026-08-17T05:26:16Z).

⚠️ **`af-workspace` is not activated.** Its value is `af-ws-<derived from the email>`, which would put
personal data into the billing data (CUR / CE / the invoice CSV). The opaque `af-membership` id is
enough as an aggregation axis, and the CP can resolve it to a member in its own database.

⚠️ **As a system, this feature can show nothing before 2026-08-17.** The reason is stated on screen.

⚠️ **Activation happens once per AWS account, with no involvement from tenants or members
whatsoever.** The unit is the `TagKey` alone (the API has no dimension for values), so one entry for
`af-membership` covers everyone added from now on, and one for `af-tenant` covers every tenant created
from now on. Tenant admins do not touch AWS in the first place (the line from 0043 decisions 24/25).
Under Organizations, only the management (payer) account can activate them.

## Decision 2 — show **actual spend (Cost Explorer) only**. Do not convert running seconds into money

An apportioned estimate (running seconds × an operator-declared unit price) **goes on lying silently**
unless the unit price is kept up to date. Once actual spend is available, putting an estimate next to
it guarantees a discrepancy and guarantees the question "which is the real one?".

- **`usage_daily` (running seconds) stays but is not turned into money** — it is the only number that
  means anything on docker and native.
- **On screen they are separate panels.** Hours and $ never appear on the same card.
- As a consequence, the period before the tags were activated is **"there are running hours but no
  cost"**. That is a fact rather than a gap, so it is stated as such.

## Decision 3 — make the billing axis work: `af-membership` on slot instances, `af-tenant` (the slug) on every resource

Reading the real deployment showed that **instances have no `af-membership`** (only `af-role=slot`,
`af-pool` and `af-slot-size`). 91% of the cost we want to attribute is those instance hours, so
**activating the tags alone does not achieve the main goal.**

- The tag is applied at the point where attaching home succeeded, and removed after a successful
  detach in `releaseSlot` and in `quarantineSlot`. The cleanup path picks up and removes "a box that
  has `af-membership` but no volume attached" (what is missed when the CP dies).
- **The existing logic does not change** — slot search is `af-role` + `af-pool`, and occupancy is
  judged from the volume side (`freeSlots` / `occupiedInstances`). The tag is read only for billing.
- `ec2:CreateTags` / `ec2:DeleteTags` are already on `CpTaskRole`. **No IAM addition is needed.**
- ⚠️ **CUR rows are hourly**, so a handover from person to person part-way through an hour lands
  entirely on whichever had the tag. It averages out daily, but **it is documented as an error term**.
  Re-apportioning by seconds is possible, but that would overwrite actual spend with an estimate, so
  it is not done.
- **The cleanup path is fixed in both directions.** A stale tag (a box whose release finished
  half-way) is removed, and a missing tag (a box that died right after attach, or that predates this
  code) is **copied from the volume**. The former overcharges a person and the latter demotes the
  attribution to "shared" — and since cost allocation cannot be backfilled, the latter is lost
  permanently. ⚠️ The pool logic never reads this tag, so **if it breaks, nothing shows up anywhere
  except the invoice.**

### The user axis already exists. **Do not add a hash of the email**

⚠️ `af-membership` is `newID() = randHex(16)` (`store.go`) — a random value, **not derived from the
email** (the `d6e8070a…` on the real deployment merely looks like a hash). So attribution to a user
already works, and it is already non-PII. **Adding a hash of the email would be worse than the random
id** — it stays pseudonymised personal data, and an organisation's email address space is small enough
to **reverse by brute force**. `af-user` (per person, across tenants) is not added either: keeping
people opaque on the AWS side means there is no readability gain, and the CP can emit it by joining
membership → identity.

### What was genuinely missing was the tenant axis. `af-tenant` is **the slug, not the id**

No AWS resource had a tenant tag, so **the AWS side alone could not be cut by tenant**. That is the
only gain from adding a tenant axis, so an opaque id would add almost nothing beyond what
`af-membership` already derives. The slug is **an organisation name, not personal data**, and there is
no API to change it (it is fixed at `CreateTenant` and there is no `UpdateTenant`, i.e. effectively
immutable). It is carried as `Workspace.TenantSlug` — **no column is added; the tenant is LEFT JOINed
on each read** (so writing a tag never hits the store).
⚠️ **When the slug is unknown, omit the tag entirely rather than writing an empty string**: an empty
cost allocation tag value becomes a real group in the bill, "tenant = (blank)", which cannot be read as
"there is no tag".

### ⚠️ A tag key AWS has never seen cannot be activated in advance (measured)

`ce update-cost-allocation-tags-status` returns
`ValidationException: Tag keys not found: af-tenant`. The order is fixed as **"tag the resources → AWS
discovers them (~24h) → activate"**, and **with no backfill the clock starts when you activate, not
when you tagged**. In other words, **however many days it takes to get the tagging code onto the real
deployment is exactly how many days are permanently unrecoverable** — which is why it was implemented
before the design was finished.

## Decision 4 — **do not distribute** shared cost. Show it as "shared" with a breakdown, **visible to super_admin only**

Measured (sandbox, 2026-08-01 to 16, $9.0370): **the ceiling on what can be attributed to people is
22.3%**. The remaining 77.7% is NAT ($2.82) / Route53 ($1.51) / tax ($0.82) / EFS ($0.57) / the CP's
Fargate ($0.43) / ALB / RDS / PublicIPv4, and **the moment you divide it per head, actual spend becomes
an estimate.**

- Shared cost is not mixed into the per-member totals; it is **a separate card with a per-service
  breakdown**.
- **Idle pool slot hours are shared too** (`af-role=slot` with no `af-membership`). For the first time,
  the actual cost of "boxes running that nobody is using" appears as a number.
- **The shared card is super_admin only.** Showing a tenant_admin the deployment-wide ALB / RDS bill
  hands over information from outside their tenant (the line from 0043 decisions 24/25).
- ⚠️ **The wording for members is not "your cost".** It is fixed as
  "**cost tied directly to your workspace (shared cost not included)**". Calling roughly 20% of the
  real figure "your cost" is exactly the "saying something different from what is true" that this repo
  keeps falling into.

## Decision 5 — separate the names first. Do not use "usage" in a third sense

The Console already has **"使用量" / "Usage" in three places** (personal = tokens; admin and tenant
settings = running hours). Before adding money as a fourth:

| Concept | Label | Unit |
|---|---|---|
| An agent's tokens | **使用量** / Usage (unchanged) | tokens |
| A workspace running | **稼働時間** / Running time (renamed) | hours |
| The AWS bill | **クラウド費用** / Cloud cost (new) | USD |

The rename touches display strings only (`admin.usage_title` and friends). **Section keys, tab keys and
API paths are untouched** — deep links and the saved last section would break.

## Decision 6 — the currency is whatever AWS returned (USD). `UnblendedCost` is what is displayed

- **No conversion to yen.**
- The display is **`UnblendedCost` (i.e. the billed amount)**. `AmortizedCost` is fetched in the same
  request and stored, with a note shown only when they diverge (an RI or Savings Plan was bought).
  Adding metrics does not increase the per-request price.
- The latest day **moves**, with `Estimated: true`. "About 24 hours behind; today's figure is not
  final" goes next to the number (a footnote is not read).

## Decision 7 — the Console does not hit CE. The CP fetches periodically and serves its own database

CE is **$0.01 per request**. Wiring it straight to screen polling makes it pressable without limit.

- A background fetch of the same shape as `usageSampler`. **Every 6 hours it overwrites the last 7
  days** (one request with `GroupBy = [TAG af-membership, DIMENSION SERVICE]` — CE's two-axis limit).
  **$0.04/day ≈ $1.2/month.**
- Stored as `cloud_cost_daily(day, membership_id, tenant_id, service, unblended, amortized,
  estimated)`. **A row with an empty `membership_id` is shared.** Amounts are held as integers (in
  micro-units).
- The API reads the database only. The Console's "refresh" is **a re-read, not a re-fetch**. A manual
  re-fetch is super_admin only and rate-limited.

## Decision 8 — **add** `ce:GetCostAndUsage` to `CpTaskRole` (overturning 0045 decision 21 this time)

Why the judgement differs from the precedent (which rejected adding IAM for a vCPU display and used an
env declaration instead):

- There, **the same number could be produced correctly by other means.** Actual spend has no
  substitute (the substitute is a different number, i.e. an estimate).
- What is added is **one read action**. CE has no resource scoping so it is `Resource: "*"`, but what
  comes back is aggregate amounts only, touching neither resources nor secrets.
- ⚠️ **In a deployment that shares the account with something other than agent-fleet, that gets mixed
  into shared cost too.** The screen states "this aggregates the whole of this account's bill".
- ⚠️ **Prerequisite**: for an IAM role to use the Billing API, the account setting "IAM user/role
  access to billing information" must be enabled. Documented in `deploy/`.

## Decision 11 — the CP does the activation too (**overturning** decision 8's proviso)

- **Addendum (2026-08-25, [docs/67](../log/67-member-cloud-cost.md) §67.17)**: **on an organisation
  member (linked) account this decision can only be half executed.** Both `ListCostAllocationTags` and
  `UpdateCostAllocationTagsStatus` are payer-only, and the CP cannot even read "the payer has activated
  them" (measured). So **when it cannot read, judge by whether attribution is actually working** — the
  poller already groups by `af-membership`, so if rows come back with values, the axis is in effect.
  No extra Cost Explorer call, and the querying stops once the evidence is in.
  ⚠️ Only the one axis being grouped by is asserted; no claim is made about the other keys.

Decision 8 deliberately left out `ce:UpdateCostAllocationTagsStatus`, on the basis that "operating the
billing console belongs to a person". The reason for overturning it is **the asymmetry of the damage**:

- If it is forgotten → **a permanent gap in the data.** The one precondition in this system that
  cannot be fixed afterwards.
- If it is automated → five more columns in the billing data.

And the shape "it cannot be done at deployment time (AWS has not discovered them), then the next day, by
a person, if they remember" is exactly [0044](0044-workspace-sizing.md) decision 3's "a procedure nobody
performs is a procedure that does not exist".

⚠️ **This is the only place the CP writes to the account's billing settings.** It is bound **in code,
not policy**, in two ways (`cost_tags.go`):

1. **A fixed allowlist of five keys.** ⚠️ **`af-workspace` is never activated** (decision 1 — its value
   derives from the email). Bound by a test.
2. ⚠️ **Do not touch a key a person set to Inactive.** AWS stamps `LastUpdatedDate` on a state change,
   so "Inactive with a stamp" is a person's decision. Reverting it would mean overriding the operator in
   their own billing console.

⚠️ **Retry rather than one shot** (an undiscovered key cannot be activated). It rides the poller's
6-hour tick and stops calling once things settle.
⚠️ **A partial failure is in the response's `Errors`, not in Go's error.** Looking only at `err` records
a rejected key as "activated".
⚠️ **Removing the permission does not break the screen.** The CP reports "cannot activate automatically"
and the operator does it with the CLI. Meanwhile the screen keeps warning that data "is being lost right
now" (it is not a loading state, so it is not dismissed).
- ⚠️ **Verify by assuming `CpTaskRole`.** E2E runs with deployer credentials
  (`AdministratorAccess`) and would always pass — the same hole by which nobody caught
  `ec2:CreateSnapshot` missing from `CpTaskRole` (docs/64 §64.22).

## Decision 9 — **the runtime declares** whether a cost surface exists. A deployment without one gets no tab

`CostProfile()` becomes an optional capability of RuntimeFactory in the same shape as `SizingProfile()`
/ `hasPool`, answered by `GET /api/cost/profile`. `docker` and `native` have **no AWS bill**, so **the
tab is not shown at all** ("putting an item on screen that does not work is close to lying" — the line
from 0045 decision 21).

⚠️ **On Fargate (`ecs`) no task is currently tagged at all** (`CreateService` has no `Tags`, no
`EnableECSManagedTags` and no `PropagateTags`). `Tags` + `EnableECSManagedTags: true` +
`PropagateTags: SERVICE` are added (`ecs:TagResource` is already there). It does not apply retroactively
to existing services, so the launch path calls `TagResource` idempotently. **The sandbox is `ecs-ec2`,
so this cannot be confirmed on real hardware — it ships explicitly labelled "unverified on real
hardware".**

## Decision 10 — who sees what

| Viewer | What they see |
|---|---|
| The member | **Only** cost tied directly to them, and its daily trend |
| tenant_admin | Per member within their tenant, plus the tenant total (**shared cost is not visible**) |
| super_admin | All tenants, all members, the shared-cost breakdown, and the deployment total |

RBAC follows the existing `tenantScope` / `tenantAdminFor`. The personal view uses the non-admin path of
`withIdentity` (the same shape as `workspace-sizing`).

## Decision 12 — "how much is this person" goes on the member detail. One dedicated endpoint is added, but **no new DTO**

Having built three surfaces (admin / tenant settings / personal settings), **the only place to go and
see "how much is this user spending" was still the list**. The member detail, where the force-stop and
disk-cap buttons sit, had everything except cost.

- **The granularity is the total, a daily trend, a per-service breakdown and a period input.** The total
  alone would just repeat the list. The daily view (still running at the weekend = holding a slot) and
  the breakdown (EBS-heavy = a big home; EC2 Compute-heavy = slot hours) are **readings that map
  directly onto the two controls already on that screen**. There are only three `attributable`
  categories, so the breakdown is about three rows.
- ⚠️ **Do not add it as a fourth resource tile.** Those are "now", polled every four seconds; cost is a
  "period", about 24 hours behind. Not putting hours and $ on the same card is decision 2. An independent
  panel goes immediately after the resources.
- **The API is a new `GET /api/admin/tenants/{slug}/members/{key}/cost`.** ⚠️ Before widening anything we
  checked whether it could be derived: `members[]` in the existing `/api/admin/cloud-cost` **has only the
  total**, so neither the daily figures nor the breakdown can be derived. Riding on `/stats` does not fit
  its shape either, with four-second polling and an early return (`{"running":false}` when there is no
  workspace) — cost exists while stopped and after disposal. **The response is identical in shape to
  `/api/cost/me`**, and the CP's aggregation was factored into one function. **Zero new DTOs**, and no
  store addition (`ListCloudCost` can already filter by membership).
- ⚠️ **Query by membership alone (do not filter by tenant).** `tenantByMembership` resolves from "the
  current workspace row", so **the rows of a member who disposed of their workspace are rewritten with an
  empty `tenant_id`**. Filtering by tenant would show **a confident $0.00** on the detail. Membership is
  already proven by `resolveMember`.
- **The rendering is extracted from `MyCloudCostView` and shared** (`useCostOne` / `CostRangeBar` /
  `CostOneBody`). The only difference between the personal view and the detail is **one key, the total's
  label**. `CloudCostAdminView` (the list plus the shared card) has a different shape and is not reused.
- ⚠️ **The label is fixed as "cost tied directly to this member (shared cost not included)".** Writing
  "this member's cost" would point at a fifth of the real figure (the same discipline as decision 4).
  Bound by a DOM test. **Shared cost cannot get in structurally** — shared rows have an empty
  `membership_id`, so filtering by a real membership never returns them (rather than an implementation
  that "is careful not to return them").
- **The component checks the capability itself.** Passing it as a prop means that the moment someone
  forgets, a deployment with no bill (docker / native) gets a surface showing $0 (the inverse of decision
  9).

## Decision 13 — fold reserved memberships' cost into SHARED. **Keep tagging**; the folding happens on ingest

The automatic re-bake of the golden snapshot (docs/64 §64.28) uses `af-golden-seed` / `af-golden-probe`,
and it creates the workspace **through the product's normal Start path** (otherwise the baked golden
would not be a copy of "the home the product actually creates"). So it gets an `af-membership` and **it
was appearing in the per-member list as a human member**. That is not anyone's work — it is the
deployment warming its own snapshot — so shared infrastructure (decision 4's SHARED) is correct.

- ⚠️ **Not tagging is not an option.** `af-membership` is a cost allocation key and at the same time **a
  matching key** — the runtime finds the EFS access point and the home volume with
  `tagValue(ap.Tags,"af-membership") == membershipID`. Emptying the value either breaks the match or
  collides with the next untagged resource that appears. **Write the tag exactly as the product writes
  it, and fold on the ingest side.**
- ⚠️ **Sum in Go when folding.** `PutCloudCost` **replaces** `(day, membership_id, service)`
  (`ON CONFLICT ... DO UPDATE SET unblended=excluded.unblended`). CE routinely returns the seed rows and
  the untagged shared rows as separate groups for the same (day, service), so passing two rows without
  adding them means **the later row erases the earlier row's amount**. Decision 7's "it replaces, it does
  not add" becomes a trap in the opposite direction here.
- The ingest window is 7 days by default, so existing rows older than that are not folded. The read side
  folds the same way, and **no migration that rewrites financial data is written**.
- The golden snapshot itself never had an `af-membership` and is therefore already shared, so this
  completes the set of three: the seed, the probe and the snapshot.

## Decision 14 — deleting a member's row does not delete their cost rows

The line drawn when introducing the physical deletion of removed members (ADR 0043 decision 46,
docs/61 §61.18). `cloud_cost_daily` is **not** included in the cascade. `memberCloudCost` querying by
membership alone is exactly decision 12's ⚠️, and it exists so that **a disposed member's spend does not
disappear**. Deleting the rows would break that premise and **change a past month's total after the
fact**. `CloudCostTotal` was designed from the start to "emit an empty UserKey/Email if the membership is
gone", so the display does not break either.

## Options discarded

- **Show apportionment (running seconds × unit price) alongside actual spend** — decision 2. Two numbers
  always disagree.
- **Distribute shared cost per head or by running time** — decision 4. The moment it is distributed it
  stops being actual spend.
- **CUR → S3 → Athena** — cheaper and more flexible than CE, but it adds Glue / Athena / S3 lifecycle
  and permissions. Not a setup to bring in at a scale where $1.2/month of CE suffices.
- **Hit CE directly from the Console** — decision 7. It wires $0.01 straight to a user action.
- **Activate `af-workspace` too** — decision 1. No personal data in the billing data.
- **Add a hash of the email as a new tag** — decision 3. Weaker than the random id that already exists
  (it stays pseudonymised personal data and is reversible by brute force).
- **Put the tenant id in `af-tenant`** — decision 3. An opaque id cannot be read on the AWS side alone,
  which discards the only gain from adding a tenant axis.
- **Leave activation as the operator's manual step** — overturned in decision 11. The asymmetry —
  permanent damage from forgetting versus "more columns" from automating — is not bearable.
- **Activate `af-workspace` automatically too** — excluded from decision 11's allowlist.
- **Derive the member detail's cost by filtering the list's response** — decision 12. The total can be
  derived, but neither the daily figures nor the breakdown are in it — and those two are the reason for
  putting it on the detail.
- **Ride the member detail's cost on `/stats`** — decision 12. It would put four-second polling and a
  six-hourly database read in the same response.
- **Have the CP revert a tag a person turned off** — decision 11. Do not override the operator in their
  own billing console.
- **Split NAT by source IP (VPC flow logs)** — the only way to split the largest shared cost (31%), but
  the log storage cost and the implementation exceed the thing itself. It stays shared.
- **Re-apportion a sub-hour slot handover by seconds** — decision 3. That would overwrite actual spend
  with an estimate.
- **Convert to yen** — decision 6.
