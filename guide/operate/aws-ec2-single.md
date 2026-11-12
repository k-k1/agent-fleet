---
audience: "someone standing up the Docker Compose deployment on a single AWS EC2 instance"
source_of_truth: "`deploy/aws/ec2-single/cfn.yaml` and `deploy/compose/release.sh` (a contradiction is a bug in this page)"
---

# Deploy on a single EC2 instance

English | [日本語](aws-ec2-single.ja.md)

Stand up one EC2 instance with CloudFormation and run the Docker Compose deployment on it.
Only the AWS CLI is required (no Terraform), and one command tears it all down.

## How it works

| What you do | What happens |
|---|---|
| Generate an SSH key locally and deploy the stack | An Ubuntu 24.04 instance (Docker installed by cloud-init), an Elastic IP, a Route 53 A record and the imported key pair are created |
| Build the release bundle and images locally and copy them to the instance | The deployment stands up from the bundle alone, with no git clone on the instance |
| Write `.env` on the instance and run `docker compose up -d` | Caddy obtains a Let's Encrypt certificate and the Console opens at `https://<Fqdn>` |
| Delete the stack when you no longer need it | Everything in the stack is removed |

This is Docker Compose on one VM, not the ECS deployment. Compare it with ECS in [Choose where to deploy](choose.md).

## Before you start

