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

## Review (2026-09-22, before P0)

The proposal was checked against `07cad1f29`, without touching AWS or measuring a deployment.
Verdict: **do not start P0 yet.** Reusing the ECS engine services from a docker Control Plane is
feasible and does not require a new engine executor, but the proposed ownership boundary is not
the one the code has. The model catalogue lives in the Control Plane database, docker suppresses
the AWS cost subsystem, the NAT-instance path is missing both an ingress rule and several required
egress destinations, and the VM role would expose a purchasing credential to containers unless
IMDS is isolated explicitly. These are design inputs, not implementation details.

### Critical

- **R1. The engine table and models bucket do not constitute a runnable engine; the model
  catalogue belongs to the Control Plane database.** `engine_models` holds the enabled bit,
  selected/default model, S3 keys, arguments, licence acceptance and sizing metadata
  (`control-plane/internal/store/migrations/0057_engine_models.sql:14-26,38-62`,
  `control-plane/internal/store/migrations/0058_engine_ingest.sql:40-53`). At boot a managed
  row reads those rows from `mgr.store` (`control-plane/engines.go:800-807,945-957`), builds the
  active set from the local catalogue (`control-plane/engine_catalog.go:82-104,425-467`), and
  overwrites SSM unconditionally (`control-plane/engine_catalog.go:615-639`, called at
  `control-plane/engines.go:964-968`). An ec2-single CP with a fresh SQLite database therefore
  publishes an **empty active set over the surviving one**. Conversely, deleting `10-data` deletes
  RDS in the default `Persistence=delete` shape, while `retain` leaves only a final snapshot that
  this template has no restore parameter for (`deploy/aws/ecs/cfn/10-data.yaml:64-66,157-174`).
  Decision 3's claim that the bucket and engine table survive the release cycle is not enough.
  Before P0 the ADR must choose one catalogue authority and a bidirectional hand-off rule. The
  smallest safe shape is for the VM's SQLite catalogue to remain authoritative and for a release
  deployment to borrow the roles through ADR 0079; making the release CP own them requires an
  explicit catalogue migration and conflict rule.

- **R2. Decision 4's NAT instance does not pass the first packet as written.** The VM is assigned
  only its public security group and `CpSg`, but `CpSg` admits only the ALB on the CP port
  (`deploy/aws/ecs/cfn/00-network.yaml:186-197`) and the public group admits only 22/80/443
  (`deploy/aws/ec2-single/cfn.yaml:50-57`). Forwarded packets retain a private-subnet source, so
  neither group admits them. The design needs a separate NAT ingress rule scoped to the two
  private CIDRs (`deploy/aws/ecs/cfn/00-network.yaml:39-44`) and must pin the VM to a public subnet
  whose table has the IGW route (`deploy/aws/ecs/cfn/00-network.yaml:69-84,100-117`); an arbitrary
  `SubnetId` is not sufficient. P1's acceptance test must first prove routing, SG and DNS from
  both private AZs, before testing ingest.

- **R3. The egress inventory in decision 4 is materially incomplete.** This repository itself
  says that the missing interface endpoints are `ecr.api`, `ecr.dkr`, `logs` and `ssm`
  (`deploy/aws/ecs/cfn/00-network.yaml:160-166`). GPU hosts must start the ECS agent
  (`deploy/aws/ecs/cfn/60-engines.yaml:501-510`), task images come from ECR and every container
  uses `awslogs` (`deploy/aws/ecs/cfn/60-engines.yaml:681-710,697-702,734-739,804-834,821-826,
  847-852`), and ingest resolves Secrets Manager secrets before the containers start
  (`deploy/aws/ecs/cfn/60-engines.yaml:592-655`). The optional llm key is also an SSM task secret
  (`deploy/aws/ecs/cfn/60-engines.yaml:727-739`). S3 removes layer/model *bytes* from the NAT, not
  ECR authentication/manifests, ECS agent traffic, log delivery, SSM or Secrets Manager. Cloud Map
  registration is ECS-service wiring (`deploy/aws/ecs/cfn/60-engines.yaml:741-750,752-775,
  854-884`) rather than a task-side API call; custom health is likewise declared there. Repository
  evidence for the host's time-sync path was **not found and remains unverified**. The P1 test must
  include a cold GPU host, cold ECR pulls, log arrival, an SSM-secret llm key, ingest secrets and
  service discovery—not only a model download.

