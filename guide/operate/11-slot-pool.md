---
audience: "a deployment administrator of the ecs-ec2 target, where workspaces run on a pool of EC2 slots"
source_of_truth: "`deploy/aws/ecs/cfn/40-ec2-pool.yaml` for the commands; the Admin → Slots page for each slot's state"
---

# 11. The ecs-ec2 slot pool

English | [日本語](11-slot-pool.ja.md)

This chapter has moved to one page per task.

| Page | What you can do |
|---|---|
| [Start using the EC2 slot pool](aws-ecs/slot-pool.md) | Build the pool, set its parameters, the memory reserve, the alarms and the golden snapshot, and move or lose an AZ |
| [Replace and stop slots](aws-ecs/slot-maintenance.md) | Replace outdated slots, protect the management agents from running out of memory, and notice and stop a slot that does not answer |
