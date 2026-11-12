---
audience: "a deployment administrator who has the source IP address of an AWS ECS deployment allow-listed by a customer firewall or a SaaS"
source_of_truth: "`deploy/aws/ecs/cfn/00-network.yaml` (`NatEip`) and `deploy/aws/ecs/capture-env.sh`"
---

# Pin the outbound IP address

English | [日本語](egress-ip.ja.md)

Find the source IP address an ECS deployment egresses from, and keep it the same across a rebuild.
This is the address to hand to a corporate firewall or a SaaS IP allow-list.

## How it works

| What you do | What happens |
|---|---|
| Look up the NAT gateway's Elastic IP and have it allow-listed | Everything the deployment sends to the internet leaves from that address |
| Before a rebuild, check the capture has `NatEipAllocationId` | The next stand-up reuses the address; no allow-list changes |
| Never pass `NatEipAllocationId` to a running stack | The address stays retained, and the NAT gateway is not replaced |

## Look up the address

With the default `EgressRoute=nat-gateway`, a deployment egresses from **exactly one address**: the Elastic IP on `00-network`'s NAT gateway.

- Every workspace's git, model API and package traffic, and the Control Plane's own calls, go through it
- The slots and the Control Plane task sit in private subnets with no public IPv4. The slot launch template assigns none either
- The only VPC endpoint is the S3 gateway endpoint on the private route table, which reaches AWS's own S3 without an address of its own
- With `EgressRoute=gateway`, workspaces egress from the gateway's address: have both registered ([Control outbound traffic with a gateway](egress-gateway.md#register-two-addresses))

```bash
alloc=$(aws cloudformation describe-stacks --stack-name af-ecs-network \
  --query "Stacks[0].Outputs[?OutputKey=='NatEipAllocationId'].OutputValue" --output text)
aws ec2 describe-addresses --allocation-ids "$alloc" --query "Addresses[0].PublicIp" --output text
```

> [!WARNING]
> `describe-addresses` also shows the ALB's addresses (`ServiceManaged: alb`). Those are inbound, AWS-owned, and not guaranteed to stay the same: never publish them. Inbound is the FQDN's job.

## Keep the address across a rebuild

`NatEip` carries `DeletionPolicy: Retain` and `UpdateReplacePolicy: Retain`, so no stack operation, a full `teardown.sh` included, releases it. Pass its allocation id back at the next stand-up.

`capture-env.sh` carries the id.

- When the stack created the EIP itself, `NatEipAllocationId` is an empty parameter, and the real id exists only as a stack output
- `capture-env.sh` applies one rule: **an output whose key matches a parameter captured empty supplies the value**
- So the id lands in the capture like any other parameter, and the next stand-up hands it back

### Check an old capture before standing up from it

A capture taken before that rule has no `NatEipAllocationId` line, and standing up from it takes a second EIP.

- The empty parameter selects creating an EIP again, and the retained address is orphaned
- The address every customer allow-listed changes without notice
- Each orphaned EIP costs about $3.6 a month

Look at the capture before you stand up:

```bash
grep '^NatEipAllocationId=' ~/.config/agent-fleet/deploy/<p>.<r>/params/00-network
```

If the line is missing or empty, what to do depends on the deployment.

| Deployment | What to do |
|---|---|
| Still up | Capture again (`capture-env.sh --force`), or read the output and write the line by hand |
| Already torn down | The retained address is still in the account. Find it by tag instead of letting the next stand-up allocate a new one |

Reading the output and writing it by hand:

```bash
aws cloudformation describe-stacks --stack-name af-ecs-network \
  --query "Stacks[0].Outputs[?OutputKey=='NatEipAllocationId'].OutputValue" --output text
# -> NatEipAllocationId=eipalloc-... into ~/.config/agent-fleet/deploy/<p>.<r>/params/00-network
```

Finding it by tag after a teardown:

```bash
aws ec2 describe-addresses --output text --query \
  "Addresses[].[AllocationId,PublicIp,AssociationId,Tags[?Key=='Name']|[0].Value]"
# the row named af-<stack>-nat-eip with no association id is the one that was retained
```

Pass the id when you stand up fresh:

```bash
aws cloudformation deploy --stack-name af-ecs-network --template-file cfn/00-network.yaml \
  --capabilities CAPABILITY_IAM --parameter-overrides NatEipAllocationId=eipalloc-...
```

## Never pass the parameter to a running stack

> [!CAUTION]
> **Do not set `NatEipAllocationId` on a running deployment**, not even to the address the stack already created.

- CloudFormation sees the `AllocationId` property's source change from `!GetAtt NatEip.AllocationId` to a parameter, and plans to replace the **NAT gateway** (`ReplaceAndDelete`)
- The same allocation is still attached to the old one, so egress drops and the create cannot succeed. This was checked on a running deployment without executing the change set
- The parameter is for a **fresh** stand-up. On a running stack, retention alone already keeps the address

## One NAT gateway means one AZ

`00-network` puts a single NAT gateway in the first AZ, for cost.

- If that AZ is lost, **every** workspace loses egress, including those in the second AZ
- A NAT gateway per AZ fixes that, and makes the deployment egress from **two** addresses
- Decide before the allow-list is published. An unattached EIP costs about $3.6 a month, so allocating the second address up front and registering both is the cheap way to keep that option open

## Related

- [Control outbound traffic with a gateway](egress-gateway.md)
- [Stand up, pause and tear down with the scripts](lifecycle.md): `capture-env.sh` and `standup.sh`
- [Tear a deployment down](teardown.md): what survives a teardown
