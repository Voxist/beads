# `bd serve` operator runbook

Last reviewed: 2026-11-09

Freshness source: `internal/httpapi/server.go`,
`internal/httpapi/events_watch.go`, `cmd/bd/serve.go` and
`internal/httpapi/auth.go` — the operating-envelope constants, the fields their
`event` emitters write, and the flag, posture and token-file rules.

Every flag and startup refusal quoted below was checked against a build of
those files, not read off the source; event names, fields and reason values
were checked against their emitters.

Running the v0 HTTP surface. For the contract it serves — the operations, the
error vocabulary, the cursor, the loopback posture — see
[design/bd-serve-v0.md](design/bd-serve-v0.md).

Every number below is a constant in `internal/httpapi/server.go` (stream
numbers in `events_watch.go`, token-file numbers in `auth.go`) unless stated
otherwise. Only `--large-apply-ceiling` is a flag; when the others become flags
the wire contract does not change. The `event=limits` startup line prints the
envelope a running build compiled in.

## Deployment

### Pass an explicit port

```
bd serve --addr 127.0.0.1:7777
```

The default is `--addr 127.0.0.1:0`, which takes an ephemeral port. That is
right for ad-hoc and test use, where the bound address printed on stdout is read
immediately — and it carries **no mutual exclusion**.

Be precise about this, because the tempting shorthand is wrong. "The TCP bind is
the singleton" holds only for an *explicit* port: two serves against one
workspace on `:0` bind two different ephemeral ports and run side by side, with
no way to enumerate them and nothing that fails. On a fixed port the second
process fails to bind, which is the intended behavior and the only mutual
exclusion this command has.

Concurrent serves are data-safe either way — claims are arbitrated in the SQL
server, not in the HTTP process. What you lose without a fixed port is the
ability to know how many servers you are running, and clients' ability to find
the one you meant.

There is no lock file, pid file or discovery file. `bd serve` is
operator-invoked, and on a fixed port the TCP bind *is* the mutual exclusion: a
second instance fails at bind with the operating system's own address-in-use
error. Clients are configured with the address; they do not discover it.

The host in `--addr` must be a numeric IP literal; a DNS name is refused.

### Streams

stdout gets exactly one line, at bind time, and nothing else:

```
bd serve: listening on http://127.0.0.1:7777
```

That is how a caller who asked for an ephemeral port discovers it — read one
line from stdout and stop. Everything else, including the request log, goes to
stderr, so the two can be redirected separately.

### Run it under a supervisor

`bd serve` runs in the foreground and shuts down gracefully on **SIGHUP** as
well as SIGINT and SIGTERM. SIGHUP is in that set on purpose: closing the
terminal of a foreground `bd serve` stops it, rather than leaving an orphan
holding a port and a pool of database connections. A supervisor that
double-forks and expects the child to survive its controlling terminal must
either detach the process from the terminal itself or keep it in the
foreground under the supervisor.

### Authentication

Off by default, and off is the loopback posture: a `bd serve` with no auth
flags is byte for byte the server it has always been.

```
bd serve --addr 127.0.0.1:7777 --auth-token-file /run/secrets/bd-tokens
```

Every operation except `GET /healthz` then requires
`Authorization: Bearer <token>`. `GET /v0/beads/context` is **not** exempt — it
reports the repo root, the beads directory and the database name. `/healthz` is
the one exemption, because a kubelet probe presents no credential and a
liveness endpoint that 401s is a pod that restarts forever.

The file holds **one token per line, and every non-empty line is accepted**.
There is no `--auth-token` flag: an argument is readable out of `ps` by every
local user. `BEADS_SERVE_TOKEN_FILE` is the environment fallback, and it
applies only when the flag was not passed.

**Rotation is a file rewrite, with no restart.** Write the new token alongside
the old, roll the clients over, then delete the old line; both the addition and
the removal take effect within about a second. The server re-reads the file
while it runs — gated to at most one `stat(2)` per second for the whole
process, on the accepting path as well as on a mismatch, which is what makes
revocation work and not just rotation. Write it atomically (temp file plus
rename; a Kubernetes secret mount already does). A failed or empty re-read
keeps the last-good set and logs `event=auth_reload_error`, so a writer that
truncates before writing cannot lock every client out.

