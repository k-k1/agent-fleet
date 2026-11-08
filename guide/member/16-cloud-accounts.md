---
audience: "anyone using AWS or Google Cloud from the workspace"
---

# 16. Cloud accounts — AWS profiles, SSM hosts and Google Cloud

English | [日本語](16-cloud-accounts.ja.md)

Logging in to an EC2 host over SSM is in [Log in to EC2 with SSM](../user/integrations/aws-ssm.md), and its detailed rules
are in [SSM reference](../ref/aws-ssm.md). The terminal, SDKs and build tools, and `af-aws-exec`: see
[af-aws-exec](../ref/cli/af-aws-exec.md).

## Running commands in Google Cloud as you (af-gcloud-exec)

Your Google Cloud profiles live in **⚙ Settings → the "Google Cloud" tab**. A profile says which project a
command points at and as whom it acts:

- **Label**: the display name. The profile's **name**, the one commands use, is made from it and shown next to
  it: lowercase letters, digits and `-` (every other run of characters becomes `-`, a name that would start with
  a digit gets `p` in front, and a label with nothing usable in it, such as a Japanese one, gets `p-` and a short
  code). If two labels make the same name (`Prod` and `prod`), **neither is available** in the workspace and the
  row says so; rename one.
- **Login method**: a Google account, including Google Workspace and Cloud Identity accounts.
- **Project**: the project ID (not the number) commands point at by default.
- **Quota project** (optional): the project billed for API quota; the project itself when left blank. Some
  APIs need one with a personal login, and your account needs permission to use services on it.
- **Account** (optional): the Google account to sign in as; a sign-in as anyone else is refused. Left blank,
  you choose at the first login.
- **Impersonate service account** (optional): commands act as this service account through your login. Your
  account needs the Service Account Token Creator role on it. This is the way to act as a service account:
  service-account keys are not accepted anywhere.
- **Region / Zone** (optional).

