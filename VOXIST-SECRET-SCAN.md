# Voxist secret scanning (Voxist/beads fork only)

This fork of [gastownhall/beads](https://github.com/gastownhall/beads) adds a
betterleaks secret scan that upstream does not have. It is Voxist-only
tooling (bead vp-ty88y) and is **not** part of the upstream project.

> Deliberately placed at the repo root, not under `docs/`: `docs/` is a
> Mintlify site whose nav/link sync is enforced by
> `test/docsync/docsync_test.go` (`TestEveryDocsPageIsPublished`), which fails
> the build for any `docs/*.md` file that isn't registered in
> `docs/docs.json` (or the tiny `docsPublishExemptions` stub list). A page
> here needs no registration and stays out of upstream's Mintlify nav
> entirely, which also keeps this file out of anything a resync could touch.

## What runs, and where

Three Voxist-only files, none of which upstream's tree contains:

- `.github/workflows/voxist-secret-scan.yml` — the `secret-scan` job
  (`pull_request` + `push` to `main`)
- `.betterleaks.toml` — the Voxist betterleaks ruleset + allowlist
- `.github/ci/secret-scan-range.sh` — computes the commit range to scan

The job runs the [betterleaks](https://github.com/betterleaks/betterleaks)
CLI binary directly (pinned to `v1.3.1`, checksum-verified at install time —
the org standard; see `voxist-platform`'s `docs/betterleaks-setup.md` and
`SECURITY-SUPPLY-CHAIN.md` §12). It scans only the commits introduced by the
triggering PR or push — never the full repository history — computed by
`secret-scan-range.sh`. This is the same scoping fix as
`voxist-platform`/`livetranslate-webrtc` (bead vc-rq8b): an unscoped scan
walks `git log -p --all` and fails a PR for findings that live on someone
else's unrelated branch.

## It will become a required check

The `secret-scan` job id **is** the GitHub status-check context (no `name:`
override — don't add one). This fork's `main` currently has no
`required_status_checks` configured; the operator decision behind vp-ty88y is
to make `secret-scan` required once this workflow is verified green. Do not
rename the job without also updating branch protection, or the required
check silently stops being enforced.

## Extending the resync allowlist

This fork periodically resyncs from upstream via a `bfork/main <- origin/main`
merge PR (see e.g. #60). If a resync brings in an upstream commit that trips
a real finding against upstream's own test fixtures, example keys in docs, or
similar known-safe content, extend the allowlist **in that resync PR**:

1. Open `.betterleaks.toml`.
2. Add a `[[allowlists]]` entry under the "Resync allowlist" section at the
   bottom of the file, with a narrow `paths` regex (a fixture directory or a
   specific file, not a broad glob) and a one-line `description` that says
   why — link the resync PR or the upstream commit.
3. Do **not** loosen the global `prefilter`/`filter` sections above it for a
   single upstream file; those are shared, org-wide false-positive rules.

Do not allowlist a finding you cannot explain. If a resync brings in what
looks like a real secret, stop and report it — don't allowlist it to get the
resync green.

## Running it locally

```sh
# From the repo root, with betterleaks installed and on PATH:
betterleaks git . --config .betterleaks.toml --log-opts="<base>..<head>"

# Or, to reproduce exactly what CI computes for the current branch:
range="$(GITHUB_EVENT_NAME=push GITHUB_SHA="$(git rev-parse HEAD)" \
  PUSH_BEFORE_SHA="$(git merge-base HEAD origin/main)" \
  bash .github/ci/secret-scan-range.sh)"
betterleaks git . --config .betterleaks.toml --log-opts="${range}"
```
