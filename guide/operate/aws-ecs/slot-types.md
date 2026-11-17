---
audience: "Operators of an ecs-ec2 deployment deciding which instance types (classes) slots run on"
source_of_truth: "`Ec2SlotTypes`, `Ec2SlotAmiArm64` and `Ec2DefaultSlotClass` in `deploy/aws/ecs/cfn/30-ingress.yaml` and `deploy/aws/ecs/cfn/PARAMETERS.md`, and the Control Plane's `parseSlotClasses` (`control-plane/internal/runtime/runtime_ecs_ec2_pool.go`)"
---

# Choose the slots' machine types

English | [日本語](slot-types.ja.md)

On an `ecs-ec2` deployment, offer several kinds of instance for slots, and let tenant administrators pick one per tenant and per member.
You can add a class that trades speed for cost, or a Graviton (arm64) class.

## How it works

| What you do | What happens |
|---|---|
| Write named classes into `Ec2SlotTypes` | **Machine** appears on the tenant administrator's screens |
| Prepare the image and the AMI before adding an arm64 class | Graviton slots can launch |
| Set `Ec2DefaultSlotClass` | Members who chose nothing land on that class |
| A tenant administrator picks a machine | That member lands on an instance of that class from their next start |

Adding a class moves nobody by itself.

## Write the classes

`Ec2SlotTypes` takes two shapes. The older one keeps working.

```
instanceType:memoryMiB[:vcpu],…                one unnamed class, x86_64
id|label|arch|<ladder>[;id|label|arch|<ladder>] one class per entry
```

| Field | What it is |
|---|---|
| `id` | The class id. `Ec2DefaultSlotClass` refers to it |
| `label` | The name shown on the tenant administrator's screens |
| `arch` | `x86_64` or `arm64`. Declared, never guessed from the instance name. A class with any other value is dropped |
| `<ladder>` | `instanceType:memoryMiB[:vcpu]` rungs, ascending, separated by commas |

- A workspace lands on the smallest rung that holds its memory request and gets the whole instance. The class picks the ladder and memory picks the rung within it, so "8 GB" means the same in every class
- The vCPU field is optional: the Console uses it to show which instance a memory number lands on, and the usage history uses it to express CPU as a share of the whole box
- A fourth field, `instanceType:memoryMiB:vcpu:baselinePct`, declares a burstable rung's CPU baseline as a percent of the whole box (1 to 99). It is optional, and only the usage history reads it, to count the minutes a member ran above it. Without it, or without the vCPU field, that figure is left empty. A value that does not parse costs only that figure, never the rung
- Classes are separated by `;` or a newline. A class with no usable rung is dropped

The default declares two Intel classes:

```
standard|Standard (Intel)|x86_64|m7i.large:8192:2,…;saver|Lower cost (Intel, previous gen)|x86_64|m6i.large:8192:2,…
```

## Compare the families

Measured in ap-northeast-1 against m7i: the price from the Pricing API, the speed from this repository's own build on each family.

| Family | Price per hour | Build time | **Cost per build** |
|---|---|---|---|
| m6i | −4.8% | +13% | +7.3% |
| **m8g** (Graviton4) | **−11.0%** | **−29%** | **−37.1%** |
| m7g (Graviton3) | −19.0% | −10% | −27.4% |
| m6g (Graviton2) | **−24.0%** | **+32%** | ±0 |

- **The cheapest instance per hour is not the cheapest instance.** m6g bills 24% less per hour and takes 32% longer, which nets out to no saving on work that keeps the CPU busy. m8g is both cheaper and faster than m7i, so there is no reason to pick m7g
- **Which column matters depends on the member.** A slot bills for the time it is running, and a workspace spends most of that reading, thinking or waiting on a model. A mostly-reading member really does pay 24% less on m6g; a member who builds all day pays in time instead. That is why this is a per-member setting
- **Put the trade-off in a class's `label`** ("Cheapest — older, slower"), not just the discount. "24% cheaper" on its own reads as free money

**m6i needs nothing.** It has the same architecture as m7i, so the same workspace image, the same AMI and the same golden snapshot work, and a member's home keeps working when they move between the two. Graviton is where the real saving is, and every step below is for Graviton.

## Add a Graviton (arm64) class

Before declaring an arm64 class, prepare in this order.

1. **Give the workspace image an arm64 manifest.** Without it the slot launches and the task cannot pull anything that runs on it. Check with `crane manifest <image>`: a single `manifest.v2+json` is one architecture; a manifest list (OCI index) is what you want
2. **Pass the arm64 AMI.** A launch template pins one AMI, and Graviton cannot boot the x86_64 ECS-optimized one. The Control Plane passes the arm64 AMI as a `RunInstances` ImageId override
   - Set `SlotAmiIdArm64` on `40-ec2-pool` to the output of the command below and update that stack. This parameter is also what the Control Plane role may launch

     ```bash
     aws ssm get-parameter --name /aws/service/ecs/optimized-ami/amazon-linux-2023/arm64/recommended/image_id --query Parameter.Value --output text
     ```

   - Pass the `SlotAmiIdArm64` output of `40-ec2-pool` as `Ec2SlotAmiArm64` on `30-ingress`
3. **Add the arm64 class to `Ec2SlotTypes`.** For example:

   ```
   …;arm|Faster and cheaper (Arm)|arm64|m8g.large:8192:2,m8g.xlarge:16384:4,m8g.2xlarge:32768:8
   ```

