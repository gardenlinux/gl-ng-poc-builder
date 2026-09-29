# Architecture Guidelines

This section is the living set of principles that govern code, tests, and prose in `gl-ng`. It is meant for *both* human maintainers and AI agents working on the codebase.

## How to read this section

Each page captures one coherent principle. Pages are written to be read stand-alone — landing on a page from search or a cross-link should be enough to understand the principle, why it exists, and how to apply it without first reading the rest of the section.

There are no numbered ADRs, no append-only history of "guideline 0017 supersedes guideline 0009". When a principle changes, the relevant page is *edited in place*. Git history is the audit trail.

## What belongs here, what doesn't

In: principles that any maintainer (or agent) needs to know to make good decisions about code, tests, or design — and that are *not* obvious from reading the code itself.

Not in:
- Specific bug post-mortems. A war story becomes a guideline only when it yields a generally-applicable principle.
- Things already explained well in [Concepts](../concepts/overview.md) or the [Internals](../internals/overview.md) chapter — link to them instead.
- AI-meta instructions ("ask before destructive ops", memory-system rules) — those live in `CLAUDE.md` and are not relevant to human contributors.

## The non-negotiable rule

Before anything else: every functional change must be checked against the corresponding documentation, and must consider whether tests need to be added or extended. This is the single guideline most likely to be skipped under time pressure, and it is the one whose absence does the most damage. Read it on its own page: [The Doc-and-Tests Rule](./doc-and-tests-rule.md).

## Index

- [The Doc-and-Tests Rule](./doc-and-tests-rule.md) — every change checks docs + tests.
- [Artifact Identity and Caching](./identity-and-caching.md) — the cache is the soul of the system; identity is sacred.
- [Dependency Locality](./dependency-locality.md) — the build chroot is a self-consistent universe; sibling cross-pins between local and lockfile entries must be wired explicitly, the engine does not auto-overlay siblings.
- [ExecEnv Discipline](./execenv-discipline.md) — all process execution flows through the ExecEnv interface.
- [Environment Propagation](./env-propagation.md) — `ExecRequest.Env` is overlay, not replace; reset explicitly when entering a fresh universe.
- [Branch and Staging Model](./branch-and-staging-model.md) — the empty `init` ancestor; merge, never rebase across branches.
