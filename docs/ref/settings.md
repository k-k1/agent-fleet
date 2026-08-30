# Settings — where things are configured

English | [日本語](settings.ja.md)

Audience: everyone, but written for whoever is looking for a knob and cannot find it
Source of truth: the Console for user and tenant settings; `deploy/compose/.env.example` for deployment variables
Updated: 2026-08

Three layers set behaviour, and confusing them is the usual reason a change appears to
have no effect. From narrowest to widest:

| Layer | Set by | Takes effect |
|---|---|---|
| Personal settings | the member, in their own settings | |
| Tenant settings | the tenant administrator | |
| Deployment variables | whoever operates the deployment, before start | |

A personal setting cannot widen a tenant limit, and a tenant setting cannot widen what
the deployment allows.

## Personal settings

| Tab | Configures | Details |
|---|---|---|

## Tenant settings

| Tab | Configures | Details |
|---|---|---|

## Deployment variables

| Variable | Sets | Default |
|---|---|---|

## Status

Axis fixed, cells to be filled in phase P1. The tab rows come from the Console's own
tab labels so this table cannot silently miss a screen; the variable rows come from
the annotated example environment file, which stays the source of truth for defaults.