4. **Wait for a second golden bake.** A golden is a home full of binaries, so there is one per architecture. Until the arm64 one is baked, new members on an arm64 class start from an empty home (slower, not broken). The **Slots** page lists them per architecture

- With an arm64 class declared and `Ec2SlotAmiArm64` empty, the Control Plane refuses to boot, rather than show a class that cannot launch
- `Ec2SlotAmiArm64` is only the initial and fallback value. The Control Plane reads the same SSM parameter itself and launches slots with it, so patching needs no redeploy

## Add light classes (opt-in)

For members whose agents mostly read, edit and run small commands, a 4 GiB instance costs about half as much as an 8 GiB one. Declare these as **separate, opt-in classes**. The shipped `Ec2SlotTypes` default does not change, and adding a class moves nobody.

```
…;light-arm|Light (Arm)|arm64|m8g.medium:4096:1,m8g.large:8192:2
;burst-arm|Burstable (Arm)|arm64|t4g.medium:4096:2,t4g.large:8192:2
;burst-x86|Burstable (Intel)|x86_64|t3.medium:4096:2,t3.large:8192:2
```

**Append them to the value you already run (the `…`); do not replace it.** The first class is the default when `Ec2DefaultSlotClass` is empty, so a value that starts with a light class would put every member who chose nothing on it. The `arm64` ones need the arm64 steps in [Add a Graviton (arm64) class](#add-a-graviton-arm64-class). For more memory per vCPU, give `r8g.medium:8192:1` a class of its own. Do not put it on the `light-arm` ladder: it ties `m8g.large:8192` on memory, and the order of tied rungs is not defined

- **A light rung is always its own class. Never add it under an existing class.** A member whose size is 0 (no memory request) lands on the smallest rung of their class. Put `m8g.medium` under `arm` and every member of that class who never chose a size silently drops to 4 GiB. A tenant default of `arm` with no choice of its own keeps starting on `m8g.large`, the smallest rung of `arm`, not on a light class
- **A 4 GiB rung gives the workspace 2048 MiB.** The reserve is a flat 2 GiB off every rung at the default `Ec2HostReserveMb` of `auto` ([What a workspace actually gets](slot-pool.md#what-a-workspace-actually-gets)), so a 4 GiB rung leaves half
  - That is enough for light agent work. Measured on `m8g.medium` and `t4g.medium`: clone and `go vet` finished on both, `npm ci` and `tsc --noEmit` finished on `t4g.medium` (not run on `m8g.medium`), and the agent CLI and the Agent both stayed up
  - It is not enough for a large Go build. `go build ./...` of this repository was OOM-killed on both, one `compile` of the AWS EC2 SDK needing about 1.7 GiB on its own. **Members who build big projects start at 8 GiB**
- **Burstable classes run in `unlimited` credit mode (`t4g` measured, `t3` the account default).** A `t4g` slot the Control Plane launched was measured as `unlimited`, and the account default is `unlimited` for both `t4g` and `t3`; the `t3` credit mode on a launched slot was not measured. Sustained CPU on them bills surplus credits on top of the hourly price, so a member who keeps the CPU busy can cost more than on `m8g`. Whether a long build actually spends surplus credits was not measured
- **Set `Ec2SlotTerminateAfterSec` on the deployment when you add these classes** (`14400` is recommended, [Three limits](slot-pool.md#three-limits)). It is one deployment-wide value that covers every class. At the default `0` an idle slot is never terminated, so spare slots and their root volumes pile up until `Ec2MaxSlots`

On-demand Linux prices in ap-northeast-1, shared tenancy (Pricing API, November 2026):

| Type | vCPU / memory | USD per hour |
|---|---|---|
| m8g.medium | 1 / 4 GiB | 0.05797 |
| m8g.large | 2 / 8 GiB | 0.11594 |
| t4g.medium | 2 / 4 GiB | 0.0432 |
| t4g.large | 2 / 8 GiB | 0.0864 |
| t3.medium | 2 / 4 GiB | 0.0544 |
| t3.large | 2 / 8 GiB | 0.1088 |
| r8g.medium | 1 / 8 GiB | 0.07106 |

Prices change; read them again before you decide.

## Set the default class

`Ec2DefaultSlotClass` is the `id` of the class a member with no per-user and no per-tenant choice lands on. Empty means the first class in `Ec2SlotTypes`.

- **Changing it on a running deployment moves every member who has not chosen for themselves.** If the architecture changes, each of their homes reinstalls its architecture-dependent tools on the next start
- A running stack keeps its own `Ec2SlotTypes` unless you pass the parameter, so upgrading the Control Plane puts a picker in front of nobody

## Move a member to a class of another architecture

Tenant administrators choose with the tenant's **Default machine** and the member detail's **Machine** ([Set the workspace size](../../admin/resources/workspace-size.md#choose-the-machine)).

Moving across architectures is safe but not free.

| What stays | What is reinstalled |
|---|---|
| The home itself | Architecture-dependent contents such as the CLIs in `~/.local`, nvm's node and Chromium. Reinstalled automatically on the next start |
| What is under `~/repos` | What the member built, such as `node_modules`, `target` and `.venv`. The member reinstalls these; [af-arch-repair](../../ref/cli/af-arch-repair.md) names the ones it detects in a notification |

The Console warns before saving.

## Related

- [Start using the EC2 slot pool](slot-pool.md)
- [Set the workspace size](../../admin/resources/workspace-size.md): tenant administrators choose the machine
- [Replace and stop slots](slot-maintenance.md): replacing slots whose AMI changed
- [Keep costs down](cost.md)
