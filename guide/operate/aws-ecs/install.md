---
audience: "Deployment administrators building a new deployment on AWS ECS (`ecs` / `ecs-ec2`)"
source_of_truth: "`deploy/aws/ecs/cfn/*.yaml` for the templates, `deploy/aws/ecs/release-ecr.sh` and `standup.sh` for the scripts, `deploy/aws/ecs/cfn/PARAMETERS.md` for the parameters in detail"
---

# Build on ECS

English | [日本語](install.ja.md)

Build Agent Fleet on AWS ECS, start the Control Plane, and get to the point where you can sign in with your IdP.
The stacks and their order are in [Understand the ECS layout](overview.md).

## How it works

| What you do | What happens |
|---|---|
| Once per account, create the service-linked roles. If you will split spend, also turn on billing access in Billing | ECS clusters can be created. The Control Plane can read spend |
| Deploy `00-network`, `10-data` and `20-platform` | The network, EFS and RDS, ECR, the cluster and the IAM roles are built |
| Push the images to ECR | ECS can pull them |
| Put the Control Plane's secrets in SSM and register the redirect URI with your IdP | The Control Plane can read its secrets, and the IdP can send sign-ins back |
| Deploy `30-ingress` | The certificate, ALB, DNS and Control Plane service are built, and you can sign in to the Console |
| If you split spend, start one workspace on the day you build | The Control Plane writes the tags; once AWS discovers the keys (up to 24 hours), the Control Plane activates the cost allocation tags |

These are the same steps `standup.sh` runs in order. Run the commands from `deploy/aws/ecs/` in the repository.

## Before you start (once per account)

### Create the service-linked roles

A fresh account has no `AWSServiceRoleForECS`, and creating a cluster with a Service Connect default namespace fails with "ECS Service Linked Role is not ready".

If you run self-hosted inference engines (`60-engines`), also create `AWSServiceRoleForEC2Fleet`, which AWS asks for before `CreateFleet`. Calls have gone through in an account without it, but it is cheap insurance.

`standup.sh` creates both. By hand, as below. It is idempotent: ignore the "has been taken" error on a re-run.

```bash
aws iam create-service-linked-role --aws-service-name ecs.amazonaws.com || true
aws iam create-service-linked-role --aws-service-name ec2fleet.amazonaws.com || true
# and, for a deployment that declares a `spot` offer:
aws iam create-service-linked-role --aws-service-name spot.amazonaws.com || true
```

For a deployment that declares a `spot` offer, run the third line too.

### Turn on billing access (optional)

If you will split spend per member, turn on "IAM user and role access to Billing information" in the Billing console of the payer account. The templates cannot set it.
While it is off, every `ce:GetCostAndUsage` call by `CpTaskRole` fails even though the policy is attached.