**No Google credentials are stored in Agent Fleet.** The login lives inside your workspace, in a gcloud store
of the workspace's own. It is separate from the `gcloud` you run in a terminal: logging in to one does not log
in to the other, and a profile never reads or changes your own gcloud configuration, logins or application
default credentials. A change in Settings reaches the workspace within about five minutes, or at once when you
run `af-gcloud-exec`. Changing a profile's account, changing its label so that its name changes, or deleting
and adding it again, resets which account it uses: unless the account it names is already logged in in the workspace, the next run asks for a
login. Export and import carry the profiles (see [20 Settings](20-settings.md#export-import)).

### Importing profiles from gcloud configurations

On the Google Cloud tab, **Import from gcloud configurations** reads the configuration files of your own machine
(`~/.config/gcloud/configurations/config_<name>`; choose several at once, or paste one and give it a name). The
files are read in the page and never sent; only the fields of the profiles you tick are saved. The preview lists
every file as **Create**, **Already registered** or **Not importable** with the reason, and the label (the
configuration name by default) can be changed before importing.

- **What carries over**: `core/project`, `core/account`, `billing/quota_project`, `compute/region`,
  `compute/zone` and a single `auth/impersonate_service_account`. The same checks as the add form apply.
- **What is refused, with a reason**: a configuration with no project; a service account as `core/account`
  (it cannot sign in without a key: sign in with a Google account and impersonate the service account); a list of
  service accounts (a delegation chain); credential file or access token file settings, a custom OAuth client or
  any other setting under `auth/`; proxy and API endpoint settings; a universe domain or custom CA file. Creating
  such a profile without them would point it at something your configuration does not.
- **What is never read or shown**: your gcloud credentials, application default credentials and key files, and any
  path or token file a configuration names. A reason names the setting and the line, not its value.
- A configuration whose label already exists with the same settings shows **Already registered**; with different
  settings it asks for another label. If the current profiles cannot be read, nothing is created.
- Importing signs nothing in: use **Log in** on each new profile.

### Running a command as a profile

```sh
af-gcloud-exec --list
af-gcloud-exec --profile <name> --project <project-id> -- gcloud compute instances list
af-gcloud-exec --profile <name> --project <project-id> -- terraform plan
af-gcloud-exec --profile <name> --project <project-id> -- kubectl get pods
```

- `--list` prints each profile's name, project, account ("chosen at the first login" until then), the service
  account it impersonates and its label, and names the profiles that are not available, with the reason. Choose
  by project and account, not by the name alone.
- `--project` must be the profile's project, or the command is refused. A Google login is not bound to a
  project, so this only checks that you and the profile agree on where the command points by default: a
  command's own `--project`, or a project written in a Terraform configuration, still wins.
- The command gets a short-lived **access token** of the profile and nothing else of yours: no gcloud login, no
  application default credentials, and nothing from the machine's own Google identity. `af-gcloud-exec` prints
  "profile … runs as <account> in project …; the token is valid for N more minutes" (`-q` leaves it out); the
  token itself is never printed.
- The token lasts what remained when it was handed over: at least ten minutes, at most about an hour. While the
  command runs, `af-gcloud-exec` stays running beside it and **renews the token file** about eight minutes before the
  token ends. Every `gcloud`, `bq` or GKE auth plugin process the command starts after that reads the new token, so
  a script that calls them again keeps working. A process that holds the token it read at its start keeps that one:
  a single long-running `gcloud` (SDK 587.0.0 reads the file once), and a program that reads
  `GOOGLE_OAUTH_ACCESS_TOKEN` once, such as **a long `terraform apply`**, fail when that token ends. A `kubectl`
  watch or an open connection depends on the plugin being run again and the connection being made again (not
  measured). Split such work into shorter runs.
- If the token cannot be renewed (the login was revoked or has to be done again, a permission, the network),
  `af-gcloud-exec` says so, keeps trying until the token ends, and then stops the command (SIGTERM, and SIGKILL
  after 30 seconds if it does not end). The exit status is 3 when you have to log in again and 1 otherwise. If the
  Console could be asked when the run started, a login request appears there as it does at the start; with
  `--login`, `--no-login` or outside a workspace the message gives the command to run in your terminal, and a request
  cancelled in the Console is reported as cancelled.
- Stopping `af-gcloud-exec` sends SIGTERM to the command it started (the command may ignore it), and the same happens
  when `af-gcloud-exec` is killed. Programs the command itself started are not reached. When `af-gcloud-exec` is
  killed, the token file stays until its token has expired and a later run removes it.
- Every run leaves a line in the session's conversation, as `af-aws-exec`'s do: the profile, the project, the
  account (and the service account it impersonates), the program's name and the command's exit status, or why it was
  refused. Never the arguments. Settings > Google Cloud lists the latest runs of every session under "Recent runs".
- The first run installs the Google Cloud SDK (one pinned version, with the GKE auth plugin) into your home: about
  85 MB to download and about 510 MB on disk, kept across stops and a Recreate. The first run of each gcloud
  command after that is a few seconds slower once. The **Toolchain** tab's table of tool versions then shows
  gcloud. A plain `gcloud` in a terminal is that same program, but with your own login, if any, not a profile's.
- For `kubectl`, fetch the cluster's entry once through the profile
  (`af-gcloud-exec --profile <name> --project <project-id> -- gcloud container clusters get-credentials <cluster> --location <location>`),
  then run `kubectl` through `af-gcloud-exec` too: the GKE auth plugin asks gcloud for the token, and only inside
  `af-gcloud-exec` does gcloud have one.

**Which tools use the token.**

| Tool | With `af-gcloud-exec` |
|---|---|
| `gcloud`, including `gcloud storage` | yes |
| `kubectl` against a GKE cluster | yes, through the GKE auth plugin |
| Terraform's Google provider | yes, with the quota project; the token is not renewed during a run |
| `bq` | yes. It comes with the SDK but is not on the path: run `~/.local/share/agent-fleet/google-cloud-sdk/bin/bq` |
| `gsutil` | **no**. It ignores the token: without a gsutil (boto) configuration of your own it sends its requests without any login, so a public bucket answers; with one, it can act as whatever identity that configuration holds. Use `gcloud storage` |
| Google's client libraries (Go, Python, Node, …) | only when the program hands them the token |

A program built on a client library stops with an error such as "File … was not found" or "no such file or
directory" for its default credentials. That is deliberate: it would otherwise find another identity (your own
gcloud login, or the machine's). The program has to take the token itself: in Python
`google.oauth2.credentials.Credentials(os.environ["GOOGLE_OAUTH_ACCESS_TOKEN"])`, in Go
`option.WithTokenSource(oauth2.StaticTokenSource(&oauth2.Token{AccessToken: os.Getenv("GOOGLE_OAUTH_ACCESS_TOKEN")}))`
(Go's libraries then use the profile's quota project, which `af-gcloud-exec` sets in `GOOGLE_CLOUD_QUOTA_PROJECT`;
in Python pass `quota_project_id=os.environ["GOOGLE_BILLING_PROJECT"]` to `Credentials`). In Node, create the `OAuth2Client` from the same version of `google-auth-library` the client library uses; one
from another major version is accepted without an error and sends no login at all. Agents ask you before changing
your code that way.

### Logging in

**In the Console.** Each profile row in **⚙ Settings → Google Cloud** has **Log in**; once the workspace holds a
login for the profile, the row says **"Logged in as <account>"** and the button becomes **Log in again**. The WS
bar's Google Cloud badge has the same buttons ([below](#the-google-cloud-badge-in-the-ws-bar)). The button opens a
login window that shows the profile, its project and its account and starts nothing yet: the sign-in starts only
when you press **Log in** (or **Log in again**) in that window, and only that window then shows the sign-in.

1. Press **Open Google sign-in**. Google's sign-in page opens in a new tab (the window also shows its address,
   which is always on `accounts.google.com`: the workspace refuses to show any other). Sign in as the profile's
   account, if it names one; a sign-in as anyone else is refused.
2. Google shows a **verification code**. Paste it into **Verification code** in the same login window and press
   **Submit code**. The window says "Logged in" once the workspace has checked that the login gives a token.

**Paste a code only into a login you started yourself, here, just now.** A code works only for the sign-in whose
page produced it, and the Console shows the code field only in the window where you pressed **Log in**, but
nothing proves who started a sign-in: any program in your workspace, an agent included, can start one. So never
paste a code into a field, a terminal or a chat message someone else put in front of you.

If the workspace already holds a usable login for the account, **Log in** finishes at once without a sign-in page.
**Log in again** always signs you in afresh instead of reusing what is stored; use it when you know the login was
revoked (the workspace cannot see that until a command is refused). If you press **Log in** again in another
window or device, the first sign-in stops and its code no longer works. A sign-in that is not finished within
15 minutes ends; **Start again** begins a new one.

**Log out** on a logged-in row (in Settings or in the badge) deletes the login the workspace stored for the
profile's Google account. The workspace keeps one login per account, so every profile using that account is
logged out with it; the confirmation names them before anything is deleted. Your sign-in at Google, and gcloud on
your other machines, are not touched, and a token a command already received stays valid until it expires (at most
an hour). A sign-in waiting for its code in one of those profiles' login windows stops. To use the profile again,
log in again. If the profile's account changed between the confirmation and the logout (another login finished
meanwhile), nothing is logged out and the Console says so.

