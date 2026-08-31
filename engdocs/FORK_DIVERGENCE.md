# Fork divergence: Voxist/beads vs upstream

This fork tracks upstream `gastownhall/beads` (formerly `steveyegge/beads`) and
carries a set of local changes. This document records **what diverges, why, and
whether it is ever expected to converge**, so a resync does not have to
re-derive that judgement from `git log`.

Keep this file current when a resync lands or an upstream PR changes state.

## Remotes

| Remote | Repo | Role |
| --- | --- | --- |
| `origin` | `gastownhall/beads` | **upstream** |
| `bfork` | `Voxist/beads` | **this fork**; `main` tracks `bfork/main` |
| `cstar`, `maphew` | peer forks | reference only |

The naming is inverted from the usual convention: `origin` is *upstream*, not
the fork. A bare `git push` on an untracked branch targets upstream. Check
`git status -sb` before pushing.

## Permanent divergence — stays local

Upstream has declined the db-proxy work. It is **not** a resync backlog item and
should not be re-proposed; carry it indefinitely and merge upstream *around* it.

### db-proxy connection pooling

- `internal/storage/dbproxy/proxy/` — `pool.go`, `pooledconn.go`, `mysqlwire.go`,
  `endpoint.go`, plus `server.go` / `stats.go` changes and their tests.
- ~2,800 lines, 14 files.
- Rationale: multi-agent orchestrators (gascity/gc) fork `bd` per agent
  operation, so the in-process `*sql.DB` pool never survives the process and
  there is zero cross-invocation reuse. See
  [CONNECTION_POOLING.md](CONNECTION_POOLING.md) and
  [CONNECTION_POOLING_DEPLOYMENT.md](CONNECTION_POOLING_DEPLOYMENT.md).
- Upstream vehicle was `gastownhall/beads#4303` (authored by `cstar`), which
  remains a **held-back draft**. Voxist never had its own upstream PR for this.

### proxied-server deploy path

- `open_backend.go`, `internal/storage/storage.go` (`ErrStoreIdentityMismatch`),
  `internal/storage/uow/*_provider.go` routing, `bd init --proxied-server`,
  the central nil-store guard, and `storeForRawDoltSync` (direct-endpoint escape
  hatch for `dolt push` / `pull` / `commit` in proxied mode).
- Depends on the pooling work above; shares its fate.
- `gastownhall/beads#4313` (dolt-server.port clobber) is the one piece still
  open upstream — converted to draft, blocked behind #4303, and awaiting a repro
  recipe maintainers asked for twice. Branch:
  `fix/proxied-routed-store-portfile-clobber`.

### be-pen9 shared backend (unmerged, fork-only)

`archive/be-pen9-shared-backend` holds the shared-proxy consolidation
(`SharedProxyRootDir`, `BackendLocalSharedServer` dispatch, T-001..T-008) from
the closed PR Voxist#5. `BackendLocalSharedServer` partially landed on `main`;
`SharedProxyRootDir` did not. Kept as an archive, not as active work.

## Landed upstream — do not re-carry

These fork fixes are **in `origin/main`**, in reworked form. Our original
versions must be dropped at resync rather than merged alongside, or the tree
ends up with two fixes for one bug.

| Fork PR | Landed upstream as | Upstream commit |
| --- | --- | --- |
| Voxist#4449 bootstrap `sync.remote` guard | #5400 | `8501a27c3` |
| Voxist#4407 Dolt 1105 merge-conflict retry | #5335 | `a48e372c8` |
| Voxist#4355 MySQL 1213 / invalid-connection retry | #4462 | `55914fb08` |

All three needed a **replacement transport**: GitHub blocks base-repo
maintainers from pushing to an organization-owned fork branch, so maintainers
could not amend our PR in place and re-landed it themselves with authorship
preserved.

> **Contribution rule:** file upstream PRs from a *personal* fork, not from
> `Voxist/beads`. "Allow edits by maintainers" does not work on org-owned forks,
> and every org-fork PR so far has cost a manual re-land.
>
> **That personal fork does not exist yet.** `bourgois/beads` is a REDIRECT to
> `Voxist/beads` — the same repository, id `1261574242` — presumably left behind
> when the fork was transferred to the org. `gh repo view bourgois/beads`
> answers about `Voxist/beads`, and a remote pointing at it pushes to the org
> fork. Verify with:
>
> ```
> gh api repos/bourgois/beads --jq .full_name   # -> Voxist/beads
> ```
>
> So this rule is currently unactionable: someone must first create a real
> personal fork under their own account (`gh repo fork gastownhall/beads`).
> Until then, an upstream PR either comes from the org fork and costs a re-land,
> or waits.