- **R4. Decision 5 puts a high-value credential on a multi-tenant container host without a
  credential boundary.** Workspaces deliberately have outbound NAT
  (`control-plane/internal/runtime/runtime_docker.go:280-303`) and the CP itself is a host-network
  container (`deploy/compose/docker-compose.yml:17-44`). The current VM declares no IMDSv2 or hop
  limit at all (`deploy/aws/ec2-single/cfn.yaml:59-73`). Giving the CP container instance-profile
  credentials normally requires making metadata reachable from a container; unless workspace
  networks are explicitly denied `169.254.169.254`, a member can read the same credentials. The
  proposed role can buy fleets and pass the engine instance role
  (`deploy/aws/ecs/cfn/60-engines.yaml:318-341`) and can run ingest with a passable task role
  (`deploy/aws/ecs/cfn/60-engines.yaml:288-302`), so “smaller than CpTaskRole” is not an adequate
  boundary. P0 needs a concrete credential-delivery design: IMDSv2 plus a tested hop/firewall
  boundary, or a credential proxy available only to the CP process. Also do not copy `CpTaskRole`'s
  broad `EcsDrive` statement, which includes service deletion and task-definition registration on
  `*` (`deploy/aws/ecs/cfn/20-platform.yaml:197-227`); enumerate the engine operations and add a
  negative test from a workspace container.

- **R5. Decisions 6 and 7 leave bill-producing invariants to prose.** Each CP starts its own
  controller goroutine (`control-plane/engines.go:1001-1009`) and route registration constructs a
  registry per process (`control-plane/engine_gateway.go:191-205`); there is no cross-deployment
  lease. The two CPs also have different stores, so their mode and demand rows cannot coordinate.
  Make ownership fail closed before P0: an explicit controller identity in the engine table plus a
  required matching env value is a cheap configuration guard, while a conditional lease is needed
  if protection against duplicated configuration is required. In addition, attach the purchasing
  policy to only the selected owner and make `standup.sh` refuse a release CP that declares the
  same managed rows. For stopping, the controllers run on `context.Background()`
  (`control-plane/engines.go:1007-1008`), so VM shutdown has no quiesce hook. Ship one stop command
  or systemd shutdown unit that sets every managed role off, polls ECS/fleets to zero, and only then
  calls `StopInstances`; direct stopping must fail or print an unmistakable warning.

### Medium

- **R6. The docker runtime disables the very cost verification and tag activation this ADR relies
  on.** `dockerFactory.CostProfile` returns `Available:false`
  (`control-plane/internal/runtime/profiles.go:361-365`), and `startCloudCostPoller` returns before
  constructing Cost Explorer whenever that flag is false (`control-plane/cloudcost.go:99-128`).
  Thus Cost Explorer read/update permission on the VM role is dead code: the cost tab is hidden,
  engine `af-role` rows are not polled, and cost-allocation tags are not activated by this CP. This
  is a real code change, not an open measurement question. Cost capability must be separated from
  workspace runtime (for example an explicit AWS-billing profile), with the docker-on-EC2 case
  tested.

- **R7. The cost table is neither internally arithmetically consistent nor like-for-like.** The
  draft states a Tokyo NAT rate of `$0.062/h`, which is `$45.26` for its own 730-hour month, not
  `~$36` (`docs/decisions/0099-engines-from-a-single-vm.md:24-29,243`). The `~$14` EBS + public-IP
  row gives neither rates nor a volume size even though the current template defaults to 30 GiB
  (`deploy/aws/ec2-single/cfn.yaml:27-29,66-68`), and the stopped-floor sentence omits the retained
  EIP, Route53, Cloud Map, Secrets, ECR and S3. The measured bill already contains hosted-zone,
  regional-transfer, public-IPv4 and a residual of RDS storage/DNS/Secrets/snapshots
  (`docs/log/67-member-cloud-cost.md:57-75`). `20-platform` creates a staging bucket and six ECR
  repositories (`deploy/aws/ecs/cfn/20-platform.yaml:34-56,58-131`), while `60-engines` retains the
  model bucket and log group (`deploy/aws/ecs/cfn/60-engines.yaml:386-419`). Finally, an always-on
  ECS lender needs the CP Fargate task, but the comparison leaves it as “usage”; the VM column
  includes the host that supplies CP **and** workspaces. Rebuild the table for the same workload,
  list hourly/unit rates with dated URLs or a captured price sheet, state data volumes and request
  counts, and show both running and stopped cases. AWS list prices themselves were **not
  verifiable from this repository**.

- **R8. Durability has been priced as zero rather than decided.** The VM volume is
  `DeleteOnTermination:true` (`deploy/aws/ec2-single/cfn.yaml:66-68`). The supplied backup is a
  tarball in a caller-selected local directory and includes the SQLite catalogue
  (`deploy/compose/backup.sh:4-18,33-58`); no off-host target or EBS snapshot schedule is present.
  A usable floor must either include S3/EBS-snapshot storage, requests and transfer, or explicitly
  accept that one instance/volume loss removes the catalogue and every workspace. This also makes
  decision 3's “survives the cycle” claim operationally incomplete.

- **R9. Graviton is a capability, not a current artefact guarantee.** The cited release lines say
  that multi-arch publication occurs only when `WS_PLATFORMS` / `CP_PLATFORMS` are set; both default
  empty (`deploy/compose/release.sh:65-75,147-187`). The current ec2-single template accepts only
  t3 types and an amd64 AMI (`deploy/aws/ec2-single/cfn.yaml:22-33`). `workspace/Dockerfile` does
  cross-compile the Agent (`workspace/Dockerfile:15-29`), but the installed CLI binaries are the
  separate unverified question the ADR already records. P2 savings must be conditional on inspecting
  the published manifests and running the full workspace image on arm64; the source-table claim
  “both images build for arm64” is too strong for the cited lines.

