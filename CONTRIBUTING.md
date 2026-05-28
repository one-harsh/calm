# Contributing to CALM

CALM is licensed under [Apache License 2.0](LICENSE). All contributions are accepted under the same license.

## Developer Certificate of Origin

Every commit must carry a `Signed-off-by` trailer attesting that you wrote the contribution or have the right to submit it under the project's license — see the [DCO](https://developercertificate.org/).

```
git commit -s
```

This appends `Signed-off-by: Your Name <you@example.com>` to the commit message. The name and email must match the commit author. DCO enforcement is active in CI — `task ci` runs `task dco:check` as its first step, which verifies every commit in the PR (or push) range carries the trailer and fails the build otherwise.

## AI-assisted contributions

AI tools (Copilot, Claude Code, Cursor, etc.) are acceptable for drafting and editing contributions. The DCO sign-off means the same thing regardless of how the code was produced: you reviewed the change, you take responsibility for it, and you believe you have the right to submit it under Apache 2.0. Raw LLM output that wasn't reviewed doesn't clear that bar.

The practical risk to watch is **license contamination** — models can reproduce training data verbatim, and that data may carry an incompatible license. If a generated chunk is non-trivial AND copy-paste-shaped (a distinctive helper, a recognizable algorithm), call it out in the PR so the reviewer can sanity-check provenance. When in doubt, paraphrase or rewrite before committing.

## Code conventions

- Follow [`CLAUDE.md`](CLAUDE.md) — it is the development directive for this project.
- New Go files carry a two-line copyright + SPDX header, applied automatically by `task fmt`:
  ```go
  // Copyright <year> The CALM Authors
  // SPDX-License-Identifier: Apache-2.0
  ```
- Don't edit generated files (`*.gen.go`, `mock_*.go`). Edit the source spec / interface and regenerate via `task gen:api` or `task gen:mocks`.
- Run `task ci` locally before opening a PR.

## Dependencies

CALM avoids AGPL-licensed dependencies as a deliberate compliance choice (see [HLD §7](docs/HLD.md) — `pg_textsearch` over `pg_search` for the same reason). Surface any AGPL-licensed or unclear-license candidate dep in the PR for discussion before adding.

## Reporting issues

Use GitHub issues. For security-relevant reports, do not file a public issue — contact the maintainer directly.
