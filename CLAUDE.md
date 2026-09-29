# CLAUDE.md — gl-ng AI Operational Guide

This file is for Claude (and other AI agents) working on `gl-ng`. It is *not* a
substitute for the project documentation. The mdBook at `doc/` is the source
of truth for what the system is, how to operate it, and how it is built. Read
that first.

---

## 1. Where to learn the system

Before making any non-trivial change, orient yourself in the docs:

- **Concepts** (`doc/src/concepts/`) — what gl-ng is, the artifact model,
  identity hashing, object store, lockfiles, rootfs assembly, the pipeline.
- **User Guide** (`doc/src/guide/`) — how to actually use the CLI, write
  `build.yml` / `sources.yml`, run builds.
- **Maintainer / Internals** (`doc/src/internals/`) — code-level walk-through
  of every `internal/<pkg>` and `cmd/`. Roughly equivalent to reading the
  source, with cross-references.
- **Architecture Guidelines** (`doc/src/guidelines/`) — living principles that
  govern code, tests, and prose. Most likely to be relevant to whatever
  change you are about to make.

If the docs and the code disagree, the code is authoritative — and you should
update the docs as part of your change. See "The Doc-and-Tests Rule" below.

---

## 2. The non-negotiable rule

**Every functional change must explicitly check the corresponding documentation
and tests.** This is the one guideline most likely to be skipped under time
pressure, and the one whose absence does the most damage. The full statement
lives in `doc/src/guidelines/doc-and-tests-rule.md`. The short form:

1. Identify which pages in `doc/src/` describe the behavior you are changing.
   Read the *current* text. Update it if it is now wrong, or note explicitly
   that no doc change is needed.
2. Identify whether the change is covered by tests. If it can fail silently
   (artifact identity, dependency resolution, container layering, env
   propagation), a unit test that pins the new behavior is mandatory.
3. Make a deliberate decision about both, even when the answer is "no change
   needed". The harm comes from skipping the check.

If you find yourself thinking "I'll come back to the docs later", you won't.
Do it now.

---

## 3. Project layout in one paragraph

The Go module is at `gl-ng/`, with `cmd/` for binaries and `internal/` for
packages. `make build` produces `bin/gl`, `bin/exec_env_stub`, and the demo
binaries. Tests use the pre-built stub via `GL_EXEC_ENV_STUB`; never run
`go build` from inside a test or shell script. The staging git repo (the
package-source store) lives separately at `../staging/`.
The mdBook source lives at `doc/src/`; build output goes to `doc/book/` and
is gitignored. The Makefile is the single orchestration entry point — read
it before reaching for any other build command.

---

## 4. Operational guidance for AI agents

### Memory hygiene

You have a persistent memory system. Use it for:

- **User preferences and feedback** — corrections you've received,
  collaboration style, recurring guidance.
- **Project state that is not derivable from the code** — current phase,
  outstanding decisions, who is working on what, why a particular approach
  was chosen.
- **References** — pointers to external systems (Linear, Slack, Grafana
  dashboards) that hold information you might need later.

Do *not* save:

- Code patterns, file paths, conventions — read the current code.
- Recent changes — `git log` is authoritative.
- Anything already documented in `doc/` or in this file.
- Ephemeral task state — that belongs in TaskCreate / plans, not memory.

When recalling memory, verify it is still current. Memory records become stale.

### Before destructive operations, ask

This includes: deleting files, dropping branches, force-pushing, `rm -rf`
of anything you didn't just create, modifying shared infrastructure, posting
to external services. The cost of pausing is low; the cost of an unwanted
destructive action is high. User authorization for one destructive op does
not extend to others.

### Investigating before fixing

When you encounter unexpected state — unfamiliar files, branches, locks — do
not delete or overwrite as a shortcut. Investigate first; it may be the
user's in-progress work. Resolve the root cause; don't bypass safety checks
(`--no-verify`, `--no-gpg-sign`) unless the user explicitly authorizes it.

### Picking up work

If you arrive in the middle of a task, look for:

1. Active task lists (TaskList).
2. The most recent commit messages for context.
3. Any plan files in `.claude/plans/`.
4. Memory entries about the current project state.

If genuinely unclear, ask the user before guessing.

### Exploration vs. implementation

For broad codebase exploration that takes more than a few queries, spawn the
Explore agent. For implementation that has multiple valid approaches, prefer
EnterPlanMode and reach alignment before writing code. Don't narrate internal
deliberation in the user-facing channel — show updates at meaningful moments
(found something, changed direction, hit a blocker) and keep them brief.

### Editing this file

You are invited to edit `CLAUDE.md` when you learn something durable about
how to work on this project. Before adding anything, ask: **does this belong
in the mdBook instead?** If it is a principle that human maintainers also
need to know, the answer is yes — put it in `doc/src/guidelines/` and link
to it from here if appropriate. Only AI-specific operational guidance lives
in this file.

---

## 5. Anti-patterns specific to LLM behavior

- **Don't fabricate file paths or function names from training data.** Always
  verify with a search before referencing them.
- **Don't add scaffolding or "future-proofing" the user didn't ask for.** A
  bug fix is a bug fix. A one-shot script doesn't need a config file.
- **Don't write multi-paragraph commit messages or PR descriptions when one
  sentence will do.** Bullet points beat prose for changelogs.
- **Don't claim a UI/feature works without exercising it.** If you can't run
  it, say so. Type-checking and unit tests verify code correctness, not
  feature correctness.
- **Don't bypass build/test failures.** Fix the underlying issue. Skipping
  hooks, force-pushing, or `--amend`-ing past a failed pre-commit is almost
  always wrong.
- **Don't restate the diff at the end of every response.** The user can read
  the diff. Summaries should mention what changed *and* what's next, in
  one or two sentences total.
