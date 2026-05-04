# Contributing to CALM

CALM is licensed under [Apache License 2.0](LICENSE). All contributions are accepted under the same license.

## Developer Certificate of Origin

Every commit must carry a `Signed-off-by` trailer attesting that you wrote the contribution or have the right to submit it under the project's license — see the [DCO](https://developercertificate.org/).

```
git commit -s
```

This appends `Signed-off-by: Your Name <you@example.com>` to the commit message. The name and email must match the commit author. DCO enforcement runs in CI once the repository is public.

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
