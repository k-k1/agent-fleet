---
audience: "Deployment administrators who deploy the AWS ECS (`ecs` / `ecs-ec2`) stacks by hand"
source_of_truth: "`af_cfn_deploy` in `deploy/aws/ecs/env.sh`, `CfnTemplatesBucket` in `cfn/20-platform.yaml`"
---

# Keep templates under the size limit

English | [日本語](template-size.ja.md)

CloudFormation does not take a template body over 51,200 bytes directly. `30-ingress.yaml` sits just under that limit and is allowed to cross it, so it is always handed over through S3.
With this page, a deploy by hand does not stop at the limit, even on a release whose template has grown.

## How it works

| What you do | What happens |
|---|---|
| Deploy with `standup.sh` or `update.sh` | The script measures every template on every call and sends the ones over the limit through S3. Nothing to do |
| Deploy `30-ingress` by hand | Always add `--s3-bucket "$CFN_BUCKET" --s3-prefix cfn`. For a template over the limit, the AWS CLI refuses without it |
| Tear down | `teardown.sh` empties the S3 bucket before deleting `20-platform` |

## When you deploy by hand

Every `aws cloudformation deploy` of `30-ingress` carries `--s3-bucket` and `--s3-prefix`.

```bash
CFN_BUCKET=$(aws cloudformation describe-stacks --stack-name <platform stack> --query "Stacks[0].Outputs[?OutputKey=='CfnTemplatesBucket'].OutputValue" --output text)
aws cloudformation deploy --stack-name <ingress stack> --template-file cfn/30-ingress.yaml --s3-bucket "$CFN_BUCKET" --s3-prefix cfn \
  --capabilities CAPABILITY_NAMED_IAM --parameter-overrides ...
```

- `CFN_BUCKET` is `20-platform`'s output `CfnTemplatesBucket`
- That S3 bucket is created by `20-platform`. `30-ingress` is always deployed after `20-platform`, so the bucket already exists
- Templates put in the bucket expire after 7 days
- Whoever deploys needs `s3:PutObject` and `s3:GetObject` on this bucket ([IAM permissions for deploying](iam.md))

The other templates are kept inside the limit and are handed over without S3.

## What the scripts do

| Script | What it does |
|---|---|
| `af_cfn_deploy` in `env.sh` | The only entry point to `cloudformation deploy`. It measures the file on every call and goes through S3 above 51,200 bytes. The bucket comes from the environment variable `AF_CFN_BUCKET`, or else from `20-platform`'s output `CfnTemplatesBucket` |
| `standup.sh`, `update.sh` | Deploy through `af_cfn_deploy` |
| `teardown.sh` | Empties the S3 bucket before deleting `20-platform`, because CloudFormation cannot delete a bucket that is not empty |

The size is measured every time rather than remembered by template name, so the next template that grows works the same way.

## Why the template descriptions are short

The limit applies to the whole template body. A parameter's `Description:` and a YAML comment count the same.

- So each `Description:` in the templates is one or two lines
- The measurements, trade-offs and traps are in [`cfn/PARAMETERS.md`](https://github.com/k-k1/agent-fleet/blob/main/deploy/aws/ecs/cfn/PARAMETERS.md)

## When it goes wrong

| Symptom | Cause and fix |
|---|---|
| `aws cloudformation deploy` ends with an error that the template is too large, and the stack events show nothing | The template is over the limit, and the AWS CLI refused on size before calling the API. Run it again with `--s3-bucket "$CFN_BUCKET" --s3-prefix cfn` |
| `af_cfn_deploy` stops with "needs to go via S3, but no hand-off bucket can be resolved" | `20-platform` is not up yet, or its output `CfnTemplatesBucket` cannot be read. Deploy `20-platform` first, or name a bucket with `AF_CFN_BUCKET` |
| `teardown.sh` fails to delete `20-platform` | The S3 bucket was not emptied. Read the error the bucket-emptying step printed just before |

## Related

- [Build on ECS](install.md)
- [Upgrade on ECS](upgrade.md)
- [Understand the ECS layout](overview.md)
