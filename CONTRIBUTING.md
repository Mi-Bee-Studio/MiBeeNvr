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

## Commit / PR conventions

- Branch from `main`; open a PR (squash-merge only, linear history).
- Subject line: `type(scope): summary` — matches the existing history
  (`feat(...)`, `fix(...)`, `docs(...)`, `refactor(...)`).
- Reference the issue number in the PR body (`Closes #N`).
- CI must be green and the branch up to date with `main` before merge.
