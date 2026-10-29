# macOS acceptance run (Apple Silicon)

macOS support is experimental until someone runs this on a real Mac (#2961). This page is
the runbook for that day: what to prepare, how to rent a cloud Mac if you have none, and
the one script that turns the checklist into a report.

What is being verified:

- **Docker edition** with `deploy/compose/docker-compose.macos.yml`
  ([compose README, "macOS"](../compose/README.md#macos-experimental-unverified)), on each
  engine you can get: Docker Desktop, OrbStack, colima.
- **Native package** (`linux-arm64`) inside a Lima VM
  ([native README](../native/README.md)).
- **Installers** on macOS itself: `install.sh` refuses with guidance, `install-compose.sh`
  verifies with `shasum`.

## The script

```bash
deploy/macos/acceptance.sh docker ~/agent-fleet-<v>     # a compose bundle with .env filled in
deploy/macos/acceptance.sh native --version <v>         # creates, uses and deletes a Lima VM
deploy/macos/acceptance.sh installer --version <v>
```

Each run writes `macos-acceptance-<mode>-<time>.md`: one row per check (PASS / FAIL /
SKIP / INFO) and a result line. It stops early only when there is nothing left to check
(the stack does not come up, the workspace does not start), and it takes the stack, or a VM it
created, down afterwards unless you pass `--keep`. It only inspects the workspace container
the Console says it started, never another `af-ws-*` on the same Mac.

| Check | What it proves |
|---|---|
| `env.*` | `.env` has the values the compose README asks for; `DOCKER_GID` matches the socket as a container sees it |
| `compose.up`, `cp.health` | the overlay starts and the Console answers on the Mac's `127.0.0.1:8099` |
| `ws.start` | the CP reached the Agent through `host.docker.internal` (a start still `starting` after the CP's health budget is polled until running) |
| `ws.arch` | the workspace runs the `linux/arm64` image |
| `ws.home_owner`, `ws.ssh_mode`, `ws.write` | ownership and modes on the engine's file share |
| `ws.callback` | the workspace reached the CP's workspace listener (401 without a token) |
| `ws.chromium` | headless Chromium renders inside the workspace |
| `io.home`, `io.tmp` | write throughput of the shared home against the container's disk (information) |
| `ws.stop`, `ws.restart`, `ws.persist` | stop → start keeps the home (a stop that fails is its own FAIL) |
| `cleanup` | the stack or the VM this run created is gone again (a VM that already existed is left alone) |
| `native.*` | the same, for the native package in the VM; the Console through Lima's port forward |
| `install.refuse`, `install_compose` | the installers' macOS behaviour |

Read the report before posting it to the issue. It carries versions and status codes,
and replaces your home directory with `~`, but an error body from the Console is quoted
(one line) and could name something of yours.

## Before you start (do this before renting anything)

- The changes under test are merged, or the branch is pushed, and you know the version
  or commit.
- A compose bundle for that version: `install-compose.sh` with `AF_VERSION=<v>`, or
  `deploy/compose/release.sh` from the checkout.
- Its `.env`, filled in from the compose README's "macOS" section (secrets included).
- This checkout on the Mac (`git clone`), for the script and the installers.

## Renting an EC2 Mac (if you have no Mac)

EC2 Mac instances are bare-metal Mac minis on a Dedicated Host, so Docker Desktop's and
Lima's VM runs at the first level of virtualization; no nested virtualization is needed.
That is inferred, not confirmed by AWS or Docker. **A host is billed for at least 24
hours** (Apple's licence), so prepare everything above first. GitHub's macOS runners do
not work for this: they are VMs, and M1/M2 cannot nest one.

Run the AWS commands as you run any other in your account (in an Agent Fleet workspace:
`af-aws-exec --profile <name> --account <id> -- <command>`):

```bash
# 1. a host (mac2.metal = M1; mac2-m2.metal, mac2-m2pro.metal, mac-m4.metal also exist)
aws ec2 allocate-hosts --instance-type mac2.metal --availability-zone <az> --quantity 1
# 2. the newest macOS AMI for arm64
aws ec2 describe-images --owners amazon --filters 'Name=name,Values=amzn-ec2-macos-*' \
  'Name=architecture,Values=arm64_mac' --query 'sort_by(Images,&CreationDate)[-1].ImageId'
# 3. the instance on that host (a key pair and a security group allowing SSH from you)
aws ec2 run-instances --instance-type mac2.metal --image-id <ami> --key-name <key> \
  --security-group-ids <sg> --placement 'HostId=<host id>' --subnet-id <subnet>
ssh ec2-user@<address>
```

On the Mac:

```bash
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
brew install colima docker docker-compose lima git
# Homebrew's compose is a CLI plugin docker does not find by itself (brew's caveat for
# docker-compose); on a fresh Mac there is no config.json to merge into yet.
mkdir -p ~/.docker
[ -e ~/.docker/config.json ] || echo '{"cliPluginsExtraDirs": ["/opt/homebrew/lib/docker/cli-plugins"]}' > ~/.docker/config.json
docker compose version                               # must answer before going on
colima start --cpu 4 --memory 8 --vm-type vz         # engine 1
deploy/macos/acceptance.sh docker ~/agent-fleet-<v>
colima stop
brew install --cask orbstack && open -a OrbStack     # engine 2
deploy/macos/acceptance.sh docker ~/agent-fleet-<v>
deploy/macos/acceptance.sh native --version <v>
deploy/macos/acceptance.sh installer --version <v>
```

Docker Desktop needs the GUI for its first start, so it needs a screen-sharing session
(enable Screen Sharing and tunnel VNC over SSH); skip it if that is not worth the time.
Switch engines with `docker context use <name>` and stop the previous one first: both
would publish the same ports.

Afterwards: `aws ec2 terminate-instances --instance-ids <id>`, then
`aws ec2 release-hosts --host-ids <host id>` once the 24 hours have passed (it is refused
before). A host left allocated keeps billing.

## Reporting

Paste each report into #2961 as a comment. A FAIL either becomes a fix (and this
directory's script learns the case) or a documented limit in the compose / native
README, which then drops "unverified" for what was actually checked.