Tokens are held and compared as SHA-256 digests in constant time, so the
process holds no raw credential and a heap dump discloses none.

The refusal is `401` with `code: unauthenticated` and `WWW-Authenticate:
Bearer`. Its `detail` is a fixed string and never echoes what was presented; a
missing header, a wrong scheme and an unrecognized token are deliberately one
code. Each one logs `event=auth_refused` with `reason=missing`,
`reason=malformed` or `reason=unknown_token`, which is where an operator tells
a misconfigured client from a stale token. The check runs **before** the
database semaphore, so a storm of refusals costs one SHA-256 each and can never
occupy the slots authenticated clients are waiting for.

A token is a shared secret granting the **whole** surface. It is not an
identity and carries no scopes, so it never makes `actor` an authenticated
principal — `actor` stays caller-asserted provenance for the audit trail, and
any client the token admits can claim as any name.

Confirm the posture from the startup line rather than from the absence of a
flag: `event=startup` carries `auth=none` or `auth=bearer (<path>)`.

### Binding beyond loopback

`--allow-non-loopback` **requires `--auth-token-file`**. Beyond loopback,
reaching the address would otherwise be the whole authorization: every peer
that can reach it gets full read and claim access.

```
bd serve --addr 0.0.0.0:7777 --auth-token-file /run/secrets/bd-tokens \
         --allowed-host bd.internal.example
```

`--insecure-no-auth` is the explicit, auditable way to bind beyond loopback
with no credential. It applies only beside `--allow-non-loopback` (on loopback
there is nothing to waive) and it contradicts `--auth-token-file`. Use it only
where a network boundary you already trust is doing the job.

Either way a non-loopback bind prints a warning on stderr at startup, and the
two are different sentences — one names the missing credential, the other names
the missing TLS:

```
bd serve: WARNING: --insecure-no-auth binds 0.0.0.0:7777 beyond loopback with no authentication. Any peer that can reach it can read every issue and claim work as any actor.
bd serve: WARNING: 0.0.0.0:7777 is bound beyond loopback with bearer authentication but NO TLS. Tokens and issue data travel in plaintext; deploy it inside a trusted network boundary.
```

**There is still no TLS.** Even with a token, the credential and every issue
body travel in plaintext, so a deployment beyond loopback has to supply
confidentiality itself — a service mesh, or a trusted network boundary. An
authenticating reverse proxy in front is still a reasonable shape; the token
file is what stops the origin being open if it is bypassed. Every response
carries `Cache-Control: no-store`, so a proxy must not cache answers about live
work.

**The Host allowlist is what a service deployment trips over first.** The
DNS-rebinding check answers only to loopback spellings (`127.0.0.1`, `::1`,
`localhost`) and the bind address, so a client dialing a service DNS name gets
a `400` on every request. A wildcard bind (`0.0.0.0`, `::`) also answers any
numeric IP literal, but still no name. Enumerate the names clients dial with
`--allowed-host` (repeatable, matched exactly after the request's port is
stripped, case-insensitive, no wildcards; an entry carrying a port is refused at
startup, since it could never match). The `event=startup` line prints the
effective allowlist as `host_allowlist`.

One behavioral difference follows `--allow-non-loopback`: `limit=0` (unlimited)
is refused on all three paged reads (`GET /v0/beads/ready`,
`GET /v0/beads/issues`, `GET /v0/beads/issues:query`) with 400
`invalid_argument` / `reason: "invalid_value"`, whether or not a token is
configured. Clients must page.

The blocked listing (`GET /v0/beads/blocked`, behind `blocked.list`) is the
exception, on every bind: like `bd blocked`, it takes no `limit` and no cursor,
so it answers the whole blocked set in one body. That set, not the whole active
one, is what it buffers, and the client bounds what it will read (bd's HTTP
client refuses a body over 32 MiB). The operation answers `has_more` so a
later server can bound it; a client must refuse `has_more: true` rather than
treat it as the whole listing.

## Probes

