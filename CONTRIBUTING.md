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
   `@ts-ignore`.
4. Add tests for new features; bug fixes should include a regression
   test where practical.
5. New Go packages should ship their own `AGENTS.md` structure notes
   (this file is gitignored — see the repository conventions in
   existing packages).

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

## Commit / PR conventions

- Branch from `main`; open a PR (squash-merge only, linear history).
- Subject line: `type(scope): summary` — matches the existing history
  (`feat(...)`, `fix(...)`, `docs(...)`, `refactor(...)`).
- Reference the issue number in the PR body (`Closes #N`).
- CI must be green and the branch up to date with `main` before merge.
