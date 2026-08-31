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
| `bd ready` nil-store panic in proxied mode | branch `fix/bd-ready-proxied-server-nil-store` (**not yet on `main`**) |
| no-op-commit gate + `bd monitor-commit-rate` (vp-5u7i, ADR-0023 L-A) | branch `gc/vp-5u7i`, Voxist PR #27 (open) |
| heartbeat/lease no-op auto-commit skip (vp-on8s) | `internal/storage/embeddeddolt/` |
| `BD_NO_AUTO_MIGRATE` fleet-migration guard | `cmd/bd/` |
| `GC_AGENT` actor resolution for `--claim` idempotency | `cmd/bd/` |
| SEC-003: `/Users/Shared` as a safe `BEADS_DIR` boundary | `internal/beads/context.go` |

Ops-only, never upstreamable: `DEPLOY_BD` install guard, nix `vendorHash`
recomputation, `prepared-dml-grandfather.txt`.

## The wisps plane rule — carried today, but SOLVABLE

`va-k0e` / `vg-3kn` / `vg-8db`. Upstream's `applyTypeSuppressions` admits the
wisp plane only for `IncludeEphemeral`, `IncludeInfra`, or an **infra** type.
The write path routes on STORAGE CLASS, not type — `internal/storage/dolt/issues.go`:

```go
useWispsTable := issue.Ephemeral || issue.NoHistory || issue.WispType != "" || s.IsInfraTypeCtx(ctx, issue.IssueType)
```

so a `no_history` task or molecule sits in the wisps TABLE and a bare
`bd list --type task` never reads it. The fork's delta makes any explicit type
admit the plane, `Ephemeral` unpinned, so `bd count --type task` answers 6 where
upstream answers 3.

