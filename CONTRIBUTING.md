# Contributing to Chord

Thanks for your interest in contributing!

This repository is intended for open-source contributors. Development can be done with standard Go tooling.

## Prerequisites

- Go 1.26+

## Build

```bash
go build ./...
```

Enable commit hooks once:

```bash
./scripts/setup-git-hooks.sh
```

This installs `.githooks/pre-commit`, which runs `gofmt` on staged `.go` files before each commit.

## Run

```bash
go run ./cmd/chord/
```

## Test

Before submitting a PR, please run:

```bash
go test -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
# CI requires total coverage >= 65.0%.
go vet ./...
staticcheck -checks 'all,-ST*' ./...
```

If your changes touch TUI performance-critical paths, also run:

```bash
./scripts/bench_tui_regression.sh
```

Optional integration tests that require external tools are not part of the default CI run. To run the real Pyright LSP integration test locally, install `pyright-langserver` and run:

```bash
CHORD_RUN_REAL_PYRIGHT_TESTS=1 go test ./internal/tools -run Pyright
```

## Scope & style

- Keep PRs small and focused
- Avoid unrelated refactors in the same PR
- When changing behavior, add or update unit/regression tests.
- If tests are impractical for a change, provide clear minimal verification steps in the PR.
- When changing user-facing behavior, CLI flags, configuration, workflows, or documented development commands, update the relevant docs.

## Documentation

- User-facing docs live under `docs/`
- Internal maintainer docs are not published

## Security

If you discover a security issue, please open a [GitHub security advisory](https://github.com/keakon/chord/security/advisories/new) with reproduction steps and impact details.