| Probe | Route | What green means |
|---|---|---|
| Liveness | `GET /healthz` | The process is running. |
| Readiness | `GET /v0/beads/ready?limit=1` | The process is running **and** the database answered. |

`/healthz` answers from the process and never touches the database. It stays
green while the database is unreachable, wedged, or idle-stopped — which is what
makes it a correct liveness probe and a useless readiness probe. Do not wire it
to a load balancer's readiness check.

`GET /v0/beads/ready?limit=1` is a real query with a one-row bound. 200 means
ready; 503 means live but not ready, and its `code` says which kind:
`db_unavailable` for a connectivity failure, `busy` for contention or slot
saturation. Both carry `Retry-After`; a probe should treat either as not-ready
and retry, not as a hard failure.

Suggested probe settings: the readiness probe inherits the server's 60s
whole-request deadline as its worst case, so give it a timeout well under that
(2–5s) and let the probe's own failure threshold do the smoothing.

**On a server started with `--auth-token-file`, the readiness probe needs the
token and the liveness probe does not.** `/healthz` is the one auth-exempt
route; `GET /v0/beads/ready` is not, so an unauthenticated readiness probe gets
a `401` — never a 503 — and a load balancer reading only the status class will
book a healthy server as permanently not-ready. Give the readiness probe an
`Authorization: Bearer` header, and alert on `event=auth_refused` with
`op=listReadyWork` as the signal that you forgot.

## Detecting a wedge

The failure mode to plan for is a database that stops answering while the
process stays healthy. `/healthz` cannot see it. Three signals can:

- **`event=semaphore_saturated`** — a request waited a second or more for a
  database slot. This is the wedge-detection signal: with no traffic there are no
  saturation events, so a stream of them distinguishes "wedged" from "quiet".
  `outcome=acquired` got its slot in the end; `outcome=abandoned` means the
  client hung up or the request deadline expired while queued, which is the same
  wedge seen from a request that did not live long enough to be shed.
- **`event=semaphore_timeout`** — a request waited the full 10s and was shed as
  503 `busy`.
- **`event=conn_cap_saturated`** — accepted connections reached the 64
  connection cap. Logged once per crossing.

Alert on the readiness probe, and use these to tell a wedged database from a
slow client.

## Connection budget

Size a shared external `dolt sql-server` before pointing several `bd serve`
processes at it. Every `bd serve` process claims, at steady state:

| Consumer | Connections |
|---|---|
| Handler pool, max open (`maxInflight + 4`) | 20 |
| Root command's `DoltStore`, in server / external-server / shared-server mode | 1–2 idle |
| **Per-process worst case** | **~22** |

The shape behind those numbers:

- `maxInflight = 16` bounds handlers that touch the database. Each unit of work
  pins one SQL connection, so that is also the steady-state connection count
  under load.
- The pool is sized `maxInflight + 4 = 20` open, 16 idle, because the semaphore
  bounds *handlers*, not *connections*. A connection poisoned by a failed
  ROLLBACK is replaced, each retry attempt of a committing transaction opens a
  fresh unit of work on a fresh pinned connection, and any semaphore-exempt
  handler that later touches the database escapes the semaphore entirely. The
  four spare slots absorb that.
- In server, external-server and shared-server modes the root command has
  already opened a `DoltStore` that `bd serve` never uses. It stays open for the
  life of the process and holds an idle connection or two against the very
  server this process is about to pool twenty more on. Opening and closing it is
  the root command's business, not one command's — but it is not free, and it
  belongs in the arithmetic.

So `max_connections` on a shared server must cover roughly `22 × (number of bd
serve processes)` plus every other `bd` process pointed at the same server,
each of which claims its own. Idle connections are reclaimed after 20 seconds
(kept under the 30s Dolt `wait_timeout` bd supports, so the pool never hands a
request a connection the server already reaped) and recycled after an hour, so
a bursty deployment settles below the worst case — but size for the worst case.
The `event=limits` line prints these as `pool_max_open`, `pool_max_idle`,
`pool_idle_time` and `pool_lifetime`.

