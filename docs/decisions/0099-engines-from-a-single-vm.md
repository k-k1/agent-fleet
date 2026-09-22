# 0099. The fleet's own GPU from a single VM — ec2-single drives the ECS engine roles, and what it replaces is stood up only to verify a release

English | [日本語](0099-engines-from-a-single-vm.ja.md)

- Status: **proposed** (2026-09-22). Nothing is implemented. No stack has been stood up in
  this shape and no bill has been read against it.
- **Nothing was measured for this document.** Every figure says where it comes from —
  (a) measurements already in this repository (`docs/log/67` §67.3, `deploy/aws/ecs/pause.sh`,
  ADR 0074/0075/0077), (b) templates and code read on 2026-09-22 (file:line under "Sources
  checked"), (c) **AWS list prices for ap-northeast-1, as published** — arithmetic, not an
  invoice. Every decision that rests on (c) is named under "Open questions".
- The request in one sentence: **the deployment that is up every day should keep its own image
  and llm GPU engines while costing what one VM costs, and the ECS build should become
  something stood up to verify a release and then taken away.**

## Background

### The ECS build's floor is the part that cannot be stopped

`pause.sh` states the shape of the bill: roughly **$5.5 a day running, $2.6 a day while
paused — half of it fixed**, and names what stays behind: NAT, ALB, RDS, EFS. Scaling a
deployment down stops the slots' compute and the Control Plane's Fargate; it cannot stop the
four things that make up the floor.

The one real invoice this repository has read (`docs/log/67` §67.3, `af-sandbox`,
2026-08-01〜16, **$9.0370 total**) is a bill for a deployment that was mostly *not there*:
`APN1-NatGateway-Hours` $1.24 at a list price of $0.062/h is about **20 hours of NAT in 16
days**, and the ALB and RDS lines are of the same size. That is the usage pattern the ECS
build is good at — stand it up, prove something, tear it down.

It is not the usage pattern of a deployment that is up every day so that another fleet can
borrow its GPU. For that, the number that decides everything is the **floor**, and the ECS
build's floor is about $78 a month that nothing but `teardown.sh` removes.

### The engine half does not know what a runtime is

Read on 2026-09-22, and this is what makes the whole ADR small:

- `newEngineRegistry` asks for two things — an engine table (`AF_ENGINES_SSM_PARAM` or an
  inline `AF_ENGINES_JSON`) and, if any row is managed here, an AWS config
  (`engines.go:721-775`). It never asks what the workspaces run on.
- **No file under `control-plane/engine_*.go` branches on `AF_RUNTIME`.** A deployment has
  engines because it declares them, not because it is ECS.
- The cluster name is already read as `firstEnv("AF_ENGINE_ECS_CLUSTER", "AF_ECS_CLUSTER")`
  (`engines.go:812`) — the split between "the workspaces' cluster" and "the engines' cluster"
  was anticipated and is the seam this ADR uses.
- The security group the engine admits is the Control Plane's, by import, not by task identity
  (`60-engines.yaml:431`). Anything wearing `CpSg` reaches the engine.

### Three stacks that are already self-contained, and they bill nothing by the hour

`20-platform` has exactly one cross-stack import: the VPC id (`20-platform.yaml:139`).
`60-engines` imports the network (VPC, private subnets, CP SG) and the platform (cluster, exec
role, engine ECR repositories, Cloud Map namespace) and **nothing else** — not `10-data`, not
`30-ingress`, not `40-ec2-pool`.

What those three keep alive costs, per month at list price: an ECS cluster $0, IAM $0, launch
templates $0, ECS services at desired count 0 $0, a Cloud Map private DNS namespace ~$0.50,
two Secrets Manager secrets ~$0.80, ECR storage and the models bucket by the gigabyte. **The
expensive half of the ECS build is `10-data` (RDS, EFS), `30-ingress` (ALB, the CP's Fargate
service) and `40-ec2-pool` (slot instances and their volumes) — none of which an engine
needs.**

### What ec2-single is today

A single Ubuntu VM with Docker, an Elastic IP and a Route53 A record, running the compose
stack (`deploy/aws/ec2-single/cfn.yaml`, `deploy/compose/`). SQLite rather than RDS
(`.env.example:374`), Caddy rather than an ALB, workspaces as containers on the same host.
It can *point at* an engine — `AF_COMFY_URL`, `AF_LLM_URL`, `AF_REMOTE_ENGINE_URL` — and
`guide/ref/deploy-targets.md` records the consequence in its capability table: "an image
engine the deployment provides" is a ✓ for docker only in the sense of somebody else's
always-on ComfyUI, and the fleet's own bought-on-demand GPU is an `ecs-ec2` row alone.

The template also puts the VM in the account's default VPC and gives it no instance profile,
so today it has neither an address inside the engine VPC nor an identity that may buy a box.

### The one thing genuinely in the way: an engine task cannot have a public IP

The engine services are `awsvpc` on the EC2 launch type, with `AssignPublicIp: DISABLED`
(`60-engines.yaml:770,880`) — which is not a choice but the only value that launch type
accepts. So the task's own ENI reaches the internet only through something the VPC provides.
What it actually needs:

| What the task does | How it gets out |
|---|---|
| pulls model objects, tens of GB (`fetch-models.sh:99`) | **S3 gateway endpoint — free** (`00-network.yaml:167`), and ECR layer blobs are S3 too |
| reads the active set and writes the pending set, two small SSM calls per loop (`fetch-models.sh:79,105,142`) | today: the NAT gateway |
| the ingest task's downloads from Hugging Face / Civitai | today: the NAT gateway (`engine_ingest.go:1068,1660` hard-code `AssignPublicIp: DISABLED`) |

ADR 0079 predicted this exact question when it reordered this work: *"its economics turn on
whether a NAT gateway can be avoided"*. They do: the NAT gateway is about **$36 a month plus
$0.062 a gigabyte**, against a target floor of one small instance.

## Decisions

### 1. The VM joins the engine VPC and wears `CpSg` as a second group

`deploy/aws/ec2-single/cfn.yaml` gains `VpcId` / `SubnetId` parameters (empty = today's
default-VPC behaviour, so an existing stand-up is unchanged) and attaches **two** security
groups: the imported `CpSg`, which is what `EngineSg` already admits, and its own public group
for 22/80/443.

The point is what does **not** change: `60-engines.yaml` is not edited for this. The engine
opens to "the Control Plane", the Control Plane is now a VM, and the sentence stays true.

Without it, every alternative ends in editing the engine stack's security rules to name an
address — which is the thing a deployment cannot hold across a rebuild.

### 2. Owning an engine is a property of the deployment, not of the runtime

An ec2-single deployment declares engines with four values in `.env` plus an instance profile:

```
AF_ENGINES_SSM_PARAM=/af-ws/engines      # the table 60-engines wrote
AF_ENGINE_ECS_CLUSTER=af-af-ecs-platform # the engines' cluster
AF_ENGINE_SUBNETS=subnet-…,subnet-…      # where a bought box is placed (decision 8)
AWS_REGION=ap-northeast-1
```

Nothing else is added to the Control Plane for the common path. The ECS adapter, the
controller, the ladder, the offers, the active set, the pending reader, the ingester and the
uptime sampler are all built from the table exactly as they are on `ecs-ec2`.

🔴 This is a statement about **code that already behaves this way**, not a refactor. If a
future change gates an engine behind a runtime profile, this ADR is what it breaks.

### 3. What stays standing is `00-network` + `20-platform` + `60-engines`

`10-data`, `30-ingress` and `40-ec2-pool` become **release-verification scaffolding**: stood
up on top of the same three stacks when a release is being checked, and deleted afterwards.
The engines, the models bucket (ADR 0085's ledger), the ECR repositories and the engine table
survive that cycle untouched, so the verification deployment costs a stand-up and not a
re-ingest.

`standup.sh` today requires `00-network 10-data 20-platform 30-ingress`
(`standup.sh:130`); it gains a way to stand up the engine trio alone.

### 4. Private egress leaves through the VM, and the NAT gateway becomes a condition

The VM is up whenever anything can ask for an engine, it already has a public address, and it
is the only always-on box in the design. So it is the default route of the private subnets:
`SourceDestCheck: false`, `net.ipv4.ip_forward=1`, one `MASQUERADE` rule in cloud-init, and
`00-network.yaml` gains `PrivateEgress: nat-gateway | instance | none` with the NAT gateway's
resources behind a condition.

- **$0, and the bytes that matter never touch it**: model objects and ECR layers go through
  the S3 gateway endpoint (free), so what crosses the VM is two SSM calls per poll and, when
  somebody takes a model in, the ingest download.
- The failure mode is honest: if the VM is down, no engine can fetch — and if the VM is down,
  nothing is asking for an engine.
- 🔴 It is a route, so nothing reports it broken. The completion test for this decision is an
  ingest of a real model through it, not a health check.

A NAT gateway stays one parameter away for a deployment that wants the managed thing.

### 5. The Control Plane's engine IAM is written to be attachable to two roles

`60-engines` already attaches its Control Plane permissions to a role it does not own, by
pulling the name out of an imported ARN (`60-engines.yaml:284,356,378`). That mechanism stays;
what changes is that the role is a **parameter** rather than `${PlatformStackName}-CpTaskRoleArn`
alone, and `20-platform` grows an EC2-trusted role plus instance profile for the VM carrying
the same statements the CP task role has for engines (ECS drive, `ec2:CreateFleet` and
friends, SSM under `/af-ws/*`, `iam:PassRole` to the exec and ingest roles, the models bucket,
the engines log group, Cost Explorer read).

🔴 The VM's role is **not** the CP task role with a second trust policy. The task role carries
EFS, RDS-secret and slot-pool statements that an ec2-single deployment has no use for, and a
credential on a VM that anyone with shell access can read should carry the smaller set.

### 6. 🔥 One engine role has exactly one Control Plane

Two Control Planes that both read the same engine table will both move the same service's
desired count, both buy boxes on the same launch template, and both write the active set.
Nothing in the code detects it; what the operator sees is an engine that stops seconds after
it starts.

So when the ECS build is stood up for a release check against an account whose engine trio is
live, exactly one of these is true:

- the VM's Control Plane is stopped for the duration, or
- the VM's engines are set to `off` and the ECS deployment owns them, or
- the ECS deployment declares them `lifecycle: "remote"` and borrows from the VM (ADR 0079),
  which is the only variant where both deployments can serve pictures at once.

This belongs in the runbook, not in code: the cheap detection ("is another CP polling this
service") is a poll of something that is 0 most of the time, and the expensive one is a lease
in the engine table that nothing else needs.

### 7. 🔥 The VM is stopped only after its engines are

`pause.sh` already carries this lesson for slots: *stopping the CP first strands any running
slot awake — the most painful way to get this wrong, since the most expensive thing keeps
billing after you think you stopped it.* A GPU box is $1.17/hour and the only thing that
would have stopped it is the controller inside the VM.

Therefore the stop procedure is: engines to `off` (or wait for the idle stop), confirm no box
is running, then stop the VM. If night-stopping the VM is automated (decision 10's P2), the
automation performs those steps and not `stop-instances` alone.

### 8. `AF_ENGINE_SUBNETS`, because `AF_ECS_SUBNETS` means the workspace pool everywhere else

`engineSubnets()` reads `AF_ECS_SUBNETS` alone (`engines.go:609`), which on an ecs-ec2
deployment is also the subnets the slot pool and the workspace tasks use. On a docker
deployment that variable otherwise means nothing, and asking an operator to set "the ECS
subnets" on a deployment that has no ECS workspaces is how a value ends up in the wrong
deployment's `.env`.

It becomes `firstEnv("AF_ENGINE_SUBNETS", "AF_ECS_SUBNETS")` — the shape `AF_ENGINE_ECS_CLUSTER`
already has, and the old name keeps working.

### 9. TLS stays Caddy's; ACM is not free without the load balancer this removes

ACM's public certificates are free **to the AWS endpoints that terminate them** — ALB, NLB,
CloudFront, API Gateway. There is no such endpoint left here: the ALB is the thing being
deleted, and putting one back to get a free certificate costs about $18 a month to save the
$0 that Caddy already charges. An exportable public certificate is billed per certificate and
hands back the renewal and the reload that `caddy:2-alpine` does by itself, with the cert
already landing under `DATA_DIR` where `backup.sh` picks it up.

ACM becomes the right answer again the moment a load balancer exists for another reason — WAF,
several origins, or a requirement that the VM hold no public address. That is not this
deployment.

### 10. The completion test is a bill, not a screenshot

ADR 0079 wrote the test for this work when it reordered it: **"the same workspace gets the
same image, more cheaply"**. Concretely, all of:

1. a session on the ec2-single deployment generates a picture on a GPU the deployment bought
   and stopped again;
2. the home fleet borrows the same engine through the VM's gateway (ADR 0079, unchanged);
3. one model is taken in through the ingest path, through the VM's route;
4. **three days of Cost Explorer**, compared against the same three days' worth of the ECS
   floor, with the GPU hours excluded from both sides.

## What this costs, at list price (arithmetic, not an invoice)

Per month, ap-northeast-1, 730 hours. The GPU is excluded from both columns: it is bought by
the same code on both and ADR 0074's measurement ($1.1672/h for `g6.xlarge`, and with ADR 0077
there is no managed-instances fee on top of it) does not change here.

| | ECS build, up every day | this ADR |
|---|---:|---:|
| NAT gateway | ~$36 | **$0** (decision 4) |
| ALB | ~$18 | $0 |
| RDS (db.t4g.micro) | ~$18 | $0 (SQLite) |
| EFS | ~$6 | $0 |
| CP Fargate + slot instances and their volumes | usage | $0 (containers on the VM) |
| the VM | — | $63 (`t4g.large`) / **$31 (`t4g.medium`)** |
| its EBS + public IPv4 | — | ~$14 |
| `20-platform` + `60-engines` standing | same on both | ~$5–15 (ECR, models bucket, namespace, secrets) |
| **floor that cannot be stopped** | **~$78** | **~$10** (a stopped VM is its EBS) |

The last row is the decision. A `t4g.large` up 24/7 is roughly break-even against the ECS
floor — **this ADR does not pay for itself by existing**. It pays when the floor becomes
choosable: right-size the instance, run Graviton (both images are already multi-arch —
`release.sh:67`), and stop the whole deployment with one API call on the nights it is not
lending a GPU, which the ECS build cannot do at all.

## Rejected alternatives

| Alternative | Why not |
|---|---|
| **Make the VM itself a GPU instance** and run the engines as compose sidecars | `g6.xlarge` 24/7 is about $850/month. The whole point of ADR 0071's controller is that the GPU is asleep most of the day |
| **A new engine lifecycle that runs a container on a plain EC2 box, no ECS at all** | It buys nothing: an ECS cluster and its services at desired count 0 bill $0. It would re-implement the ladder, the offers, the placement, the fetch sidecar and the active set — ADR 0071/0072/0074/0075/0077 — to save a line item that is already zero |
| **A tunnel from the home fleet into the engine VPC**, no AWS-side deployment at all | ADR 0079 rejected it on its merits (the instances are short-lived, addressed through Cloud Map, and their SG admits the CP only). It also does not save anything: the tunnel needs an always-on box in the VPC, which is the VM this ADR already has — minus the deployment |
| **Keep borrowing from an ECS af-sandbox and just pause it harder** | `pause.sh`'s own header says what is left: NAT, ALB, RDS, EFS. A paused deployment also cannot lend a GPU, which is the reason it is up |
| **ACM instead of Caddy** | Decision 9 |
| **An SSM interface endpoint instead of the NAT gateway** | ~$9/month for one AZ, and it does not finish the job: the ingest task still needs the internet, so a NAT of some kind stays. It is the fallback if decision 4's route proves unreliable |
| **Tear af-sandbox down entirely between releases** | The cheapest possible answer and the one in use today — it is what makes the measured bill $9 for 16 days. It costs the daily GPU, which is the thing being paid for |
| **Buy a GPU for the host at home** | Out of this repository's scope, and honestly the cheapest answer if the daily use is heavy: ADR 0076 already supports a LAN ComfyUI, and ADR 0093's llm role would need the same treatment. Worth re-deciding before P2 spends money on instance sizing |

## Decisions this overrides, and the ones it keeps

- **ADR 0079's "Doing ec2-single's engine stack first" — this is that work.** It was reordered,
  not rejected, and its ordering argument held: 0079 shipped first, the borrowing side exists,
  and this ADR is now a cost optimisation of the lender with a completion test 0079 wrote.
- **ADR 0071 decision 4 (the gateway is the only way a workspace reaches an engine): kept.**
  No agent-side change is proposed anywhere in this document.
- **ADR 0072 / 0074 / 0075 / 0077: kept unchanged.** This ADR adds no engine behaviour; it
  changes who holds the credential and where the Control Plane runs.
- **ADR 0076's `external` and ADR 0079's `remote` rows: kept.** A deployment may still have
  both — a bought engine and a borrowed one — because that is a table, not a mode.
- `guide/ref/deploy-targets.md`'s capability table changes: "an image engine the deployment
  provides" becomes true for `docker` in the fleet's-own-GPU sense, with a footnote saying it
  requires the AWS-side engine trio and an instance profile.

## Open questions (decide after measuring)

1. **What does the deployment cost right now?** The one measured breakdown is 2026-08 and
   describes a deployment that was up 20 hours in 16 days. P0 starts by reading the last 30
   days in the Console's cost view — the table above is list-price arithmetic until then.
2. **Does an ingest of tens of gigabytes through the VM's route finish in a reasonable time?**
   A `t4g.medium`'s sustained network is a fraction of a NAT gateway's. Measure one real model.
3. **How does a VM outage look from the engine side?** The fetch sidecar retries; whether the
   panel says anything useful while the route is dead is unknown.
4. **Does every CLI in the workspace image work on arm64?** The image builds for it
   (`workspace/Dockerfile:27`); the agent CLIs it installs are a separate question, and P2's
   saving depends on the answer.
5. **How do the two builds share a name?** One FQDN moved between an EIP and an ALB, or a
   second hostname for the verification deployment with its own OAuth redirect registered.
6. **Does cost attribution survive?** `60-engines` stamps `af-role` on the engine resources, so
   engine rows should still resolve; the VM is one untagged instance carrying every workspace,
   which is exactly the "77.7% is shared" shape `docs/log/67` already warns about.
7. **Is 4 GB enough** for the CP, Caddy, one workspace container and a NAT path? The ec2-single
   README already says `t3.medium` works with `WS_MEMORY` lowered.

## Phases

- **P0 — it works, and nothing about the network changes.** `ec2-single` gains VPC placement,
  the second security group and an instance profile; `standup.sh` gains the engine-trio path;
  `AF_ENGINE_SUBNETS` lands. The NAT gateway stays exactly as it is. Done when a session on the
  VM generates a picture on a box the VM's Control Plane bought and stopped.
- **P1 — take the NAT gateway out.** `PrivateEgress` and the VM's route. Done when an ingest
  and a borrow both complete with the NAT gateway deleted, and three days of bill are in hand.
- **P2 — choose the floor.** Instance family and size, the night stop with decision 7's order,
  Spot for the image role only (ADR 0075's split: an interrupted conversation is not the same
  as an interrupted picture).
- **P3 — optional, and only if P1 says the route is a problem.** Remove SSM from the task path
  by publishing the active set to the models bucket and reading it through the free gateway
  endpoint, and let the ingest task carry a public IP (`engine_ingest.go`). It ends the private
  subnets' need for egress entirely — and it bumps `engine-tools/CONTRACT` and `TAGS.tsv`,
  which is the most breakable seam in the repository, so it is deliberately last.

## Sources checked (2026-09-22, this repository)

| Claim | Where |
|---|---|
| the registry needs a table and AWS credentials, and nothing else | `control-plane/engines.go:721-775` |
| the engines' cluster is already a separate variable | `control-plane/engines.go:812` |
| the subnets a bought box goes in come from `AF_ECS_SUBNETS` | `control-plane/engines.go:609` |
| an external/remote row is what makes AWS optional | `control-plane/engines.go:705-715` |
| the engine admits the CP's security group, by import | `deploy/aws/ecs/cfn/60-engines.yaml:422-433` |
| the engine service is `awsvpc`, private subnets, no public IP | `deploy/aws/ecs/cfn/60-engines.yaml:768-776` |
| the CP's engine IAM is attached to an imported role | `deploy/aws/ecs/cfn/60-engines.yaml:279-300,350-378` |
| `ec2:CreateFleet` is the CP's, on this stack | `deploy/aws/ecs/cfn/60-engines.yaml:321` |
| `20-platform` imports only the VPC id | `deploy/aws/ecs/cfn/20-platform.yaml:139` |
| `20-platform` bills nothing by the hour | `deploy/aws/ecs/cfn/20-platform.yaml:34-210` |
| the NAT gateway and the free S3 gateway endpoint | `deploy/aws/ecs/cfn/00-network.yaml:136-172` |
| public subnets already map a public IP on launch | `deploy/aws/ecs/cfn/00-network.yaml:75,83` |
| the fetch sidecar's S3 and SSM calls | `deploy/aws/ecs/engine-tools/fetch-models.sh:79,99,105,142` |
| the ingest task's `AssignPublicIp` is a constant | `control-plane/engine_ingest.go:1068,1660` |
| what `standup.sh` requires | `deploy/aws/ecs/standup.sh:130` |
| what pausing leaves behind, and the order that matters | `deploy/aws/ecs/pause.sh` (header) |
| the one measured invoice | `docs/log/67-member-cloud-cost.md` §67.3 |
| ec2-single is compose on a VM, default VPC, no instance profile | `deploy/aws/ec2-single/cfn.yaml` |
| SQLite rather than RDS | `deploy/compose/.env.example:374` |
| Caddy terminates TLS with ACME | `deploy/compose/Caddyfile:14-16` |
| both images build for arm64 | `deploy/compose/release.sh:67-72` |
| the capability table this changes | `guide/ref/deploy-targets.md` |
| the reordered alternative this ADR is | `docs/decisions/0079-remote-engine-from-another-deployment.md:564` |