- **R10. Two cheaper/simpler alternatives deserve an explicit comparison.** First, keep the
  managed NAT gateway but create it only for an engine/ingest activity window, retaining its EIP;
  this trades minutes of cold-start orchestration for hourly billing only during use and avoids
  host routing/patching. Second, keep the VM as the sole engine owner during release verification
  and make the ECS CP use ADR 0079's existing `remote` lifecycle, which also avoids catalogue
  transfer and the dual-controller hazard. The present rejected list mentions only “pause it
  harder” and a permanent SSM endpoint (`docs/decisions/0099-engines-from-a-single-vm.md:260-271`),
  not either shape. A separate tiny NAT instance is also worth recording as the reliability/
  isolation fallback if coupling egress to the application VM proves unacceptable.

### Light

- **R11. The `Sources checked` table is not yet an auditable basis for the decisions.** Each row
  was opened at the stated location. The result is below; “partial” means the cited text exists but
  does not support the whole claim.

| Row | Verdict | What the cited location actually establishes / corrected location |
|---|---|---|
| registry needs only table + AWS | **wrong** | `control-plane/engines.go:721-775` covers the boot gate and clients only; store, cluster, ingest, catalogue publication and subnets are at `:800-869,812,945-987` |
| separate engine cluster variable | correct | `control-plane/engines.go:812` is exactly `firstEnv("AF_ENGINE_ECS_CLUSTER", "AF_ECS_CLUSTER")` |
| bought-box subnets | correct, narrow line | the read is `control-plane/engines.go:609-616` (especially `:611`), not the function header alone |
| external/remote makes AWS optional | correct | `control-plane/engines.go:705-715`; the load gate is `:752-775` |
| EngineSg admits CpSg | correct | `deploy/aws/ecs/cfn/60-engines.yaml:422-433` |
| services are awsvpc/private/no public IP | **partial** | cited `:768-776` is llm only; image is `:878-885`, ingest is `control-plane/engine_ingest.go:1060-1069,1652-1661` |
| CP engine IAM attaches to imported role | **partial/wrong range** | CP policy attachment is `60-engines.yaml:278-341`; `:350-384` attaches secret reads to the imported **execution** role; base ECS control is `20-platform.yaml:197-227` |
| `ec2:CreateFleet` is CP permission | correct | `deploy/aws/ecs/cfn/60-engines.yaml:318-322` |
| `20-platform` imports only VPC id | correct | the one `Fn::ImportValue` is `deploy/aws/ecs/cfn/20-platform.yaml:139` |
| `20-platform` has no hourly bill | **partial** | `:34-149` shows S3, ECR, Cloud Map and ECS; no ECS-cluster hourly resource, but storage/namespace/request charges are not disproved |
| NAT + S3 gateway endpoint | correct | `deploy/aws/ecs/cfn/00-network.yaml:119-173`; `:165-166` also names traffic still using NAT |
| public subnets map public IP | correct | `deploy/aws/ecs/cfn/00-network.yaml:69-84` |
| fetch sidecar S3 + SSM calls | **claim misstated** | Put is `fetch-models.sh:74-80`, S3 is `:92-103`, Get is `:105` and every watch iteration at `:141-149`; it is not “two calls per loop” |
| ingest public IP constant | correct | `control-plane/engine_ingest.go:1060-1069,1652-1661` |
| standup required stacks | correct | `deploy/aws/ecs/standup.sh:126-135` |
| pause residue and order | correct | `deploy/aws/ecs/pause.sh:8-26` |
| measured invoice | correct | `docs/log/67-member-cloud-cost.md:54-80` |
| ec2-single shape | correct | default-VPC/no-profile facts are visible at `deploy/aws/ec2-single/cfn.yaml:50-73`; the template does not declare `VpcId`, `SubnetId` or `IamInstanceProfile` |
| SQLite | correct | `deploy/compose/.env.example:373-374` |
| Caddy ACME | **partial** | `deploy/compose/Caddyfile:14-16` shows a public host and reverse proxy; the repository's explicit Let's Encrypt statement is `deploy/aws/ec2-single/README.md:77-79` |
| both images build for arm64 | **wrong as stated** | `deploy/compose/release.sh:65-75` documents opt-in variables whose defaults are empty; build branches are `:147-187` |
| capability table | correct but unscoped | exact current row and footnotes are `guide/ref/deploy-targets.md:28-57` |
| ADR 0079 reordered alternative | correct | `docs/decisions/0079-remote-engine-from-another-deployment.md:556-564` |

### Gate before P0

P0 may start only after the ADR chooses the catalogue authority, specifies isolated credential
delivery, makes one controller fail closed, and corrects the P0 completion test to include the
docker cost profile and workspace-to-CP image path. P1 additionally needs the NAT SG/public-subnet
design and the full cold-start egress matrix above. No AWS price, time-sync behaviour or live route
was verified in this review.
