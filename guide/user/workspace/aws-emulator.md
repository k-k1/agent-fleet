---
audience: "People who want to try AWS code without touching a real AWS account"
source_of_truth: "MiniStack's documentation, and af-aws-exec --help"
---

# Try AWS code against a local emulator

English | [日本語](aws-emulator.ja.md)

Run [MiniStack](https://github.com/ministackorg/ministack), an open-source AWS API emulator, inside your workspace, and
you can try code, templates and scripts without touching a real account.

## How it works

| What you do | What the agent does |
|---|---|
| Install and start MiniStack | Sends commands to the emulator with the dedicated profile |
| Write the dedicated profile into `~/.aws/config` | Does its trial and error inside the emulator |
| Check the result against real AWS at the end | Runs it against the real account with `af-aws-exec` |

Installed from PyPI, MiniStack needs no Docker and starts in seconds. It is not in the image, so install it yourself.

## Install and start MiniStack

```sh
python3 -m venv ~/.local/ministack && ~/.local/ministack/bin/pip install ministack
GATEWAY_PORT=14566 ~/.local/ministack/bin/ministack
```

- Ports are shared with other sessions. Pick a free one; the default 4566 may be taken
- Stop the server when you are done

## Create a dedicated profile

Add a profile with a name nothing uses yet to `~/.aws/config`, **outside** the managed block.

```ini
[profile ministack]
region = us-east-1
endpoint_url = http://127.0.0.1:14566
aws_access_key_id = 000000000001
aws_secret_access_key = test
```

Pick a name that appears in none of these.

| Where to check | If the name is already there |
|---|---|
| `aws configure list-profiles` / `~/.aws/credentials` | The keys in `~/.aws/credentials` win, and real credentials are sent to the emulator |
| `af-aws-exec --list` (the profiles in Settings) | The Settings profile of the same name is no longer written out |

## Use it

Name the profile on every command.

```sh
aws --profile ministack s3 mb s3://demo-bucket
aws --profile ministack s3 ls
```

MiniStack is not used through `af-aws-exec`: `af-aws-exec` strips endpoint settings on purpose.

### Tools that ignore the profile's endpoint

Only AWS CLI v2 and current SDKs read a profile's `endpoint_url`. These do not:

- AWS SDK for Java 1.x, JavaScript v2, Go v1
- Tools that resolve endpoints themselves

They go to real AWS with the dummy keys. Give the client the endpoint itself: Terraform's `endpoints` block, or the
SDK's endpoint setting.

`AWS_ENDPOINT_URL*` or `AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=true` exported in the shell take precedence over the
profile's endpoint. Check for those too.

## Keep it away from real AWS

> [!WARNING]
> **Do not `export` dummy keys into your shell** (`AWS_ACCESS_KEY_ID=test` and so on). Every command then carries them,
> and one that forgot `--endpoint-url` sends its request to real AWS (where the dummy keys are rejected).

What happens when you forget the profile depends on the workspace.

| Workspace | A command without the profile |
|---|---|
| A workspace in the usual setup (no AWS identity handed in) | Stops with "Unable to locate credentials" |
| A workspace whose administrator gave the workspace role back, or the native runtime with the machine's own credentials | Runs against real AWS as that identity |

In the last two, never leave the profile out. The details are in
[af-aws-exec](../../ref/cli/af-aws-exec.md#why-a-command-needs-a-profile).

## Share one server between several sessions

The 12-digit access key becomes the account ID. Resources are separate per account, so sessions that must not see each
other's resources use different keys.

Whether regions separate them depends on the service (an S3 Bucket, for example, is visible from every region). Do not
rely on regions to keep two sessions apart.

## What does not work

- **RDS, ElastiCache and ECS do not run.** They start real Docker containers, and the workspace has no Docker
- RDS still answers `available` and returns an endpoint such as `localhost:5432` where no database of yours is, and
  where another PostgreSQL in the workspace may be listening. When you need a database, use `af-db`
  ([Run database-backed tests](databases.md))
- **It is an emulator.** A template or IAM policy that works here can still fail on AWS.
  Use it for trial and error, and check against real AWS with `af-aws-exec` before relying on it

## Keep the state

State lives in memory and is gone when the server stops.

| Environment variable | What it keeps |
|---|---|
| `PERSIST_STATE=1` | The services' state (not the contents of S3 objects) |
| `S3_PERSIST=1` | The contents of S3 objects |

Both keep their files under `/tmp` by default. `/tmp` is wiped when the workspace restarts, so point `STATE_DIR` and
`S3_DATA_DIR` at directories in your home.

## Related

- [Use AWS SSO](../integrations/aws-sso.md): run it against a real account
- [af-aws-exec](../../ref/cli/af-aws-exec.md)
- [Run database-backed tests](databases.md)
