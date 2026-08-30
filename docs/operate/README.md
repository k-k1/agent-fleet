# Operating a deployment

English | [日本語](README.ja.md)

Audience: whoever installs, upgrades and keeps one deployment alive — with a shell on the host or an AWS account
Source of truth: the scripts under `deploy/` — a procedure here that contradicts the script it describes is a bug in this shelf
Updated: 2026-08

This shelf answers **"how do I stand it up and keep it alive?"**: choosing a
deployment target, installing it, backing it up, upgrading it, securing it,
diagnosing it, and tearing it down.

## What belongs here

- Runbooks: the actual commands, in order, with what to check after each one.
- Key generation, backup and restore, upgrade and rollback, incident response.
- Ingress, TLS, sign-in providers, egress control, and where each secret lives.
- Capacity: sizing, idle-stop, and what a deployment costs to run.

## What does not

- **Capability facts** — which features exist on which deployment target. That is
  [ref/deploy-targets.md](../ref/deploy-targets.md).
- **Why the architecture is the way it is** — [build/](../build/README.md) for how it
  works, [decisions/](../decisions/) for why.
- **Tenant-level administration** — [admin/](../admin/README.md). The distinction is
  the shell: if the reader needs one, it belongs here.

## Update trigger

A change to a deployment target, an installation or upgrade step, a variable that
must be set, or a failure mode worth recognising.

## Migration in progress

Not written yet, and this shelf has the largest gap to close. Today the real
procedures live in `deploy/*/README.md` — roughly 2,100 lines, with
`deploy/aws/ecs/README.md` alone at over a thousand — while `../guide/operator/`
holds a Japanese commentary that points at them. That split means the procedures are
outside `docs/`, so they are **never shipped to the operator's container**, which is
the one place they would be read under pressure.

Phase P3 of the documentation rebuild moves the runbooks here and reduces
`deploy/*/README.md` to pointers. Until then, `deploy/*/README.md` is the source of
truth and `../guide/operator/` is its commentary.