## Fork-local, no upstream path yet

Not refused — simply never proposed. These are the real upstreaming backlog.

| Area | Where |
| --- | --- |
| heartbeat/lease no-op auto-commit skip (vp-on8s) | `internal/storage/embeddeddolt/` |
| `BD_NO_AUTO_MIGRATE` fleet-migration guard | `cmd/bd/` |
| `GC_AGENT` actor resolution for `--claim` idempotency | `cmd/bd/` |
| SEC-003: `/Users/Shared` as a safe `BEADS_DIR` boundary | `internal/beads/context.go` |

Ops-only, never upstreamable: `DEPLOY_BD` install guard, nix `vendorHash`
recomputation, `prepared-dml-grandfather.txt`.

## The wisps plane rule — RESOLVED, no longer a divergence

`va-k0e` / `vg-3kn` / `vg-8db`. Closed 2026-08-31. Kept here because the shape of
the answer is worth not re-deriving, and because the reasoning that made it look
unavoidable was wrong in an instructive way.

**The problem.** The write path routes on STORAGE CLASS, not type —
`internal/storage/dolt/issues.go`:

```go
useWispsTable := issue.Ephemeral || issue.NoHistory || issue.WispType != "" || s.IsInfraTypeCtx(ctx, issue.IssueType)
```

so a `no_history` task lives in the wisps TABLE while remaining ordinary durable
work. Upstream's read rule admits that table only for `IncludeEphemeral`,
`IncludeInfra`, or an infra type, so `bd count --type task` never saw it.