**When an agent's command needs the login**, it asks you in the Console: `af-gcloud-exec` prints "Google Cloud
login for profile … requested in the Agent Fleet Console", and a toast at the bottom of the screen says **"An agent
is waiting for a Google Cloud login"**, with the profile, its project and account, which session and command ask,
and the same countdown until the request is withdrawn. Press
**Log in** on the toast to open the login window and continue as above. When Google refused the stored login, the
window says so, and the sign-in starts afresh. The agent's command waits about a minute and a half (a few seconds
when it is not run by an agent session, such as a script in a shell session) and continues once you are logged in;
if it has given up by then, it exits with code 3 saying the login is waiting in the Console, and the agent runs it
again after you tell it you are done. **Close** keeps the request: it stays in the Console on your other devices
too. **Cancel the request** is for a login you do not want: it withdraws the request, the command exits with
code 3, and for about a minute that profile is not asked for again. Closing the toast only hides it in that tab.
**Log in** on the profile's row in Settings, or in the badge, works at any time; a sign-in there settles the
request too.

**At a terminal** in a workspace, `af-gcloud-exec` asks the Console like an agent's command, and waits up to ten
minutes for you to finish the login there; Ctrl-C stops waiting with exit code 3 and leaves the request in the
Console. With `--login` it starts the Google sign-in in that terminal itself (and does so outside a workspace, or when
the Console cannot be asked): it prints a URL, you open it in your browser, sign in, and paste the verification code
the page shows back into **that** terminal. The same rule holds: paste only a code from a sign-in you started yourself
just now. A command that
cannot ask the Console (with `--no-login`, or outside a workspace) exits with code 3 and prints the command to run
in a terminal of your own (a shell session, or in Claude Code type it after `!` at the prompt):

```sh
af-gcloud-exec --profile <name> --project <project-id> --login -- true
```