Those pool limits are applied only if the unit-of-work provider exposes the
knob. If it does not, `bd serve` says so at startup with
`event=pool_limits_unavailable` and runs with an **unbounded** pool rather than
silently pretending. Check for that line before trusting the arithmetic above.

None of this arithmetic applies to a registered backend, which is served from
the store the root command opened rather than from a unit-of-work provider
(`db=roles` on the startup line, against `db=provider` for every Dolt topology).
The pool belongs to that backend, `bd serve` neither owns it nor can reach it,
and no `pool_limits_unavailable` line is emitted — a missing capability would be
reported for a provider that was never asked for. Size that pool wherever the
backend is configured.

`maxConns = 64` bounds *accepted TCP connections*, which is a client-side limit
and not part of the database budget. It exists because the semaphore does not
bound connections: Go spawns a goroutine per connection, and one parked on a
full semaphore still holds its goroutine, file descriptor and buffers. Excess
connections wait in the kernel accept backlog instead of in Go memory.

`maxWatchStreams = 48` caps `GET /v0/beads/events:watch`, the one operation
whose requests last hours, and it sits **16 connections below `maxConns`
deliberately**. `LimitListener` hands a connection slot back only when the
connection closes — after the handler returns, and therefore after the stream
counter has already come down — so a stream cap equal to the connection cap
would be unreachable: the connect that should earn the `503` would never be
accepted, and neither would the poll it is told to fall back to. The headroom is
what makes the refusal deliverable and keeps polls, mutations and a
fresh-connection `/healthz` answerable while every stream slot is taken.

Watch the gauge, not the cliff: `event=events_watch_admitted` carries
`streams=N max_streams=48` on every connect, so accumulation is visible before
the first refusal. `event=events_watch_saturated` is the refusal itself.