**What the fork used to do.** Change the default: any explicit type admits the
plane. That fought upstream's tested contract (`the default must stay
byte-identical`), so it meant editing `internal/workapi/{list,count}.go`, three
golden cases, and **both** `cmd/bd/count_*_test.go` twins on every resync — and
the twins silently drifted, because only the embedded one was updated when the
delta landed. It was also not upstreamable: carried against clean upstream it
fails upstream's own `TestEmbeddedCountIncludeInfra`.

**What it does now.** `bd count` gained an `--include-ephemeral` flag, mirroring
the one the fork already had on `bd list`. Upstream's default is untouched.

- `issueops.CountRequest.IncludeEphemeral` — the plane knob alone: exactly the
  first of `IncludeInfra`'s four bundled changes, with none of the other three.
- `internal/workapi/count.go` — one line: `} else if !in.IncludeEphemeral {`.
- `internal/workapi/list.go`, the golden corpus and BOTH count twins are
  byte-identical with upstream again.

`--include-infra` was not a usable substitute: upstream's own doc calls it "FOUR
changes at once and not one", and its template exclusion silently drops template
rows of the named type — one silent undercount traded for another.

**Why the old rationale did not survive contact.** Worth remembering, because
each of these read as solid at the time:

- The motivating consumer was gone. `va-k0e` existed so wisp-leak reconciliation
  could find in-flight wisps via `bd list --type=molecule`; that consumer now
  passes an explicit tier and unions two queries.
- No programmatic caller depended on it. The orchestrator's only `bd list` call
  site already passes `--include-infra`, and it never shells out to `bd count`.
- The "test behind it" was circular — this document argued the contract was
  settled because a test pinned it, where that test was the fork's own edit of
  upstream's test.
- The fleet's own default tier is `ephemeral = 0`. The real need was always the
  `no_history` rows, not the ephemeral ones.

**Behaviour change.** `bd list --type X` and `bd count --type X` are
durable-only again; add `--include-ephemeral` to reach the wisps tier. The
concrete loser is `bd list --type session` (city `session` beads are written
`no_history`, and `session` is a custom type, not an infra type);
`workflow`/`step`/`convergence` are in the same position. No doc, skill, runbook
or agent instruction in either repo promised otherwise — swept before the
change. `docs/CLI_REFERENCE.md` deliberately does NOT document the new flag yet:
the docs describe the pinned release (`docs/cli-docs.pin`), so it appears at the
next release bump.

**Remaining carry**, all additive and each proposed upstream separately: the
`--include-ephemeral` registrations on `bd list` and `bd count`, the
`CountRequest` field, and the one-line guard. Upstream already owns the concept —
`ListRequest.IncludeEphemeral` is documented, the HTTP API exposes
`include_ephemeral`, and the flag is registered on `bd ready` and
`bd linear sync` — it was simply never wired to `bd list` or `bd count`.

## Known resync conflict zones

Recurring conflicts, roughly in descending pain:

1. `internal/storage/uow/*`, `internal/storage/dolt/store.go`,
   `internal/storage/issueops/commit_pending.go` — our originals vs the
   upstream re-lands. **Auto-merges cleanly and wrongly**; review by hand.
2. `go.mod` / `go.sum` / `default.nix` — the fork adds `lumberjack` and
   `x/time` as direct deps, so `vendorHash` must be recomputed after every
   resync.
3. `cmd/bd/uow_factory.go`, `cmd/bd/main.go`, `Makefile`.

## Parked, deliberately not carried

Work that exists, is not on `main`, and should stay that way until something
changes. Recorded so it is neither rediscovered nor re-attempted by accident.

### `bd monitor-commit-rate` (vp-5u7i deliverable 2)

Branch `feat/monitor-commit-rate`; Voxist PR #33 closed 2026-08-31.

A watchdog that samples committed history for no-op-commit storms. Parked
because **every storm source it watches for is already closed**, and nothing
calls it:

- `internal/storage/embeddeddolt/store.go` skips the auto-commit when only
  operational columns changed — the vp-on8s lease/heartbeat case that motivated
  it;
- upstream's `DiscardNoopIssueUpdates` gates value-identical updates inside `bd`;
- the fleet's own direct-SQL writes (`gascity/internal/beads/bdstore.go`) are
  CAS-guarded, so they only match rows they actually change;
- zero references to the command anywhere in gascity — the CHANGELOG entry
  advertises a city-pack order that does not exist.

Landing it would mean carrying fork-local code, plus a `cmd/bd/main.go`
registration line to re-apply every resync, to detect a condition three separate
fixes already prevent.

The branch is worth keeping rather than deleting: its detection logic was
**fixed** before parking. It identifies a no-op by NULL-safe comparison of the
actual content columns, discovered from `information_schema`, instead of the
frozen `content_hash` — which is written only by the upsert path and never
recomputed on update, so the original signal classified every real edit as a
no-op. Anyone resuming starts from a correct signal.

Known defects still in that branch, from the PR review: the command is
registered in `main()` rather than `init()` (so in-process tests cannot see it);
the query scans `dolt_diff_issues` over all history rather than using
commit-scoped `dolt_diff(from, to, 'issues')`, which is O(history) at per-minute
cadence; `--dry-run` returns exit 0 without analysing; and the alert text still
names the content-hash signal that was removed.

**Deliverable 1 of vp-5u7i (the no-op-commit gate) is NOT parked — it is
upstream's now**, via `DiscardNoopIssueUpdates` plus the early `Changed: false`
return in `issueops/update.go`. Do not re-land the fork version; `main` carries
no `nochange.go` and should not gain one.

## Recurring resync chores

Neither is a tree change you can prepare in advance; both are done per cycle.

1. **Push the docs-pin tag to the fork.** `docs/cli-docs.pin` names the release
   tag the docs pipeline builds `bd` from, and CI resolves it against the fork.
   Git branch pushes do NOT carry tags, so the tag is absent from
   `Voxist/beads` and the pin fetch fails, taking down `Check doc flags
   freshness` and `PR Policy (wrapper timing)`. Fix: `git push bfork
   refs/tags/<pin>`. Hit on the 2026-08-07 resync (v1.1.0) and again on
   2026-08-31 (v1.2.2).
2. **Recompute `vendorHash`.** The fork's `go.sum` carries lumberjack, which
   upstream lacks, so upstream's hash never validates for us. Run
   `./scripts/update-nix-vendorhash.sh` (it falls back to a `nixos/nix` Docker
   image when nix is not installed).

## Local conventions

Worktrees live in `worktrees/` inside the repo. Upstream `.gitignore` only
covers `.worktrees/` (dotted), so `worktrees/` is excluded via
`.git/info/exclude` — deliberately *not* added to `.gitignore`, to avoid one
more tracked-file divergence.
