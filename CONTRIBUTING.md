# Contributing to MiBee NVR

Thanks for considering a contribution!

## Contribution licensing (important)

MiBee NVR is licensed under **AGPL-3.0-only** (see [LICENSE](LICENSE) and
[NOTICE](NOTICE)), with a linking exception for `pkg/` (see
[LICENSE.pkg-linking-exception](LICENSE.pkg-linking-exception)).

Unless you explicitly state otherwise, **any contribution you
intentionally submit for inclusion in this repository shall be
dual-licensed: MIT OR AGPL-3.0-only, at the project's option** —
meaning the project may use, distribute, and relicense your
contribution under either license (including future versions of the
project under AGPL-3.0-only together with the pkg/ linking exception),
without any additional permission required from you.

This keeps the door open for the project's own future licensing
decisions while your contribution remains fully open source. If you
prefer different terms for your contribution, please state them
explicitly in your pull request so they can be reviewed before merge.

## Before you submit

1. Run `make lint` (golangci-lint v2; `make lint-install` first) — CI
   enforces it.
2. Run `make test` (Go) and `cd web && npm test` (frontend) — CI
   enforces 55% minimum Go coverage.
3. Frontend rules: Svelte 5 runes only (no legacy `$:` reactive
   syntax), plain Vite + hash routing (no SvelteKit patterns), no
   `@ts-ignore`. Building the web UI from source requires
   **Node ≥ 24.15** (`.nvmrc` pins the version CI uses).
4. Development follows **test-driven development** — see the next
   section; PRs without their tests are not mergeable.
5. New Go packages should ship their own `AGENTS.md` structure notes
   (this file is gitignored — see the repository conventions in
   existing packages).

## What CI runs

A PR runs seven checks; all of them must be green before merge:

| Check | What it does |
|-------|--------------|
| `test` | `go test -race` split into three parallel shards (two balanced package shards + a dedicated quiet shard for `internal/p2psdk`, whose realtime-protocol tests need a runner to themselves) |
| `coverage` | Merges the shard coverage profiles and enforces the 55% floor |
| `lint` | golangci-lint v2 + markdown fence lint (MD040) + docs relative links & zh/en parity (`scripts/check-docs-links.py`) |
| `build` | Frontend tests + build (Node 24), then the Go binary |
| `build-desktop` | Cross-builds the Windows/macOS desktop binaries |
| `gb35114` | Builds and tests the `-tags gb35114` security seam |
| `home-assistant` | Validates the Home Assistant integration packaging |

Docs-only PRs: keep fenced code blocks tagged (a bare ``` fence fails
MD040), keep image paths and relative links resolvable, and keep the
zh/en page sets in parity — `scripts/check-docs-links.py` enforces all
three.

## Vendored code

`internal/p2psdk/` is a vendored snapshot of our client-side SDK, synced
into this tree by the maintainers — see its
[README](internal/p2psdk/README.md) for the API surface (signaling, tunnel,
relay). Don't change it in drive-by PRs — open an issue first so the edit
can be coordinated with the upstream SDK and carried through the next sync.

## Development process — TDD (required)

All development work — new features, bug fixes, refactors — follows
**test-first development**:

1. **Write the failing test first** (red). It should encode the
   issue's acceptance criteria and test the public behavior/contract,
   not implementation internals.
   - **Bug fix**: the test must reproduce the bug and fail on the
     current code. A fix without a reproducing regression test will
     not be merged — the repro test is how we prove the bug existed
     and that the fix actually fixes it.
   - **Refactor**: keep existing tests green throughout; add
     characterization tests before changing uncovered code.
2. **Make it pass** with the minimum implementation (green), then
   refactor while the tests stay green.
3. **The PR includes the tests** written in step 1. CI failing or
   coverage dropping below the floor blocks the merge.
4. New tests must also satisfy the anti-flake rules in the next
   section.

## Writing tests that don't flake in CI (#571)

These rules are distilled from the root causes of past recurring CI
failures. Every new test must satisfy them:

- **One SQLite instance per test.** Create the DB under `t.TempDir()`
  (see the `newTestEnv` helper in `internal/cleanup/cleanup_test.go`).
  Never share one `*storage.DB` across parallel tests — WAL writer
  contention is what produced the historical `database is locked`
  failures.
- **Assert on observable state, never on elapsed time.** For async
  work, poll the observable end state with `require.Eventually`
  (generous timeout, short interval) instead of `time.Sleep` followed
  by an assertion. The #559 storage-root migration flake was exactly
  this: asserting "done" before the job had been observed.
- **Race-clean or don't ship.** `go test -race ./internal/<pkg>/`
  must pass locally before you push. Shared package-level state in
  tests must be atomic or guarded.
- **Backdate fixtures via SQL, and build timestamps in UTC.** When a
  test needs "a record completed 2 hours ago", insert the row and then
  `UPDATE ... SET completed_at = ?` with a UTC timestamp — never depend
  on the test running fast. All DB times are stored UTC; comparing
  them against a local-time `time.Now()` is a bug (see the
  `ListDarkRecordings` timezone bug caught while writing these rules).
- **Keep fixtures lightweight.** Prefer hermetic stubs (fake shell
  scripts, `httptest`, UDP loopback) over real external processes so
  CI wall-time stays flat as the suite grows.
- **Never real hosts in fixtures.** Test vectors, sample payloads,
  example configs, and comments must not contain real hostnames,
  public IPs, or deployment endpoints — anything resolvable on the
  public internet or tied to a real deployment is deployment
  information and does not belong in the repository. Use reserved
  values only:
  - Domains: `example.com` / `example.invalid` (RFC 2606)
  - IPv4: the documentation ranges `192.0.2.0/24`, `198.51.100.0/24`,
    `203.0.113.0/24` (RFC 5737); IPv6: `2001:db8::/32`
  - `127.0.0.1` and RFC 1918 private addresses are fine for
    local-server fixtures; when a vector mimics a captured wire
    payload, prefer the documentation ranges.
  Reviewers will ask for captured-payload vectors to be rewritten with
  reserved values before merge.

## Commit / PR conventions

- Branch from `main`; open a PR (squash-merge only, linear history).
- Subject line: `type(scope): summary` — matches the existing history
  (`feat(...)`, `fix(...)`, `docs(...)`, `refactor(...)`).
- Reference the issue number in the PR body (`Closes #N`).
- CI must be green and the branch up to date with `main` before merge.