Activating the cost allocation tags comes after the build ([Split spend per member](#split-spend-per-member-optional)).

## Deploy the base stacks

### Capabilities each stack needs

| Stack | `--capabilities` | Why |
|---|---|---|
| `00-network` | `CAPABILITY_IAM` | It declares the egress gateway's instance role |
| `10-data` | `CAPABILITY_AUTO_EXPAND CAPABILITY_IAM` | It declares `Transform: AWS::LanguageExtensions`. Under `Persistence=retain` it also creates the unnamed IAM role AWS Backup uses for the EFS backups |
| `20-platform` | `CAPABILITY_NAMED_IAM` | It creates named IAM roles |
| `40-ec2-pool`, `60-engines`, `30-ingress` | `CAPABILITY_NAMED_IAM` | Each attaches a named managed policy to the Control Plane's task role |

Without them the change set is refused with `InsufficientCapabilitiesException … Requires capabilities : [CAPABILITY_IAM]` or similar.

### Deploy in order

1. Deploy `00-network`

   ```bash
   aws cloudformation deploy --stack-name af-ecs-network --template-file cfn/00-network.yaml \
     --capabilities CAPABILITY_IAM \
     --profile af-sandbox --region ap-northeast-1
   ```

2. Deploy `10-data`. For a deployment whose data must survive, use `Persistence=retain`

   ```bash
   aws cloudformation deploy --stack-name af-ecs-data \
     --template-file cfn/10-data.yaml --capabilities CAPABILITY_AUTO_EXPAND CAPABILITY_IAM \
     --parameter-overrides Persistence=retain \
     --profile af-sandbox --region ap-northeast-1
   ```

3. Deploy `20-platform`

   ```bash
   aws cloudformation deploy --stack-name af-ecs-platform \
     --template-file cfn/20-platform.yaml --capabilities CAPABILITY_NAMED_IAM \
     --profile af-sandbox --region ap-northeast-1
   ```

4. Only if you use them, deploy `40-ec2-pool` (`ecs-ec2`), `50-tts` (speech) and `60-engines` (self-hosted inference), in that order

`Persistence` defaults to `delete`: deleting the stack deletes EFS and RDS with it.

## Push the images to ECR

The ECR repositories belong to `20-platform`, so deploy it first. Creating the repositories out of band makes the later `20-platform` deploy fail with AlreadyExists.

`release-ecr.sh` only checks that the repositories exist; it does not create them.

```bash
# images already local (built by deploy/release/build.sh):
VERSION=<v> ./release-ecr.sh --profile af-sandbox --region <region>
# or from the air-gap images tar (B) when the build host is a different machine:
VERSION=<v> ./release-ecr.sh --profile af-sandbox --region <region> \
    --images-tar agent-fleet-images-<v>.tar.gz
```

- The first form is for images built on this machine by `deploy/release/build.sh`, the second for pushing from the images tar made on another machine
- A deployment that runs its own engines also needs `af-engine-tools` in ECR, under the tag `60-engines` asks for. That image exists only in GHCR and is never built locally or shipped, so it is copied registry to registry with `crane copy` rather than tagged and pushed
- If neither ECR nor GHCR has that tag, the script stops. Run `engine-tools-image.yml` with that tag first

### Push by hand

The script is a transcription of these commands.

```bash
aws ecr get-login-password --region <region> | docker login --username AWS \
    --password-stdin <acct>.dkr.ecr.<region>.amazonaws.com
# tag + push the images already built for local (no heavy rebuild here):
docker tag agent-fleet/control-plane:<tag> <acct>.dkr.ecr.<region>.amazonaws.com/af-control-plane:<tag>
docker tag agent-fleet/workspace:<tag>     <acct>.dkr.ecr.<region>.amazonaws.com/af-workspace:<tag>
docker push <acct>.dkr.ecr.<region>.amazonaws.com/af-control-plane:<tag>
docker push <acct>.dkr.ecr.<region>.amazonaws.com/af-workspace:<tag>
```

## Start the Control Plane (`30-ingress`)

The Control Plane does the sign-in itself (`AuthMode=oauth`): Google, or any OIDC IdP (Entra ID, Okta, Keycloak, Auth0, Cognito, GitLab). The ALB only terminates TLS.
The Control Plane keeps its state in RDS Postgres and workspace homes live on EFS. Both are wired from `10-data`'s exports, so `10-data` must already be up.

### 1. Put the Control Plane's secrets in SSM

The secrets live in SSM SecureString parameters and are read by the execution role. They never go into a plaintext environment variable.

```bash
RG=ap-northeast-1
aws ssm put-parameter --profile af-sandbox --region $RG --type SecureString \
  --name /af-cp/cookie-secret       --value "$(openssl rand -hex 32)"
aws ssm put-parameter --profile af-sandbox --region $RG --type SecureString \
  --name /af-cp/master-key          --value "$(openssl rand -hex 32)"
# one of these two, or both — only the IdPs you enable are wired in
aws ssm put-parameter --profile af-sandbox --region $RG --type SecureString \
  --name /af-cp/google-client-secret --value "<GOOGLE_OAUTH_CLIENT_SECRET>"   # your terminal, not shared
aws ssm put-parameter --profile af-sandbox --region $RG --type SecureString \
  --name /af-cp/oidc-client-secret   --value "<AF_OIDC_<ID>_CLIENT_SECRET>"
```

- Put only the secrets of the IdPs you use. Type the values in your own terminal, never on a shared screen
- To encrypt `master-key` with your own KMS key instead of `aws/ssm`, add `--key-id <key ARN>` to its `put-parameter` and set `20-platform`'s `MasterKeyKmsKeyArn` to the same ARN **first**: the execution role's `kms:Decrypt` must exist before the Control Plane task starts. Details are in [Put the keys at rest on AWS KMS](../keys/kms.md#encrypt-the-afmasterkey-parameter-with-your-own-key)

The GitHub and Bitbucket OAuth apps are not deployment configuration. There is no CloudFormation parameter and no SSM entry for them; a tenant administrator registers them in the Console ([Choose the OAuth apps for integrations](../../admin/access/oauth-apps.md)).

### 2. Register the redirect URI with your IdP

Add `https://<your-fqdn>/oauth2/callback` to your IdP client's authorized redirect URIs. That one URI serves every IdP you enable.
How to create the client for each IdP is in [Set up how people sign in](../signin/overview.md).

### 3. Deploy `30-ingress`

`30-ingress.yaml` is over 51,200 bytes, so it is handed over through the S3 bucket `20-platform` created ([Keep templates under the size limit](template-size.md)).

For a deployment that signs in with Google:

```bash
CFN_BUCKET=$(aws cloudformation describe-stacks --stack-name <platform stack> --query "Stacks[0].Outputs[?OutputKey=='CfnTemplatesBucket'].OutputValue" --output text --profile <p> --region <r>)
aws cloudformation deploy --stack-name af-ecs-ingress \
  --template-file cfn/30-ingress.yaml --s3-bucket "$CFN_BUCKET" --s3-prefix cfn --capabilities CAPABILITY_NAMED_IAM \
  --parameter-overrides GoogleClientId=<id> \
    Fqdn=af.example.com HostedZoneId=<your-zone-id> \
    AllowedEmails=you@example.com SuperAdminEmails=you@example.com \
  --profile af-sandbox --region $RG
```

For a Microsoft Entra ID deployment (or Okta, Keycloak and the like), pass the `Oidc*` parameters instead of `GoogleClientId`. With `GoogleClientId` empty, Google is simply not offered.

```bash
  --parameter-overrides OidcProviderId=ENTRA \
    OidcIssuer=https://login.microsoftonline.com/<tenant-guid>/v2.0 \
    OidcClientId=<application-client-id> OidcTrust=issuer \
    OidcLabelJa="Microsoft でサインイン" OidcLabelEn="Sign in with Microsoft" \
    Fqdn=af.example.com HostedZoneId=<your-zone-id> \
    AllowedDomains=example.co.jp SuperAdminEmails=you@example.co.jp
```

| Parameter | Details |
|---|---|
| `Fqdn` / `HostedZoneId` | The Console's name and the Route 53 hosted zone that contains it. The zone must be in this account |
| `AllowedEmails` / `AllowedDomains` | The email addresses and domains allowed to sign in |
| `SuperAdminEmails` | Who becomes a deployment administrator (super_admin) |
| `OidcIssuer` | For Entra ID, pin it to your tenant GUID. With `/common/` or `/organizations/` every Microsoft account in the world reaches the sign-in, and personal accounts can rewrite their own email, so the Control Plane refuses to start unless `OidcAllowedTids` is set |
| `ImageTag` | For release images, add `ImageTag=<v>`. The default `dev` matches a `:dev` push for development |

ACM's DNS validation and the Route 53 alias are automated. Certificate issuance adds a few minutes to the first deploy.

### 4. Check that it works

1. Check that `https://<your-fqdn>/readyz` answers 200. `/healthz` answers 200 even with a dead database, so `/readyz` is the one to check
2. Open the Console and sign in as one of the `SuperAdminEmails`
3. If you split spend per member, start one workspace now ([Split spend per member](#split-spend-per-member-optional))

## Split spend per member (optional)

Optional, but it cannot be backfilled: if you want it, start on the day you build.

**Deploying the stacks does none of this.** The templates carry only the IAM permission (`ce:GetCostAndUsage` on `CpTaskRole`). The tags are written by the running Control Plane, and there is no CloudFormation resource that activates a cost allocation tag. The order is:

1. Before the build, turn on billing access ([Turn on billing access](#turn-on-billing-access-optional))
2. Deploy the stacks and start the Control Plane (the steps above on this page)
3. **Start one workspace.** The Control Plane writes the tags onto resources. Start it on the day you build, even if nobody uses it yet
4. Wait for AWS to discover the tag keys (up to 24 hours)
5. Activate the cost allocation tags. **Normally the Control Plane does this for you** (see below)

| Point | Details |
|---|---|
| Not retroactive | Spend from before activation can never be attributed to anyone. Neither can the spend during the wait |
| Once per key per account | Activation is keyed on the tag key alone, with no value dimension. New tenants and members need nothing |
| Payer account only | Under AWS Organizations only the management (payer) account can activate |
| An unseen key cannot be activated | A key AWS has never seen on a real resource is refused with `ValidationException: Tag keys not found` |
| Do not activate `af-workspace` | Its value is derived from the member's email address, and activating it copies that into the billing data (CUR, Cost Explorer, invoice CSVs). The Control Plane joins on `af-membership`, an opaque random id |

The Control Plane retries activation on every poller tick until AWS has discovered each key. It skips `af-workspace` and leaves alone any tag a person switched off.

Activate by hand only if you removed the `CostAllocationTagActivation` statement from `CpTaskRole`, or if the Console reports it could not.

```bash
aws ce update-cost-allocation-tags-status --region us-east-1 \
  --cost-allocation-tags-status '[{"TagKey":"af-membership","Status":"Active"},
    {"TagKey":"af-tenant","Status":"Active"},{"TagKey":"af-role","Status":"Active"},
    {"TagKey":"af-pool","Status":"Active"},{"TagKey":"af-slot-size","Status":"Active"}]'
```

`list-cost-allocation-tags` shows what AWS has found. A key listed as `Inactive` can be made `Active` at once; a key missing from the list is one nothing has written yet. The wait applies only the first time a key appears.

## Related

- [Understand the ECS layout](overview.md)
- [Set up the alarm for an unreachable database](alarms.md): set it right after building
- [Turn on optional features with stack parameters](options.md)
- [IAM permissions for deploying](iam.md)
- [Troubleshoot ECS](troubleshooting.md)