| What you need | Notes |
|---|---|
| The AWS CLI and IAM rights to deploy | EC2, Route 53 and CloudFormation. No SSM write and no IAM capabilities are needed |
| A Route 53 public hosted zone | If the domain is not delegated to that zone, see [The DNS caveat](#the-dns-caveat) below |
| Docker on an x86_64 (amd64) machine | Builds the release bundle and images. The instance is amd64, so images built on an arm64 machine such as an Apple Silicon Mac do not run on it |
| An IdP client for sign-in | [Set up how people sign in](signin/overview.md) |

## 1. Create the stack

Generate the SSH key locally. The private key never leaves your machine; only the public key is imported, so no `ssm:PutParameter` permission is needed.

```bash
ssh-keygen -t ed25519 -N '' -f agent-fleet-test-key      # -> agent-fleet-test-key(.pub)

aws cloudformation deploy \
  --stack-name agent-fleet-test \
  --template-file cfn.yaml \
  --parameter-overrides \
      HostedZoneName=example.com. \
      Fqdn=af.example.com \
      SSHLocation=<your-ip>/32 \
      PublicKey="$(cat agent-fleet-test-key.pub)"

# read the outputs (public IP, FQDN, ssh command)
aws cloudformation describe-stacks --stack-name agent-fleet-test \
  --query 'Stacks[0].Outputs' --output table
```

Run the commands in `deploy/aws/ec2-single/`.

| Parameter | Default | Meaning |
|---|---|---|
| `HostedZoneName` | (required) | An existing Route 53 public hosted zone, with the trailing dot (`example.com.`) |
| `Fqdn` | (required) | The Console's host name. Used for TLS and the OAuth redirect |
| `SSHLocation` | `0.0.0.0/0` | The CIDR allowed to SSH. Pass your `<ip>/32` to lock it down |
| `PublicKey` | (required) | The SSH public key to import (the contents of your `*.pub`) |
| `InstanceType` | `t3.large` | `t3.medium`, `t3.large` or `t3.xlarge`. For `t3.medium` (4 GB), see [A small instance](#a-small-instance) below |
| `VolumeSizeGb` | `30` | The size of the root volume |
| `LatestUbuntuAmi` | Canonical's SSM parameter | The latest Ubuntu 24.04 AMI. Leave it as is; it works in any region |

The outputs are `PublicIp`, `Fqdn`, `ConsoleUrl`, `OAuthRedirectUri` and `SshCommand`. The security group opens port 22 to `SSHLocation` and ports 80 and 443 to everyone.

### The DNS caveat

The Route 53 A record only works if the domain is delegated to that hosted zone (the registrar's NS are the zone's `awsdns-*` servers).
If the domain lives at another DNS provider, do one of these:

- Add the A record there instead
- Skip DNS and use **sslip.io** on the Elastic IP. Set `Fqdn` and `PUBLIC_DOMAIN` to `<dash-separated IP>.sslip.io` (e.g. `203-0-113-7.sslip.io`): it resolves to the IP with no setup and still gets a real Let's Encrypt certificate

## 2. Ship the bundle to the instance

Build the images and the bundle on your machine, then copy them over. Nothing is cloned with git on the instance.

```bash
cd ../../..                                  # repo root
VERSION=0.1.0 deploy/compose/release.sh --save
scp -i deploy/aws/ec2-single/agent-fleet-test-key \
    deploy/compose/dist/agent-fleet-images-0.1.0.tar.gz \
    deploy/compose/dist/agent-fleet-0.1.0.tar.gz \
    ubuntu@<public-ip>:~
```

`0.1.0` is an example; use the version you build. `--save` writes, besides the bundle (`agent-fleet-<version>.tar.gz`), a tar of the images saved with `docker save` (`agent-fleet-images-<version>.tar.gz`).

> [!IMPORTANT]
> **Run this on an x86_64 (amd64) machine.** `--save` writes the images for the architecture of the machine it runs on only (it refuses several architectures). The template's AMI is amd64 Ubuntu, so images built on an arm64 machine do not run on the instance.

## 3. Start it on the instance

```bash
ssh -i deploy/aws/ec2-single/agent-fleet-test-key ubuntu@<public-ip>
# on the VM:
tar xzf agent-fleet-0.1.0.tar.gz && cd agent-fleet-0.1.0
./load-images.sh ~/agent-fleet-images-0.1.0.tar.gz    # or: gunzip|docker load
cp .env.example .env
#   set DATA_DIR=/srv/agent-fleet/data, DOCKER_GID=$(getent group docker|cut -d: -f3),
#   PUBLIC_DOMAIN / PUBLIC_BASE_URL=https://<Fqdn>, the login IdP (GOOGLE_OAUTH_*,
#   AF_OIDC_PROVIDERS + AF_OIDC_<ID>_*, and/or AF_GITHUB_ALLOWED_ORGS +
#   GITHUB_OAUTH_CLIENT_ID/_SECRET), AF_COOKIE_SECRET,
#   AF_MASTER_KEY (generate!), AF_OAUTH_ALLOWED_DOMAINS, SUPER_ADMIN_EMAILS
sudo mkdir -p /srv/agent-fleet/data && sudo chown 1000:1000 /srv/agent-fleet/data
docker compose up -d
docker compose logs -f caddy cp
```

- What each `.env` value means, and how to generate the secrets, is the same as in [Deploy with Docker Compose](compose/install.md#1-configure-and-start-from-source)
- Ports 80 and 443 are open, so Caddy obtains a real Let's Encrypt certificate for `<Fqdn>`
- Register `https://<Fqdn>/oauth2/callback` in the IdP's client. That one URI serves every IdP you enable
- Open `https://<Fqdn>` and sign in

About the first administrator:

- `AF_OAUTH_ALLOWED_DOMAINS` is what gets the first administrator in. After that, someone invited to a tenant in the Admin panel signs in without being listed there too
- Each tenant's sign-in page is `https://<Fqdn>/login/<slug>`
- `SUPER_ADMIN_EMAILS` is read once at startup and is the only source of truth. Removing an address and restarting also revokes the role in the database

### A small instance

On `t3.medium` (4 GB), lower `WS_MEMORY` in `.env` (e.g. `2g`) before starting a workspace. Otherwise the instance runs out of memory during the Claude install.

## Protect the instance metadata

The instance requires IMDSv2 with a hop limit of 1 (`MetadataOptions` in `cfn.yaml`).

- The template attaches no instance profile. The setting is there so that one attached later reaches the host-network Control Plane (one hop) and not the workspaces on docker bridges (two hops)
- Workspaces are started with `AWS_EC2_METADATA_DISABLED=true`. With `AF_WS_WORKLOAD_AWS=1` on the Control Plane they are not, but the hop limit stays, so the instance profile is out of reach either way
- This template does not offer the Bedrock routes the deployment pays for (provided routes): the Control Plane would need an AWS identity of its own to assume the route role. Use the ECS stack's `ProvidedRoutes=on`

For a stack created before this setting, apply it to the running instance rather than by updating the stack: `LatestUbuntuAmi` resolves again on every update, and a newer AMI replaces the instance.

```bash
aws ec2 modify-instance-metadata-options --instance-id <id> \
  --http-tokens required --http-put-response-hop-limit 1 --http-endpoint enabled
```

Workspaces that were already running keep their old container and environment until they are stopped and started in the Console (`docker compose up -d` does not recreate them). After that, check from a workspace shell:

| What to check | `AF_WS_WORKLOAD_AWS` off (the default) | `AF_WS_WORKLOAD_AWS=1` |
|---|---|---|
| `echo ${AWS_EC2_METADATA_DISABLED:-unset}` | Prints `true` | `unset`, by design |
| `aws sts get-caller-identity` with no profile | Fails with "Unable to locate credentials" | — |
| The token request below | Does not print `200` | Does not print `200` |

```bash
curl -s -o /dev/null -m 3 -w '%{http_code}' -X PUT -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' http://169.254.169.254/latest/api/token
```

The other targets are in [Other operational controls](secure/controls.md#keep-the-hosts-cloud-identity-from-workspaces).

## 4. Tear it down

Everything in the stack is removed.

```bash
aws cloudformation delete-stack --stack-name agent-fleet-test
aws cloudformation wait stack-delete-complete --stack-name agent-fleet-test
```

The Elastic IP, security group, instance, Route 53 record and key pair are all deleted with the stack.
Also delete the **sign-in IdP client** you used for testing (the Google Cloud Console OAuth client, or the Entra / Okta / … app registration): its secret was in use during the test.

### When the key pair does not delete and the stack goes `DELETE_FAILED`

The `AWS::EC2::KeyPair` handler touches SSM Parameter Store even for imported keys. If the deploying user lacks `ssm:*` (e.g. only `AmazonSSMReadOnlyAccess`), the key pair fails to delete with `InternalFailure` and the stack goes `DELETE_FAILED`.
Delete the stack retaining that one resource, then delete the key pair directly.

```bash
aws cloudformation delete-stack --stack-name agent-fleet-test --retain-resources KeyPair
aws cloudformation wait stack-delete-complete --stack-name agent-fleet-test
aws ec2 delete-key-pair --key-name agent-fleet-test-key
```

Granting the deploying user `ssm:PutParameter` and `ssm:DeleteParameter` on `/ec2/keypair/*` avoids it entirely.

## Related

- [Deploy with Docker Compose](compose/install.md)
- [Install Agent Fleet](install.md)
- [Back up and restore](run/backup-restore.md), [Upgrade](run/upgrade.md)
