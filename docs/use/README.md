# Using Agent Fleet

English | [日本語](README.ja.md)

Audience: anyone who runs agents from the Console
Source of truth: the Console itself — if a screen disagrees with this shelf, the screen is right
Updated: 2026-08

This shelf answers **"how do I do this?"** for the person doing the work: starting
sessions, following and steering an agent, working with repositories and files,
connecting the agent you want to use, and getting unstuck.

## What belongs here

- Step-by-step procedures, in the order the reader will actually do them.
- What a screen, badge or menu means.
- What to try when something looks wrong, and how to tell the difference between
  "still working" and "stuck".

## What does not

- **Capability facts.** Which agent supports plan mode, which provider supports pull
  requests, what a role may do — those live in [ref/](../ref/README.md) so there is
  only one copy. Link to the table.
- **Anything a reader cannot see on screen.** No environment variable names, no
  internal identifiers, no API paths, no source paths. The words in the Console are
  the correct words; see [CONVENTIONS](../CONVENTIONS.md).
- **Administration.** Anything that affects other people belongs in
  [admin/](../admin/README.md); anything about installing or keeping a deployment
  alive belongs in [operate/](../operate/README.md).

## Update trigger

A change to a screen, a flow, or a default that the reader would notice. If a feature
ships and this shelf says nothing about it, the feature is not done
([CONVENTIONS §8](../CONVENTIONS.md)).

## Migration in progress

Not written yet. Until it is, the user guide is `../guide/member/` and it remains the
source of truth. This shelf is written in phase P2 of the documentation rebuild, and
`../guide/` is deleted at that point.
