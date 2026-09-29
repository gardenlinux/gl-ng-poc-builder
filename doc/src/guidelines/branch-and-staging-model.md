# Branch and Staging Model

**The staging repo's branches all descend from a single empty `init` commit. Merges between package branches and `main` rely on this shared ancestor. Never rebase across these boundaries — merge, always.**

## Why

The staging repo holds two kinds of git history that need to interact:

- **Package branches** (`pkgs/<name>`) carry the linear import history of one Debian source package: each commit is one imported upstream version, tagged `pkgs/<name>/<version>`.
- **The `main` branch** carries the integration history: build specs (`build.yml`), local patches, merges of package-branch versions chosen for inclusion.

For `git merge` to do the right thing when bringing a package version into `main`, the two branches must share a common ancestor. Without one, git treats them as unrelated histories and the merge resolves as "delete everything from one side" — corrupting the integration branch on first contact.

Forking every branch from a single empty commit (`git commit --allow-empty -m "init"`, tagged `init`) is the cheapest way to give every future branch a non-trivial ancestor. The empty tree means the merge logic only has to reason about additions from each side, never deletions of files the other side never had.

The "no rebase across branch boundaries" rule preserves this property over time. If a package branch is rebased onto a different base, its commits get new parents and the shared `init` ancestor is replaced. The next merge into `main` then sees no common ancestor and reverts to the unrelated-histories failure mode.

## How to apply

When initializing a fresh staging repo:

```bash
git init
git commit --allow-empty -m "init"
git tag init
# main now exists at the init commit
```

When importing a new package:

```bash
git checkout -B pkgs/<name> init
# import source, commit, tag pkgs/<name>/<version>
```

When integrating an imported version into `main`:

```bash
git checkout main
git merge pkgs/<name>/<version>
# add build.yml, commit
```

**Things to avoid:**

- `git rebase` across the package-branch / `main` boundary — re-parents commits and severs the shared ancestor.
- `git checkout -B pkgs/<name>` from `main` instead of from `init` — couples the package branch to integration history, so the next package import polluting `main` would also pollute this branch.
- Squash-merging a package branch into `main` — the `pkgs/<name>/<version>` tags must remain reachable from `main`'s history so the build system can resolve a version reference back to its source commit.

## What this enables

- Multiple versions of the same package can coexist on the package branch without polluting `main`. Only the version `main` merges in is "live"; older tagged versions stay reachable for inspection or rollback.
- Local patches on `main` survive subsequent imports — merging a newer version of the package replays as a normal merge, with conflicts surfaced where the local patch and the new upstream actually overlap, rather than as a wholesale revert.
- The audit trail is intact: any file in a built rootfs traces back through artifact identity → source build → source commit → package branch tag → upstream import.

## See also

- [Source Management and the Staging Repo](../concepts/sources.md) — the conceptual model.
- [Importing Sources](../guide/import.md) — the user-facing workflow.