**Connection saturation at `maxConns` is the worse cliff and is still
reachable** — by 64 ordinary clients, or by 48 streams plus 16 other
connections. It is silent by construction: `LimitListener` simply stops calling
`Accept`, so further connections wait in the kernel backlog with nothing on
stderr and `/healthz` unable to get a fresh connection either. `conn_cap_saturated`
is the only warning, and it is edge-triggered; see [Detecting a wedge](#detecting-a-wedge).

A stream has no read deadline (SSE clients legitimately send nothing for hours,
so one would kill healthy streams), which raises the question of what reaps a
consumer that vanishes without a FIN or RST — a yanked cable, a killed VM.
**TCP keepalive already does**, and is on by default: Go's listener enables
`SO_KEEPALIVE` on every accepted connection and sets its own idle (15s),
interval (15s) and probe count (9) over the system's. Measured on
this fleet (2026-08-09): `SO_KEEPALIVE=1`, `TCP_KEEPIDLE=15`, `TCP_KEEPINTVL=15`,
`TCP_KEEPCNT=9` — a **~150 second** reap for a connection that is idle, against
the ~2h11m the bare system defaults (7200/75/9) would have given. A stream that
is actively writing when the peer vanishes is reaped by retransmission instead,
on `tcp_retries2` (15 here, roughly **15 minutes**), because keepalive does not
run while data is unacknowledged. So the worst case for a stream slot held by a
vanished peer is minutes, not hours — and the stream cap is sized on the
assumption that it is minutes.

**Streams cost nothing from the database budget between reads**: the handler
takes a semaphore slot around each one-second poll and gives it straight back,
so 48 open streams do not occupy 16 handler slots. They are exempt from the 60s
whole-request deadline for the same reason, and each read is bounded by its own
15s deadline instead — short because it also bounds how long a stream can go
without a heartbeat and how long it can ignore a shutdown, not because a page of
1000 journal rows should ever take that long. Tell accumulating consumers to
poll `GET /v0/beads/events`, which is never refused for capacity. A refused
stream's `503 events_watch_saturated` carries `Retry-After: 30`.

A caught-up stream polls about once a second (jittered by up to ±10% so
reconnected streams do not read in lockstep) and writes a `heartbeat` comment
after 20 seconds of silence. **Any proxy or load balancer in front must have an
idle timeout above 20 seconds**, or it cuts healthy streams, and must not buffer
the response (the stream sends `X-Accel-Buffering: no`; other proxies need
buffering turned off for the route). The stream's first line tells clients to
reconnect after 3 seconds.

When the workspace journals with auto-prune on, `bd serve` runs journal
retention itself on a 5-minute ticker
(`eventsjournal.DefaultAutoPruneTickInterval`, started from
`cmd/bd/serve.go`), sharing the persisted schedule the CLI uses. A
consumer whose checkpoint falls behind the prune floor gets `410
events_journal_truncated` (or a `truncated` event mid-stream) and must
re-baseline. Prune results are logged only under bd's debug logging.

## Shutdown, and the ambiguous claim

On SIGINT, SIGTERM or SIGHUP the server stops accepting and drains for up to
**20 seconds**. The drain deliberately does *not* cancel in-flight handler
contexts: the budget covers a claim inside its serialization-retry budget plus
its commit, because killing such a connection early would leave the client
unable to tell whether its write landed.

**A large `POST /v0/beads/issues:batchApply` (over 100 items) extends this
budget rather than being cut off by it.** Such a request can legitimately still
be running at up to `--large-apply-ceiling` (5 minutes by default) when
shutdown begins, so the drain budget computed at shutdown start extends to that
request's own remaining deadline, plus 5 seconds to write its response back,
instead of the ordinary 20 seconds — the in-flight write is waited out, not
killed mid-commit. A large apply still *queued* for the one-wide large-apply
semaphore when shutdown begins is refused with `503` immediately rather than
being admitted or extending the drain further.

Large applies are serialized outside shutdown too: one runs, at most one more
waits up to 10 seconds for the slot, and any other arriving meanwhile is refused
at once with `503 busy`. Once admitted, a large apply runs to its ceiling even if
its client hangs up.

**The orchestrator's stop grace (e.g. a Kubernetes `terminationGracePeriodSeconds`)
must be at least `--large-apply-ceiling` plus 5 seconds.** The drain logic above
only runs inside this process; an external `SIGKILL` after the orchestrator's
own grace period expires cuts the process off regardless of what the drain was
waiting for, which can still sever an in-flight large apply the drain would
otherwise have waited out. Size the grace period to the ceiling plus those 5
seconds, not to the ordinary 20s drain.

**Raising `--large-apply-ceiling` past about 5m15s also outruns bd's own HTTP
client.** It waits 6 minutes for a batch apply over 100 items: the default
ceiling plus what the server may spend around it (the 30s request read, the
10s wait for the large-apply slot, 5s to write the answer back). The server
does not advertise its ceiling, so a client cannot wait longer for a server
that grants more, and an apply that runs past the client's 6 minutes is
reported to the caller as a timeout with an unknown outcome while the server
goes on to commit it.

Journal streams are the exception, and they have to be: a held-open response
would otherwise sit through the whole budget and then be killed anyway. They get
an explicit close signal when the drain starts and end on their own within a
poll interval — or, if they are mid-read or mid-backlog, within one 15s read —
so an open stream does not turn a clean stop into a twenty-second one. Their
clients reconnect and resume from the id they reached.

One case still forces it, and it is accepted rather than fixed: a stream whose
client has **stopped reading** blocks in a write, and the write-stall deadline
that frees it is 30 seconds against a 20-second drain. Such a stream will be
force-closed, and the `shutdown_forced` line is correct — that connection was
already ambiguous. Nothing is lost: a stream carries no write, and its client
resumes from its own last id.

If the drain budget is exceeded, remaining connections are closed and
`event=shutdown_forced` is logged with the count. That is the ambiguous case,
and it is a client-visible one: a claim killed mid-commit may or may not have
landed, and the client saw a dropped connection rather than a response.

**Recovery is a re-claim, and it is safe by construction.** A claim is a
compare-and-set, and a re-claim by the same actor is idempotent: if the first
attempt landed, the second finds the actor already holds it, returns 200 and
writes no commit. If the first did not land, the second claims normally. If a
different actor won in between, the second gets a typed 409 `already_claimed`
carrying that actor in `assignee` — which is the true answer, not an error to
retry through.

So the recovery for an ambiguous shutdown is: re-issue the claim with the same
actor and read the result. Never infer the outcome from the dropped connection,
and never fall back to reading the issue and guessing.

The same reasoning covers a client that times out, hangs up, or is restarted
mid-claim. A client hanging up is not counted as a server fault:
`event=request` records `code=client_closed` and no `request_error` is emitted,
so an impatient caller does not spike the signal an operator alerts on.

## Request log

One structured line per request on **stderr**, prefixed `bd serve: ` and a UTC
timestamp from the standard logger:

```
bd serve: 2026/08/01 05:17:42 event=request request_id=8f3a1c07-000042 op=listReadyWork method=GET path=/v0/beads/ready status=200 code="" duration_ms=12.480 sem_wait_ms=0.031 uow_ms=11.902 conns=1 remote_addr=127.0.0.1:54321
```

Fields, in order:

| Field | Meaning |
|---|---|
| `request_id` | Correlation id, `<per-process random prefix>-<sequence>`. Echoed in the `request_id` member of any problem body. |
| `op` | The spec's `operationId`. |
| `method`, `path`, `status` | As sent and answered. |
| `code` | The problem `code`, or `""` on success. |
| `duration_ms` | Whole request, milliseconds with three decimals. |
| `sem_wait_ms` | Time spent waiting for a database slot. |
| `uow_ms` | Time spent holding the unit of work. |
| `conns` | Live accepted connections at that moment — watch this climb toward 64. |
| `remote_addr` | Which client; on loopback the port identifies the local process. |
| `refused` | Present only when the request was refused for a specific value: the offending value. |

The per-process random prefix means ids from two servers, or two runs of one
server, never collide in a shared log.

Values are quoted whenever they contain a space, `"`, `=` or any control
character, and an empty value renders as `""`. That is not cosmetic: without it
a caller-supplied value — a `Host` header, a rejected parameter, an error
message — could forge fields or whole lines, and an unquoted C1 control such as
U+009B is a CSI introducer that would drive the operator's terminal.

Other events on the same stream:

| Event | When |
|---|---|
| `startup` | `addr`, `mode`, `db` (`provider` or `roles`), `workspace`, `beads_dir`, `database`, `host_allowlist`, `capabilities`, and `auth` — `none`, or `bearer (<token file path>)`. Check `auth` after a deploy rather than inferring it from a process listing. |
| `auth_refused` | A `401`. Carries `request_id`, `op`, `remote_addr` and `reason` (`missing`, `malformed`, `unknown_token`). A burst of `unknown_token` from one peer is a client left on a rotated-out token; `missing` is a client that was never configured with one. |
| `auth_reload_error` | The token file could not be re-read. **Not** a refusal — the last-good set stays in force — but the file an operator is rotating is unreadable and nothing else says so. |
| `limits` | The operating envelope this build compiled in: `max_inflight`, `max_conns`, `sem_wait`, `deadline`, the effective `large_apply_ceiling`, and on the provider source the `pool_*` limits. Log it, then compare against this page. |
| `request_error` | Accompanies a ≥500, carrying the real error the body withholds. Join on `request_id`. Not emitted when the client hung up (see above). |
| `request_refused` | A 4xx from a write operation (create, update, batch create, close or apply, claim-next, release, heartbeat, dependency edit, metadata CAS) whose response rewords the storage refusal: carries the original `error`. Join on `request_id` when a client's 400 or 409 needs explaining. |
| `pool_limits_unavailable` | The unit-of-work provider does not expose the pool knob, so the limits below are *not* applied and the pool is unbounded. Worth alerting on; it changes the connection budget. |
| `panic` | A panicking handler: the value, the stack, and the same `request_id`. |
| `semaphore_saturated`, `semaphore_timeout`, `conn_cap_saturated` | See [Detecting a wedge](#detecting-a-wedge). |
| `shutdown_start`, `shutdown_complete`, `shutdown_forced` | The drain. |
| `events_watch_admitted` | A journal stream opened, carrying `streams=N max_streams=48`. One line per stream, not per record: this is the gauge that shows accumulation before the cap is hit. |
| `events_watch_saturated` | A journal stream was refused because this process is already holding its cap. Carries the live count and the cap. Streams end when their consumers leave, so a run of these means consumers are accumulating, not that the server is slow. |
| `events_watch_failed` | An open journal stream ended on a failure it could not report to the client, because the `200` was already sent: a read that failed (carrying the checkpoint it reached), or a stored payload that would not encode (carrying its `seq`). The client reconnects on its own; a repeated read failure names a database problem, and a repeated `seq` names one unencodable row that will end every stream that reaches it. |
| `events_watch_unflushable` | A journal stream was refused because the response writer cannot flush. Not reachable through `bd serve`'s own server; it means something is wrapping this handler. |

A 5xx body carries a fixed static detail by design, so `request_id` is the
client's only handle on the one line that has the real error. When a user
reports a 500, ask for the `request_id` and grep for it — the `request` line
gives the shape, the `request_error` line gives the cause.

## Refusals to expect at startup

| Message | Cause |
|---|---|
| `operation "serve" not supported by the embedded-dolt backend: bd serve requires a Dolt SQL server; this workspace uses embedded Dolt` | Permanent. The embedded backend commits outside the SQL transaction on a separate connection, so this server's per-request atomicity would be a lie there. Refused by `serveDatabaseSource`, which is the only thing refusing it — see "Workspace modes" in the design doc. |
| `bd serve is unavailable under strict readonly` | `--readonly`, or `readonly` in config. Every server this command binds publishes the issue-claim operation and the advertised capability set is a property of the build, not of the flags on the process that started it — so the alternatives were a server advertising a claim it always fails (the store source, where the read-only open reaches the claimer) or a `--readonly` that quietly bought nothing (the provider source, which builds its own writable connection). Drop the flag to serve. |
| `--addr "localhost:7777": host must be a numeric IP literal, not a name — use 127.0.0.1 rather than localhost` | `--addr` was given a DNS name. |
| `--addr "7777" must be HOST:PORT with a numeric IP literal host (unix sockets are not supported): …` | `--addr` is not `host:port`; a bare port or a socket path is refused. |
| `--addr "0.0.0.0:7777" binds beyond loopback, which requires --allow-non-loopback (and, with it, --auth-token-file)` | A non-loopback `--addr` without the flag. |
| `--allow-non-loopback requires --auth-token-file (or the explicit --insecure-no-auth): every peer that can reach the address gets full read and claim access` | Binding beyond loopback with no credential and no waiver. |
| `--insecure-no-auth applies only to a bind beyond loopback; on loopback there is nothing to waive, so pass --allow-non-loopback or drop the flag` | The waiver without the bind it waives. |
| `--insecure-no-auth contradicts --auth-token-file; pass one or the other` | Both spellings of the auth decision at once. |
| `--auth-token-file: token file <path>: open <path>: no such file or directory` | The token file is unreadable. Also `is a directory`, `contains no tokens`, and `is larger than 1048576 bytes; that is a mis-pointed path, not a token file`. All four are refusals at startup, never a server that binds anyway. |
| `--allowed-host "bd.internal.example:7777" carries a port; the port is stripped from a request's Host before matching, so an entry with one could never match` | An allowlist entry with a port. An empty entry, one with whitespace, and one that looks like a URL are refused the same way. |
| `--large-apply-ceiling must be positive, got 0s` | A zero or negative ceiling. |
| `bind <addr>: … address already in use` | The fixed-port mutual exclusion working as intended: a second server is already on that port. |

The flag refusals above (everything but the embedded, readonly and bind rows)
are raised by `resolveServeConfig` before serve opens its database source or
listener, independent of the workspace, so they read the same in every
workspace mode. The readonly refusal follows them, still ahead of the
workspace; in proxied-server mode the root command refuses `--readonly` first,
with `proxy.readonly.unsupported`. (The root command's
`PersistentPreRunE` still resolves the workspace first, so a directory with no
beads database answers `no beads database found` ahead of any of them.) And
`httpapi.Listen` re-checks the same rules, so a second caller of the package
cannot assemble a `Config` that serves the whole surface to a network with no
credential.
