# Agent Fleet on a single EC2 VM (CloudFormation)

Spin up one Ubuntu 24.04 EC2 instance (Docker pre-installed via cloud-init), a
stable Elastic IP, a Route53 A record, and an imported SSH key pair (generated
locally), then run the
**on-prem Compose stack** (`deploy/compose`) on it. This is the environment used
for the P3-10 stage-5 completion gate ("stand it up from the release bundle on a clean
host"). It is a single-VM host — **not** the ECS adapter (P3-7).

Only the AWS CLI is required (no Terraform). Tear everything down with one command.

## 1. Provision

Generate an SSH key locally (the private key never leaves your machine — only the
public key is imported, so no `ssm:PutParameter` permission is needed):

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

> **DNS gotcha.** The `Route53` A record only works if the domain is actually
> delegated to that Route53 hosted zone (registrar NS = the zone's `awsdns-*`
> servers). If the domain lives at another DNS provider, either add the A record
> there instead, or skip DNS entirely and use **sslip.io** on the Elastic IP:
> set `Fqdn` / `PUBLIC_DOMAIN` to `<dash-ip>.sslip.io` (e.g.
> `203-0-113-7.sslip.io`) — it resolves to the IP with zero setup and still
> gets a real Let's Encrypt cert.

Minimal IAM for the deploying user: EC2 + Route53 + CloudFormation (no SSM write,
no IAM caps needed).

## 2. Ship the release bundle to the VM

Build images and bundle on your workstation, then copy over (bundle-only = the
real gate: no git clone on the box):

```bash
cd ../../..                                  # repo root
VERSION=0.1.0 deploy/compose/release.sh --save
scp -i deploy/aws/ec2-single/agent-fleet-test-key \
    deploy/compose/dist/agent-fleet-images-0.1.0.tar.gz \
    deploy/compose/dist/agent-fleet-0.1.0.tar.gz \
    ubuntu@<public-ip>:~
```

## 3. Bring it up on the VM

```bash
ssh -i deploy/aws/ec2-single/agent-fleet-test-key ubuntu@<public-ip>
# on the VM:
tar xzf agent-fleet-0.1.0.tar.gz && cd agent-fleet-0.1.0
./load-images.sh ~/agent-fleet-images-0.1.0.tar.gz    # or: gunzip|docker load
cp .env.example .env
#   set DATA_DIR=/srv/agent-fleet/data, DOCKER_GID=$(getent group docker|cut -d: -f3),
#   PUBLIC_DOMAIN / PUBLIC_BASE_URL=https://<Fqdn>, the login IdP (GOOGLE_OAUTH_*
#   and/or AF_OIDC_PROVIDERS + AF_OIDC_<ID>_*), AF_COOKIE_SECRET,
#   AF_MASTER_KEY (generate!), AF_OAUTH_ALLOWED_DOMAINS, SUPER_ADMIN_EMAILS
sudo mkdir -p /srv/agent-fleet/data && sudo chown 1000:1000 /srv/agent-fleet/data
docker compose up -d
docker compose logs -f caddy control-plane
```

Caddy obtains a real Let's Encrypt cert for `<Fqdn>` (ports 80/443 are open).
Register `https://<Fqdn>/oauth2/callback` in the login IdP's client (that one URI
serves every provider you enable), then open `https://<Fqdn>` and sign in.

## 4. Tear down (removes EVERYTHING in the stack)

```bash
aws cloudformation delete-stack --stack-name agent-fleet-test
aws cloudformation wait stack-delete-complete --stack-name agent-fleet-test
```

The Elastic IP, security group, instance, Route53 record and key pair are all
deleted with the stack.

> **KeyPair delete gotcha.** The `AWS::EC2::KeyPair` handler touches SSM
> Parameter Store even for imported keys; if the deploying user lacks `ssm:*`
> (e.g. only `AmazonSSMReadOnlyAccess`), the KeyPair fails to delete with an
> opaque `InternalFailure` and the stack goes `DELETE_FAILED`. Work around it by
> retaining that one resource and deleting the key pair directly:
>
> ```bash
> aws cloudformation delete-stack --stack-name agent-fleet-test --retain-resources KeyPair
> aws cloudformation wait stack-delete-complete --stack-name agent-fleet-test
> aws ec2 delete-key-pair --key-name agent-fleet-test-key
> ```
>
> (Or grant the deploying user `ssm:PutParameter`/`ssm:DeleteParameter` on
> `/ec2/keypair/*` to avoid it entirely.)

Also delete the throwaway **login IdP client** (the Google Cloud Console OAuth
client, or the Entra/Okta/… app registration) — its secret was used during testing.

## Notes

- `LatestUbuntuAmi` resolves via a Canonical-published SSM public parameter, so
  the template is region-independent — just run in your chosen region.
- `SSHLocation` defaults to `0.0.0.0/0`; pass your `<ip>/32` to lock SSH down.
- On `t3.medium` (4GB), lower `WS_MEMORY` (e.g. `2g`) in `.env` before starting a
  workspace so the box doesn't OOM during the Claude install.