**On a Compute Engine VM.** On a workspace that runs directly on a Google Cloud VM (the `native` runtime), gcloud
first asks whether to use a personal account on that VM. The Console login does not answer that question for you:
the login window ends saying so, and you log in with the terminal command above instead.

**When a login ends.** Google can refuse a stored login: it was revoked, or your organisation requires you to
sign in again after a set time (session length). The next run that needs a fresh token then asks for a login as
above, and that login really signs you in again instead of reusing what was stored. The workspace **cannot warn
you before** such an end: the time is not recorded anywhere it can read. Until the token already handed out has
less than ten minutes left, runs keep working, so the request for a login can come up to about 50 minutes after
the end.

### The Google Cloud badge in the WS bar

While the workspace is running and you have at least one Google Cloud profile, the WS bar shows a badge with the
Google mark beside the AWS one. It names your logged-in profile when there is exactly one, and otherwise counts
them ("1/3", one of three logged in). It is green when something is logged in, plain when nothing is, and amber
while an agent's command is waiting for a profile's login. Press it for the list: each profile with **Logged in**
or **Not logged in**, its name, project and account, **"An agent is waiting for this login"** on the row an agent
is waiting for, **Log in** / **Log in again**, which open the login window above, and **Log out** on a logged-in
row (see above). There is no default profile:
every logged-in profile can be used at the same time, and each command picks one with
`af-gcloud-exec --profile <name>`. **Google Cloud settings** at the bottom opens the Settings tab.

**Logged in** means only that a Google login for the profile's account is stored in the workspace. The workspace
does not check it with Google, and keeps it when Google refuses it, so a revoked login stays **Logged in**, even
after a command has found out. While that command's login request waits, the badge is amber; once the request is
cancelled or expires (after 15 minutes), the badge is green again although the login no longer works.
**Log in again** signs in afresh and fixes it. The badge does not poll: it asks the workspace when it comes up,
when you open the list, when a login request comes or goes, after a login, when you come back to the tab, and
after you change a profile in Settings. A profile you add or delete shows there once the workspace has picked up
the change, within about five minutes.

### When it stops

| What you see | What it means |
|---|---|
| exit code 3, "the login was requested in the Agent Fleet Console and is waiting …" | The profile has no login yet, or Google refused the stored one, and the command stopped waiting. Log in from the toast or Settings, then have the command run again. Exit 3 always means a login. |
| exit code 3, "the login request was cancelled in the Agent Fleet Console" | You cancelled the request. Log in from Settings if you do want it. |
| exit code 3, "Google Cloud login required … log in from a terminal with:" | The Console could not be asked (`--no-login`, or outside a workspace). Run the printed command in a terminal. |
| "is for project X, not Y; --project must be the profile's project" | The wrong profile for this project. Check `--list`. |
| "no Google Cloud profile …" or "not exported: …" | The name is not one of your available profiles: add or fix it in Settings. |
| "gcloud could not mint a token: …" | Not a login problem: no permission (also on the service account to impersonate), an API not enabled, or the network. The message says which. |
| "the token gcloud minted is valid for only …" | gcloud could not hand out a token with ten minutes left. Try again in a minute. |
| "this deployment does not export Google Cloud profiles" | Your deployment does not offer Google Cloud profiles. |
| "could not renew the token of profile … trying again until it ends in N minutes" | The token the command is using could not be renewed yet. The run keeps trying; if the token ends first, the command is stopped. |
| "the profile's login is now X, not Y, which the command started as; run it again" | Someone logged the profile in as another account during the run. Run the command again. |
| "the profile changed in Settings while this run started; run it again" | Run it again; check `--list` if the account or project now differs. |
| "waiting for another af-gcloud-exec or a profile sync …" | A login in a terminal, or another run, is using the workspace's gcloud store. It continues when that ends. |

**What it does not guarantee.** The command runs as you and can read your home, your own gcloud directory
included: `af-gcloud-exec` keeps Google's standard ways of finding credentials (gcloud's and the client
libraries') from finding anything but the token it hands over; it does not make your files unreadable, and a tool
that looks elsewhere, such as `gsutil` with its own configuration, is not covered. On a workspace that runs directly on a Google Cloud VM, a
program that ignores those standard ways could still reach the VM's own identity. The same holds for a pod on Kubernetes: the
workspace is started with `AWS_EC2_METADATA_DISABLED=true`, but that variable does not block the cluster's metadata server (the
node's service account, or Workload Identity on GKE), so a program that ignores the standard ways could still reach that
identity unless your administrator blocks the route in the cluster's network.
