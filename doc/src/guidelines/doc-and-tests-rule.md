# The Doc-and-Tests Rule

**Every functional change must be explicitly checked against — and where appropriate update — the corresponding documentation, and must consider whether tests need to be added or extended.**

## Why

Two failure modes the project has seen repeatedly, both expensive to clean up:

- **Doc drift.** Code evolves; the page describing it does not. A reader trusting the page is led wrong, and the next person who edits the page has to reverse-engineer current behavior before they can write a correct sentence. The cost compounds — every page that lies makes the next reader trust the docs less, which means future changes fix less of the prose, which means more lies.
- **Behavioral changes without test coverage.** A fix lands, the e2e build still passes, no unit test pins the new behavior. Six weeks later a refactor silently regresses it; the original bug returns; nobody notices until the symptom resurfaces in the field.

The rule exists because both of these are easy to skip under time pressure and easy to catch if you build the check into the change.

## How to apply

When you finish a code change, before opening the PR (or before declaring the task done):

1. **Identify the doc impact.** Which pages in `doc/src/` describe the behavior that changed? Read them — *the actual current text*, not your mental model of them. If they describe the old behavior, update them. If they don't yet describe the area at all, decide whether the change is significant enough to need a paragraph or page.
2. **Identify the test impact.** Is the change covered by an existing test? If not, can a unit test be added cheaply? If only the e2e covers it, can a focused unit test be lifted out of the e2e path? "Covered by e2e" alone is not enough for things that can fail silently — anything affecting artifact identity, dependency resolution, container layering, or env propagation needs a unit test that pins the behavior.
3. **Make a deliberate decision** about both. The rule is to *check*, not to always edit — sometimes the right call is "no doc change needed" or "the existing test is sufficient". The harm comes from skipping the check, not from concluding "no change".

If you change behavior without checking docs and tests, you are accumulating debt the next person has to pay.

## What "functional change" means

Anything that changes what the system *does*: a bug fix, a new feature, a refactor that alters observable output, a CLI flag, an artifact identity rule, a config-file schema. Pure internal restructuring with no observable change rarely needs doc edits, but often needs a test confirming behavior was preserved.
