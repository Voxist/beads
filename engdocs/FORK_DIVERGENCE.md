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
| `mine` | `bourgois/beads-fork` | **personal fork**; upstream PRs are filed from here |
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

> **Contribution rule:** file upstream PRs from **`bourgois/beads-fork`**, never
> from `Voxist/beads`. "Allow edits by maintainers" does not work on an
> org-owned fork, so a maintainer who wants to amend a PR cannot push to it —
> every org-fork PR we filed (#4355, #4407, #4449) cost them a manual
> replacement transport instead. Confirmed working on the personal fork:
> `maintainer_can_modify` is `true` on #6096/#6097/#6098.
>
> ```
> git remote add mine git@github.com:bourgois/beads-fork.git
> git push mine <branch>
> gh pr create --repo gastownhall/beads --head bourgois:<branch>
> ```
>
> **The fork is `beads-fork`, not `beads`, deliberately.** `bourgois/beads` is a
> REDIRECT to `Voxist/beads` — the same repository, id `1261574242` — left by the
> transfer to the org, and it resolves at the GIT level, not just the web UI:
>
> ```
> gh api repos/bourgois/beads --jq .full_name        # -> Voxist/beads
> git ls-remote https://github.com/bourgois/beads.git  # -> Voxist/beads' main
> ```
>
> Creating a repo at that path would DISABLE the redirect, silently retargeting
> anything still configured with the old URL — reads would return the wrong
> repository and a push would land in it, neither failing loudly. Nothing on the
> dev machine referenced it when this was checked, but other clones, CI configs
> and teammates could not be inspected, so the name was left alone. Do not
> reclaim it casually; if you ever want it, audit those first.

## Filed upstream, awaiting review

Opened 2026-08-31 from `bourgois/beads-fork`, all with `maintainer_can_modify`.
None is fork carry — each fixes an upstream bug the fork happened to hit.

| PR | What |
| --- | --- |
| [#6096](https://github.com/gastownhall/beads/pull/6096) | `update-nix-vendorhash.sh` writes a stray `default.nix''` on BSD sed — `SED_INPLACE="sed -i ''"` cannot carry an empty argument, so the quotes survive word-splitting as a literal backup suffix. The stray carries the placeholder `sha256-AAAA…` hash, so `git add -A` can commit an unvalidatable `vendorHash`. |
| [#6097](https://github.com/gastownhall/beads/pull/6097) | `TestProtocol_FieldsRoundTrip` fails wherever `TZ` differs from the system zone. `workspace.env()` is a whitelist and omits `TZ`, so the `bd` child falls back to `/etc/localtime` while the test process uses its own. Upstream CI misses it because its runners have `TZ` unset AND a UTC system zone. |
| [#6098](https://github.com/gastownhall/beads/pull/6098) | `bd list --wisp-type X` is UNSATISFIABLE — wisp-typed beads are routed to the wisps table on write, `issues.wisp_type` defaults to `''`, and the plane is skipped, so the predicate is evaluated only against rows that cannot carry it. The golden corpus was recording the bug. |

Two more candidates, both gated on the `--include-ephemeral` work landing here
first:

- **`--include-ephemeral` on `bd list` and `bd count`** — additive, defaults
  untouched. Landing it retires nearly all of the remaining carry in the wisps
  section above.
- **`bd count --type <infra type>` answers 0** while `bd list --type <infra
  type>` returns rows. Upstream's asymmetry; pinned by
  `TestCountAndListPlaneAgreement`'s `infra type diverges` case.

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

## The wisps plane rule — resolved into an additive flag

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
durable-only again; add `--include-ephemeral` to reach the wisps tier.

`bd count --type <INFRA TYPE>` — `agent`, `role`, `message`, or whatever the
workspace configures — now answers **0**, because infra beads are always written
to the wisps table and count no longer reads it for a named type. `bd list
--type agent` still returns rows, so the two disagree. That asymmetry is
UPSTREAM'S (its count arm is a bare `else { SkipWisps = true }`); the fork's old
wide default merely masked it. `--include-ephemeral` or `--include-infra`
recovers the count. Pinned by `TestCountAndListPlaneAgreement`'s
`infra type diverges` case, and worth fixing UPSTREAM rather than here.

Among non-infra types the concrete loser is `bd list --type session` (city `session` beads are written
`no_history`, and `session` is a custom type, not an infra type);
`workflow`/`step`/`convergence` are in the same position. No doc, skill, runbook
or agent instruction in either repo promised otherwise — swept before the
change. `docs/CLI_REFERENCE.md` deliberately does NOT document the new flag yet:
the docs describe the pinned release (`docs/cli-docs.pin`), so it appears at the
next release bump.

**Remaining carry.** Not zero, and the earlier claim that it was is wrong. The
divergence CHANGED CHARACTER rather than disappearing, which is the real win:

| File | What |
| --- | --- |
| `internal/workapi/count.go` | the one-line `!in.IncludeEphemeral` guard |
| `issueops/counter.go` | the `CountRequest.IncludeEphemeral` field + doc |
| `cmd/bd/count.go` | flag registration, read, examples line |
| `cmd/bd/list.go`, `cmd/bd/list_input.go` | the pre-existing `--include-ephemeral` flag |
| `internal/httpapi/reads.go` | reads `include_ephemeral` in `countFilters` |
| `internal/httpapi/spec/openapi.v0.yaml` | the parameter on `countIssues` |
| `internal/httpapi/apigen/types.gen.go` | GENERATED from that spec — `make api-gen` |
| `internal/httpapi/count_test.go` | parameter map, forwarding case, the filter counts |
| `cmd/bd/count_filter_test.go` | the flag tripwire's map and `want` |
| `internal/workapi/count_skipwisps_test.go` | fork-owned; no upstream conflict |
| `cmd/bd/count_include_ephemeral_embedded_test.go` | fork-owned; no upstream conflict |

The last two rows are ours and cost nothing at resync. The rest are
upstream-owned — but every one is an ADDITIVE registration of a new flag, which
is the difference that matters. The old delta CONTRADICTED values upstream
asserts (`--type task` 3 -> 6 in two count twins); that can never be upstreamed
and must be re-applied, by hand, forever, and it silently drifted once. These
go away entirely the day the flag is upstreamed.

Two of them are still in-place edits of upstream TEST files
(`internal/httpapi/count_test.go`, `cmd/bd/count_filter_test.go`) — the same
pattern this change set out to remove, so it is worth being clear-eyed about:
both are tripwires that FAIL LOUDLY when the flag is present and unregistered,
rather than assertions that silently disagree. `count_test.go` also carries
hand-maintained prose ("the role publishes 24 filters") that a resync will not
update for you.

**`openapi.v0.yaml` is the trap.** Miss that hunk on a resync and `make
api-check` fails with a regeneration diff that reads like the fork's generated
file is stale, not like a dropped delta. Re-apply the spec hunk, then
`make api-gen`.

## Known resync conflict zones

Recurring conflicts, roughly in descending pain:

1. `internal/workapi/count.go`, `issueops/counter.go`, `cmd/bd/count.go`,
   `cmd/bd/list.go`, `cmd/bd/list_input.go`, `internal/httpapi/reads.go`,
   `internal/httpapi/spec/openapi.v0.yaml` (then `make api-gen`),
   `internal/httpapi/count_test.go`, `cmd/bd/count_filter_test.go` — the
   `--include-ephemeral` carry above. Additive, so it conflicts far less than
   the delta it replaced, but it is NOT zero: see the table in that section for
   what each file holds.
2. `internal/storage/uow/*`, `internal/storage/dolt/store.go`,
   `internal/storage/issueops/commit_pending.go` — our originals vs the
   upstream re-lands. **Auto-merges cleanly and wrongly**; review by hand.
3. `go.mod` / `go.sum` / `default.nix` — the fork adds `lumberjack` and
   `x/time` as direct deps, so `vendorHash` must be recomputed after every
   resync.
4. `cmd/bd/uow_factory.go`, `cmd/bd/main.go`, `Makefile`.

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

None is a tree change you can prepare in advance; all are done per cycle.

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

3. **`make install` cannot succeed in this fork — use `install-force`.** The
   `check-up-to-date` target compares HEAD against `origin/main`, and here
   `origin` is UPSTREAM, so it always reports the fork as stale:

   ```
   ERROR: Local branch is not up to date with origin/main
     Local:  96a40b308   (our main)
     Remote: d530cddfa   (gastownhall/beads)
   ```

   `install-force` skips that check and is the correct target for this repo,
   not an override being abused. The inverted-remote convention showing up in
   the build system.

## Deploying a new bd to the fleet

**BINARY FIRST, ALWAYS. Migrating a database before deploying breaks every
stale binary against it, with no graceful degradation.**

bd refuses outright when the database is ahead of the binary:

```
schema version mismatch: database is at v66, binary knows up to v62 (4 migrations ahead)
```

`bd ready`, `bd list` and `bd doctor` all fail. This is NOT the same question as
which migrations get applied — `migrationSource.pendingVersions` selects
`version > current` from the binary's own set, so a stale binary finds nothing
pending — and reasoning from that alone gives the wrong answer. Learned on
2026-08-31 by migrating `vct` before deploying and taking wise-stack down for
fifteen minutes. It was the quietest rig only because the rehearsal was
deliberately run there.

The order that works:

1. **Back up first.** One shared Dolt server, one data dir, N databases, and
   migrations do not roll back. Use `CALL DOLT_BACKUP('sync-url',
   'file:///…/<db>')` per database THROUGH THE SERVER — the fleet holds live
   connections, so a filesystem copy can catch a torn write. Then actually
   restore one (`dolt backup restore file://…/<db> <name>`) and check its
   schema version and a row count against live. An unrestored backup is a guess.
2. **Build out of band**: `make install-force INSTALL_DIR=/tmp/bd-new`. The
   canonical path is guarded by `check-deploy-bd`; do not aim there yet.
3. **Rehearse on the smallest, idlest database.** Capture a baseline first
   (schema version, row counts, `bd ready`/`bd list` counts) by direct SQL, not
   through `bd` — store-open runs `autoMigrateOnVersionBump`, so even a read
   command can start migrating.
4. **Deploy canonically.** `bd-deploy` is referenced by the Makefile guard but
   does not exist on the dev machine; the working path is
   `make install-force DEPLOY_BD=1 INSTALL_DIR=~/.gc/bin`, which builds,
   codesigns, stages to a temp name and `rename(2)`s over the live path. Snapshot
   the outgoing binary first, following the convention already in `~/.gc/bin`:
   `bd.prev`, `bd.prev-<stamp>`, and `bd-<version>-<sha>`.
5. **Let the rest migrate on first touch**, and watch them converge. A database
   caught mid-flight reports an intermediate version (`vp` sat at 65 for a few
   seconds); that is normal, but confirm it settles.

Anything else still running an old `bd` against the shared server will break the
moment its database migrates. Check other machines before deploying.

## Local conventions

Worktrees live in `worktrees/` inside the repo. Upstream `.gitignore` only
covers `.worktrees/` (dotted), so `worktrees/` is excluded via
`.git/info/exclude` — deliberately *not* added to `.gitignore`, to avoid one
more tracked-file divergence.
