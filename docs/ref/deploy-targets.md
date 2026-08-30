# Deployment targets — what exists where

English | [日本語](deploy-targets.ja.md)

Audience: everyone; decisive for [operate/](../operate/README.md)
Source of truth: this table; the rows are checked against the runtime profiles the Control Plane accepts
Updated: 2026-08

One core runs on every target; only the edge adapter changes. What differs is
therefore small and specific — and worth stating precisely, because "it works on my
deployment" is the most expensive kind of documentation error here.

| Target | Workspace runs as | Home persists on | Chosen when |
|---|---|---|---|
| docker | | | |
| native | | | |
| ecs | | | |
| ecs-ec2 | | | |

`docker` also answers to `local`, `ecs` to `aws`, and `native` to `wsl`; the Control
Plane rejects anything else at boot rather than quietly defaulting.

## Capability differences

| Capability | docker | native | ecs | ecs-ec2 |
|---|:--:|:--:|:--:|:--:|
| Multiple users, mutually invisible | | | | |
| Per-user CPU / memory limits | | | | |
| Per-user disk sizing | | | | |
| Idle auto-stop | | | | |
| Stop / start preserving home | | | | |
| Role-scoped docs in the container | | | | |
| Browser pane | | | | |
| Cost attribution per member | | | | |
| Backup / restore procedure | | | | |
| Upgrade procedure | | | | |

## Status

Axis fixed, cells to be filled in phase P1. `ecs` and `ecs-ec2` are deliberately
separate profiles rather than a flag — the reasoning is a decision record, and the
measurements behind the EC2 pool are in the frozen archive.
