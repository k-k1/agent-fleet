---
audience: "anyone checking exactly how SSM hosts behave"
source_of_truth: "the SSM hosts of Settings > AWS profiles/SSM, the SSM screen of Start, and the SSM session in the workspace"
---

# SSM reference

English | [日本語](aws-ssm.ja.md)

How to use it is in [Log in to EC2 with SSM](../user/integrations/aws-ssm.md). This page holds the detailed rules.

## Hosts

| Field | Rule |
|---|---|
| Profile to use | Required. A host remembers its profile by an internal ID. A profile deleted and registered again is a different one, and the host has to pick it again |
| Alias | Required. Another host may have the same alias; the session's name then gets the profile's name (or, without one, the account ID) in parentheses |
| Instance ID | Required |
| Region | Optional. How it is chosen: [Region](#region) |
| run-as document | Optional. When set, it is passed to `aws ssm start-session --document-name`. When empty, Session Manager's default shell |
| Terminal color | "Auto" by default, which differs per host. The color is set when a session is created, so a change applies to the sessions you open from then on |

- An edited host applies to the sessions you open from then on. Sessions already open keep their host.
- A profile that a host uses cannot be deleted, and the screen names the host. A profile that other profiles assume a
  role from cannot be deleted or changed to the assume-role kind either.
- When the profile a host used is gone, its row says "Deleted profile". Edit the host and pick one again.

## Region

The region a session connects to is the first of these that is set:

1. The host's region
2. The profile's default region
3. For a profile that assumes a role from another profile, the source profile's default region
4. The SSO region of the SSO profile (for a role profile, of its source)

Profiles written to `~/.aws/config` resolve their region in the same order, so `aws`, an SDK and an SSM session reach
the same region.

## Connecting

- The terminal first checks the login with `aws sts get-caller-identity` and, when you are not logged in, starts a
  device-code login. The code appears on the "SSM login" screen. For a profile that assumes a role, the login is the
  source profile's.
- Right before it connects, it prints one line:
  `[Agent Fleet] AWS profile <name> (identity from Settings, not confirmed) · account … · role … · region … · target …`.
  This is the identity Settings names, not one AWS has confirmed. Parts without a value are left out. A role profile
  adds `via <source profile>`, and a shared sign-in adds `sign-in <name>`.
- **Force re-login** (for a stopped session, **Re-login and resume**) discards the profile's login and then logs in.
- **Cancel** on the "SSM login" screen moves a session you just created to the trash. A stopped session you were
  reopening stays, stopped.
- **Resume** on a stopped SSM session checks the login again from the start.

## Search AWS

**Search AWS** on the SSM screen of Start runs `aws ssm describe-instance-information` with the chosen profile.

- Only EC2 instances (`i-…`) that are online now are listed. On-premises managed nodes (`mi-…`) are not.
- The name is the instance's EC2 Name tag. When the role lacks `ec2:DescribeInstances`, instances are listed without
  a name.
- Without `ssm:DescribeInstanceInformation` the screen says "You don't have permission to search AWS instances".
- With two or more profiles you choose the one to search with; with one, that profile is used. A host you
  **Register** connects with that profile too.
- The search uses the login already saved. It fails when the login has expired: connect to a registered host once to
  log in.
- The search gives up after 25 seconds. Any AWS error other than a permission error shows as "AWS search failed".
- A host made with **Register** takes the Name tag as its alias, or the computer name, or else the instance ID. Its
  region is empty (the profile's default), and so is its run-as document.
- Agent Fleet does not keep the search results.

## ssh config import

**Import from ~/.ssh/config** (next to the profile import) reads your ssh config and creates SSM hosts.

**The way you connect changes.** In your ssh config these entries open an SSH session (your user, key and port)
through an SSM tunnel, with `ProxyCommand aws ssm start-session … --document-name AWS-StartSSHSession`. An Agent Fleet
host opens an `aws ssm start-session` shell on the instance, with no SSH user, key or port. Those settings (`User`,
`IdentityFile`, `Port`, forwards …) are not carried over, and the preview lists their names.

The file is read in your browser only, and **a `ProxyCommand` is never run, shown, sent or kept.** Only the alias,
instance ID, AWS profile and region of the rows you tick are saved, through the same save as adding a host.

### The form it reads

Only one static form is read:

- A `Host` that is **a single concrete name**
- A `ProxyCommand` that is `aws ssm start-session` (optionally wrapped as `sh -c "…"`)
- Only these options, each at most once:
  - `--target`: an instance ID (`i-…` / `mi-…`), or `%h`. `%h` is the entry's `HostName`, or the `Host` name itself
    when there is no `HostName`, and it must be an instance ID
  - `--document-name AWS-StartSSHSession`
  - `--parameters portNumber=%p` (or `portNumber=22`)
  - `--profile`
  - `--region`

Everything else cannot be imported, with a reason: other documents (port forwarding, custom), other options, other `%`
tokens, shell syntax (`;`, `|`, `$(…)`, quoting beyond the whole word), `ProxyJump`, `CanonicalizeHostname`, wildcards
(`*`, `?`), negations (`!`), `Match` and `Include`.

### Overlapping blocks

OpenSSH uses the **first** value it finds for each keyword across every block that applies to a name, so the blocks
are not independent. A host can be imported only if its `ProxyCommand` (and its `HostName`, when `%h` needs it) is the
first value and comes from **a block that names the host exactly**. A value inherited from `Host *` or an earlier
pattern, or one that follows a `Match` that sets such a keyword or any `Include` (its text is not in what you pasted),
is refused. To import such a host, paste the included files' text above it and write its entry out in full.

### Profile and alias

- The **AWS profile** is the Settings profile whose AWS name equals the command's `--profile`. When there is no
  `--profile`, or none matches, choose a profile in the row.
- An alias may use letters, digits and `. _ @ -`, up to 64 characters.
- Rows that already exist (same alias and instance) show as **Already registered**. An alias already used for
  another instance is refused.
- A file can hold at most 500 entries.
- Text that looks like an access key or token is warned about and not used.
- Nothing is created until you press Import, and a failed read of the current hosts sends nothing.