**This is NOT an upstream bug.** Upstream asserts 3 in BOTH count twins,
commented "the default must stay byte-identical", and routes the tier through
`--include-infra`. A branch carrying the delta against clean upstream fails
upstream's own `TestEmbeddedCountIncludeInfra` with `count --type task = 5,
want 3`. Do not file it as a bug.

### Current cost

Editing two upstream test files every resync
(`cmd/bd/count_embedded_test.go`, `cmd/bd/count_proxied_integration_test.go`,
`--type task` 3 -> 6), plus the two conditions and 3 golden cases. The twins
**silently drifted**: when the delta landed only the embedded one was updated,
and the proxied one still asserted upstream's 3 until the 2026-08-31 resync
pulled that file into the tree and CI failed. There is no seam that avoids the
edits — both twins build fixtures inline and shell out to the built binary, with
no injectable hook, build tag, or config the assertions read.

### The way out (investigated 2026-08-31, not yet applied)

**The fork already owns the right opt-in and did not know it.** It carries a
fork-only `--include-ephemeral` flag on `bd list` (`cmd/bd/list.go`,
`cmd/bd/list_input.go`, 6 lines). Measured: upstream's
`bd list --include-ephemeral --type X` produces a filter **byte-identical** to
what the fork's modified default produces for `bd list --type X` — same
`SkipWisps`, same `ExcludeTypes`, every other field equal.

`bd count` has no such flag; `CountRequest` has only `IncludeInfra`. And
`--include-infra` is **not** a substitute — upstream's own doc
(`issueops/counter.go:108-126`) calls it "FOUR changes at once and not one".
Under a pinned non-infra type two are no-ops, but `IsTemplate:false` is not:
`bd count --type task --include-infra` silently drops **template** tasks the
fork's count includes. That is a narrowing — the same silent-undercount failure
the delta exists to prevent, relocated.

So the minimal fix is additive and leaves upstream's default untouched:

1. add `IncludeEphemeral` to `issueops.CountRequest` and wire an
   `--include-ephemeral` flag on `bd count`, mirroring `bd list`;
2. revert `internal/workapi/count.go` to upstream and change only the last arm
   to `} else if !in.IncludeEphemeral { filter.SkipWisps = true }`;
3. revert `internal/workapi/list.go`, the golden corpus and **both** count twins
   to byte-identical with upstream;
4. retarget the two fork test files at the flag (`--type X` -> `SkipWisps=true`;
   `--type X --include-ephemeral` -> `SkipWisps=false`), and add a `cmd/bd`
   integration case asserting `bd count --type task --include-ephemeral` = 6.

Per-resync cost then drops to **zero**, and fork carry shrinks to the two flag
registrations — themselves upstreamable, since upstream already owns the field
(`issueops/reader.go`), exposes `include_ephemeral` over HTTP, and registers the
flag on `bd ready` and `bd linear sync`, just not on `bd list`.

### Why the original rationale no longer holds

- **The motivating consumer is gone.** `va-k0e` (`f3a12745e`) existed because
  wisp-leak reconciliation needed `bd list --type=molecule` to find in-flight
  wisps. That consumer now passes an explicit `TierBoth` that unions
  `--include-infra` with a separate ephemeral query, and reaches those rows
  without the fork default.
- **No programmatic consumer depends on it.** The orchestrator's only `bd list`
  call site already appends `--include-infra --include-gates` unconditionally,
  and it never shells out to `bd count` at all (its counter is direct SQL).
- **The "test behind it" was circular.** This document previously argued the
  contract was settled because a test pinned it — but that test *is* the fork's
  own edit of upstream's test, not independent evidence.
- **The fleet's own model contradicts the wide default**: its default tier is
  `ephemeral = 0` — durable plus no-history, ephemeral only via an explicit
  tier. The honest need is the **no_history** rows, not the ephemeral ones.

### What it would cost

A real, user-visible regression for interactive use: `bd list --type X` and
`bd count --type X` go back to durable-only, and a human must add
`--include-ephemeral`. The concrete loser is `bd list --type session` — city
`session` beads are written `no_history` and `session` is a registered custom
type, not an infra type. `workflow`/`step`/`convergence` are in the same
position; `molecule` and `event` lose rows only where a writer chose
`no_history`.

**No documented contract promises otherwise.** A sweep of beads `docs/`,
`engdocs/`, `plugins/beads/skills/`, `AGENT_INSTRUCTIONS.md`, `AGENTS.md` and
the gascity equivalents found no runbook, skill or agent instruction that tells
anyone to expect wisp-plane rows from a bare type filter. Mitigation is light:
regenerate `docs/CLI_REFERENCE.md` so `--include-ephemeral` is discoverable,
patch the `--type event` examples in `docs/core-concepts/labels.md`, and call it
out in release notes. The exposure is operator muscle memory, not a contract.

### Where it lives today, and what guards it

| Side | Code | Guard |
| --- | --- | --- |
| list | `internal/workapi/list.go` (`applyTypeSuppressions`) | `TestBuildListFilter_SkipWisps`, `TestBuildListFilter_PlaneAdmitsNamedTypeWhereverItLives`, 3 golden cases |
| count | `internal/workapi/count.go` (`BuildCountFilter`) | `TestBuildCountFilter_PlaneRule` (unit), `TestEmbeddedCountIncludeInfra` + `TestProxiedServerCountIncludeInfra` (cmd/bd, gated on a live backend) |

Both cmd/bd twins must be edited together, and both unit guards are
mutation-checked. Until the fix above is applied, `Ephemeral` stays **unpinned**:
pinning it false was tried during review and reverted, because it makes the
embedded twin's 3 durable + 2 no_history + 1 ephemeral task answer 5, not 6.

## Known resync conflict zones

Recurring conflicts, roughly in descending pain:

1. `internal/workapi/list.go`, `internal/workapi/count.go`,
   `testdata/list_filter_golden.json` (and, when upstream moves them,
   `cmd/bd/count.go` / `count_filter_test.go` / `list_golden_test.go`) — the
   wisps plane rule above. Semantic, not textual: upstream keeps restructuring
   the surrounding code, so the delta has to be re-applied on the new shape
   rather than merged. Re-read that section before resolving; the answer is
   KEEP, and the tests named there are what prove you got it right.
2. `internal/storage/uow/*`, `internal/storage/dolt/store.go`,
   `internal/storage/issueops/commit_pending.go` — our originals vs the
   upstream re-lands. **Auto-merges cleanly and wrongly**; review by hand.
3. `go.mod` / `go.sum` / `default.nix` — the fork adds `lumberjack` and
   `x/time` as direct deps, so `vendorHash` must be recomputed after every
   resync.
4. `cmd/bd/uow_factory.go`, `cmd/bd/main.go`, `issueops/reader.go`, `Makefile`.

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
