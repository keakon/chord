# Contributing to Chord

Thanks for your interest in contributing!

This repository is intended for open-source contributors. Development can be done with standard Go tooling.

## Prerequisites

- Go 1.27.0+
- Go quality tools:

```bash
go install golang.org/x/tools/cmd/goimports@latest
go install honnef.co/go/tools/cmd/staticcheck@latest
```

`make gopls-check` installs its own pinned gopls, so no manual gopls install is required for the gates.

## Build

```bash
go build ./...
make build
```

`go build ./...` checks that every package compiles; `make build` writes `./chord` with cgo disabled, `-trimpath`, and stripped symbols.

## Homebrew releases

Pushing a stable `vX.Y.Z` tag builds and publishes the release archives. After the
release job succeeds, `homebrew.yml` generates the formula from
`scripts/homebrew/chord.rb.erb` and the release's `SHA256SUMS`. It installs and tests
the candidate on macOS and Linux, on both ARM64 and x86_64, before committing it
directly to `keakon/homebrew-tap`'s `main` branch.

Set `HOMEBREW_TAP_TOKEN` in this repository to a fine-grained token with Contents
read/write access only to `keakon/homebrew-tap`. Pull requests permission is not
needed. The tap's branch rules must permit this identity to push to `main`.
Only the final publishing job uses this token.

To retry a tap update without rebuilding the release:

```bash
gh workflow run homebrew.yml --repo keakon/chord --ref main -f tag=v0.8.2
```

Replace the tag with the published stable version. Drafts, prereleases, older
releases, and versions already installed in the tap are skipped. Validation
failures leave the tap unchanged. A concurrent push fails without overwriting
remote changes; rerun the workflow to validate and publish against the new state.

Changes to formula installation or tests belong in the template and the tap's
current formula. Verify the generator and publishing behavior with:

```bash
ruby scripts/test_homebrew.rb
```

## Git Hooks Setup (Required)

After cloning the repository, enable commit hooks:

```bash
./scripts/setup-git-hooks.sh
```

This configures `core.hooksPath` to use `.githooks/` and ensures:

- `pre-commit` rejects staged paths ignored by the staged repository rules
- `pre-commit` automatically runs `goimports` and `gofmt` on staged `.go` files
- `pre-push` runs `make preflight` (formatting, dependency, vet, staticcheck, gopls, modernize, deadcode, and test gates) before pushing; its test gate goes through `make test-fast`, so it uses a memory-backed temp dir when one is available (see below). Its deadcode gate covers the host target; the other baselines are checked by CI

`pre-commit` refuses to format and re-stage a Go file that also has unstaged changes, because that would sweep hunks belonging to other work into the commit. When you deliberately want to commit only the staged hunks of such a file, pass the override on the command line for that one command:

```bash
CHORD_PARTIAL_STAGE_OK=1 git commit
```

The staged content is still checked: it must already match `goimports` + `gofmt` output. Do not export the variable into your shell — a permanently set override silently skips the re-stage protection for every later commit.

Verify the setup:

```bash
git config --get core.hooksPath  # should output: .githooks
```

**Why is this required?** Without proper hook configuration, code formatting won't be automatically applied, leading to CI failures and inconsistent formatting across commits.

## Run

```bash
go run ./cmd/chord/
```

## Test

Before submitting a PR, run the same entry point CI uses locally:

```bash
make ci
# or, for the test/coverage job only:
scripts/check_ci_local.sh
```

The Makefile targets mirror CI and should be preferred over copying individual commands by hand. Useful focused targets:

```bash
make preflight     # Same Go gates as the pre-push hook; docs checks depend on changed paths.
make fmt-check
make deps-check
make test-cover    # CI requires total coverage >= 80.0%.
make race-fast     # Race gate with a memory-backed temp dir (local default).
make race          # Same race gate with the default temp dir (CI parity).
make stress        # Full lifecycle repetition and resource-leak checks.
make vet
make staticcheck
make gopls-check
make modernize-check
make deadcode-check
make docs-check
make docs-examples-check
```

### Speeding up local test runs

The suite spends most of its wall time in durable persistence: every state write
fsyncs, which costs milliseconds on a real disk and does not scale with
parallelism. Running its temp dir on a memory-backed filesystem cuts that
latency while keeping the same code paths and the same durability checks
executing. `internal/agent`, the slowest package, drops from ~70s to ~20s.

`make test-fast` uses such a filesystem when one is available and otherwise
falls back to the default temp dir:

```bash
make test-fast
make test-fast PKGS=./internal/agent
```

`scripts/test_tmpdir.sh` resolves the directory — first writable one wins:

1. `CHORD_TEST_TMPDIR`, for example `export CHORD_TEST_TMPDIR=/dev/shm`
2. `.chord/test-tmpdir`, an untracked repo-local file whose first non-comment line is the path
3. the platform default: `/dev/shm` on Linux, or a reused-or-created APFS RAM disk on macOS

Size it for Go's build scratch, not only the tests. `go test ./...` keeps its
work directory (`$TMPDIR/go-build*`, including each test binary) on the same
filesystem; with a warm Go build cache that peaks a few hundred MiB, but on a
cold cache the same run writes every compiled package archive into that
directory and can exceed 1 GiB. The tests' own temporary files are negligible.
The helper creates a 4 GiB RAM disk on macOS. Its backing memory grows as blocks
are written; deleting files frees filesystem space but does not necessarily
return that memory to the system. Repeated writes and deletes can therefore
leave `diskimages-helper` using much more memory than the remaining files.
A small disk fails with `no space left on device` while building.

On Unix, `make test-fast` and `make race-fast` create a separate
`chord-test-runs/run-<id>/work` directory for each command. The runner sets
`TMPDIR`, `TMP`, `TEMP`, and `GOTMPDIR` to that directory for the command and its
children; the Go build cache stays separate. Concurrent runs keep their own
directories. The runner executable is built for the host and cached under
`GOCACHE/chord-test-runner` before the test workspace is created.
Each build ID has its own executable path, so concurrent worktrees keep using
the supervisor from the same build. The bootstrap compilation uses Go's normal
cleanup; it is not supervised by the runner it is building.

On Unix, project test commands share a build cache at
`~/Library/Caches/chord/build-cache/go-build` on macOS, or
`${XDG_CACHE_HOME:-$HOME/.cache}/chord/build-cache/go-build` on Linux.
This applies to `make test`, `test-fast`, `race`, `race-fast`, `test-cover`,
`stress`, and `bench-tui`, including their directly invoked scripts.
They append `-trimpath` to the existing `GOFLAGS`, so identical sources and
compiler settings reuse compiled packages across worktrees. Race and coverage
builds still have separate artifacts. The first run warms this cache; raw Go
commands and editors continue using their configured cache.

The runner registers cache use before launching the command. Once all users
have finished, it scans at most once an hour and removes ordinary cache files
larger than 10 MiB whose recorded last use is over seven hours old. Go updates
that timestamp at most once an hour. Above 30 GiB, the scan also removes smaller
old entries, oldest first, toward a 20 GiB target. Recent entries remain protected,
so these are soft limits. Fuzz corpora, cached executable directories and tool
installations are left alone. The cache is on disk; the RAM disk holds only
per-command scratch files.

Usage records survive a killed runner. Its supervisor releases them only after
proving command cleanup. An unproven cleanup retains its record under
`chord/build-cache/users`, blocks automatic cache eviction, and leaves later
builds usable. The runner reports its retained record's path when it exits.
A skipped trim can also mean another command is still running.
Investigate the retained run before removing its usage record; an exited runner
alone does not prove its descendants have stopped.
Do not point unrelated commands at this managed cache: they would bypass the
usage registry. The wrapper overrides `GOCACHE` and disables `GOCACHEPROG` for
its command tree. Existing default-cache data is left untouched.

The runner removes the directory after the supervised process group exits and
its inherited output pipes close. This also covers test failures, panics,
timeouts, and cancellation with Ctrl-C or SIGTERM. A separate supervisor
continues teardown if the runner is killed. The next run using the same base
directory recovers leftovers only when their locks are available and their
matching cleanup records prove completion. Missing evidence, open output
pipes, and deletion failures leave the directory in place with a diagnostic.
Failure to remove an old run leaves the new command free to run. A failure to
clean up the current command makes an otherwise successful command fail.

Tests must still use `t.TempDir()` and close resources through `t.Cleanup()`.
Processes that detach into another session must have their own shutdown
protocol: a process that also closes inherited output pipes cannot be tracked
through the supervised group or those pipes. Files written outside the test
workspace are the test's responsibility. Direct Go commands and non-Unix hosts
do not use this supervisor; interruption can skip their test cleanup.

The shared RAM disk stays mounted between runs. Files outside the runner's
managed directories are left alone; remove them only after confirming that
their users have exited. To release the macOS disk's backing
memory as well, stop all users of the disk and run
`scripts/test_tmpdir.sh --destroy`; the next `make test-fast` or `make race-fast`
creates it again. The ownership check verifies which disk the helper created,
not whether other processes still need it.

To create one yourself:

- **Linux**: `/dev/shm` is already a tmpfs, so `TMPDIR=/dev/shm` works — provided it allows executing files (Go runs the test binary from there) and has room; containers mount it `noexec` and only 64 MiB, so check with `findmnt /dev/shm` first. A dedicated tmpfs (`sudo mount -t tmpfs -o size=2g,exec tmpfs /mnt/ram`) is equivalent.
- **macOS**: `scripts/test_tmpdir.sh` reuses `/Volumes/ChordTestRam` whenever it is writable and allows exec, and creates a new APFS RAM disk only when no usable mount is present. Both the Chord and chord-gateway helpers use the same owner record under `~/Library/Caches/chord-test-tmpdir`; the record stores the mounted device, volume UUID, parent disk, and attached physical store. `--destroy` ejects that physical store only when the mount still matches the first three fields and the store still backs the parent. A mount without a record is reused but left in place by `--destroy`. When the disk just attached cannot be confirmed as the one at `/Volumes/ChordTestRam`, the helper leaves that mount untouched — another process may already use it — and switches to a private `/Volumes/ChordTestRam-<pid>-<random>` disk instead. `make test-fast` and `make race-fast` release that private disk after cleaning their test directory. An unproven cleanup or a killed runner can leave it mounted; release it with `scripts/test_tmpdir.sh --release <dir>` after its users have exited. This command never ejects the shared mount. It ejects the disk it attached when the mount provably does not use it, and otherwise leaves that disk attached and names the `diskutil eject` command in the warning. When only the owner record cannot be written, the shared mount stays in use and `--destroy` refuses it; that warning also names the `diskutil eject` command. To create a disk by hand:

  ```bash
  dev=$(hdiutil attach -nomount ram://8388608 | awk '{print $1}')
  diskutil erasevolume APFS ChordTestRam "$dev"   # APFS, not HFS+
  ```

  Use APFS: HFS+ timestamps have one-second granularity, which breaks tests that detect an external edit by mtime. A manually created disk has no owner record, so keep `$dev` and eject it with `diskutil eject "$dev"`; `scripts/test_tmpdir.sh --destroy` refuses to eject unverified mounts.

Run `scripts/test_tmpdir_ownership.sh` after changing the helper's ownership behavior.

- **Windows**: there is no built-in memory filesystem, and Go reads `TMP`/`TEMP`, not `TMPDIR`. Either install a RAM-disk tool and point `TMP`/`TEMP` at it, or point them at a fast SSD. `make test-fast` exports `TMPDIR`, `TMP`, and `TEMP` together, so the same path works under Git Bash.

Keep `TMPDIR`, `TMP`, and `TEMP` scoped to test commands. Exporting them for the
shell that launches Chord also puts Chord's own temporary files and its
subprocesses' files on the test disk. `make test-fast` and `make race-fast` set
these variables for the test command only.

Any other test entry point can reuse the directory by prefixing the variable, for example:

```bash
TMPDIR=$(scripts/test_tmpdir.sh) make test-cover
```

`make gopls-check` installs the pinned `GOPLS_VERSION` once into the Go build cache, builds it for the host platform, and selects production, cgo, internal-test, and external-test files with `go list` for the current `GOOS`/`GOARCH`. Update `GOPLS_VERSION` in the Makefile deliberately when adopting a newer analyzer release; do not replace the pinned version with `@latest`, because new releases may add checks and fail the existing baseline. The gate fails on diagnostics even when gopls exits successfully, as well as on package-loading or tool failures. CI checks linux/amd64, darwin/arm64, and windows/amd64; other operating systems are outside this gate. To check another supported target locally, run `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 make gopls-check`. Python 3 is required to read the package metadata. Run `scripts/test_gopls_check.sh` after changing the gate.

`make modernize-check` runs the pinned `golang.org/x/tools/go/analysis/passes/modernize` command across production and test code. Update `MODERNIZE_VERSION` in the Makefile deliberately when adopting a newer analyzer release; do not replace the pinned version with `@latest`, because new releases may add checks and fail the existing baseline.

`make deadcode-check` compares the symbols that are unreachable from the production entry points against the baselines in `scripts/deadcode-baseline/`, one file per analyzed `GOOS/GOARCH`. CI checks every baseline — linux/amd64, windows/amd64, and darwin/arm64 — by pointing `GOOS`/`GOARCH` at each target from one runner; a local run analyzes the host platform unless the environment selects another. Reachability is a whole-program property, so the vet, staticcheck, and gopls gates stay silent on a symbol that only test code still references; `pre-push` runs this check for the host target, and `GOOS=windows GOARCH=amd64 make deadcode-check` covers another target. A new unreachable symbol fails the gate: remove it, or add it to the matching baseline in a dedicated commit with the platform and a reason. Removing symbols shrinks the baseline, so cleanup commits carry their own baseline updates.

`make race-fast` is the local default. It delegates to
`scripts/check_ci_race.sh`, the same entry point used by GitHub Actions, and
uses the same isolated temporary directory and cleanup as `make test-fast`.
In addition to `go test -race`, that script rejects unexpected stderr and leaked
runtime or interactive output. You can pass a focused package set through
`PKGS`, for example:

```bash
make race-fast PKGS=./internal/tui
```

Use `make race` instead when you need to reproduce CI's default temporary
directory (or when a memory-backed temp dir is not desired). It runs the same
script without overriding the temp dir.

If your changes touch TUI performance-critical paths, also run the checks in [Performance-sensitive changes](#performance-sensitive-changes).

The ordinary takeover tests cover successful saves, retryable failures and resource
release with a small number of repetitions. `make stress` runs all 200 takeover
cycles, 100 listener start/stop cycles, and the high-volume concurrent recovery
writes under the race detector, using `scripts/check_ci_stress.sh` and the ordinary
race gate's output checks.
Both `make ci` and GitHub Actions include the full stress workload, with Actions
running it in a separate job. `CHORD_TEST_STRESS=1` also enables the full
workload when invoking the selected tests directly.

The LSP package runs real gopls round trips when `gopls` is installed. For a
focused check without starting language servers, use `go test -short ./internal/lsp`.
The ordinary test and CI commands keep these checks enabled; short mode is not
a substitute for complete verification before submitting changes.

The Pyright and upstream model catalog integration tests require explicit opt-in.
To run the real Pyright LSP integration test locally, install `pyright-langserver` and run:

```bash
CHORD_RUN_REAL_PYRIGHT_TESTS=1 go test ./internal/tools -run Pyright
```

To compare the embedded catalog with its pinned upstream tag, run the optional
model catalog integration test:

```bash
CHORD_RUN_MODELCATALOG_INTEGRATION_TESTS=1 go test ./internal/modelcatalog/refresh -run '^TestPinnedCatalogRepository$' -count=1
```

This clones the repository and tag recorded in the embedded snapshot into a
temporary directory. To use a local checkout, set
`CHORD_MODELCATALOG_TEST_CONFIG` to a YAML file containing `local_path`. Relative
paths resolve against that file's directory. An empty or absent local path
uses the recorded upstream; a nonexistent checkout also falls back to cloning
upstream. The test never modifies the configured checkout. A supplied config
file must exist and contain valid keys. Default unit tests create temporary
fixture repositories and require neither this config nor network access.
An existing checkout must contain the pinned tag; a missing tag fails rather
than silently falling back to network access. GitHub Actions provides the
manual **Model catalog integration** workflow for the upstream clone path;
normal push and pull request CI uses only the offline fixture tests.

```yaml
local_path: ./catalog-checkout
```

Pre-push checks a local chord-models checkout when one is present and prints a
non-blocking reminder if it has a newer catalog tag than the embedded snapshot.
The default location is a sibling `../chord-models`; set `CHORD_MODELS_DIR` for
another path. A missing or unrelated checkout is ignored. The check only sees
tags already fetched in that checkout, so fetch tags in chord-models to
discover newer releases. Run `make model-catalog-check` at any time, or set
`CHORD_SKIP_MODEL_CATALOG_CHECK=1` to silence the reminder.

## Performance-sensitive changes

LLM stream replay uses an explicit corpus directory. Point it at a session's
`dumps/llm` directory or a directory containing selected LLM dump JSON files:

```bash
CHORD_SSE_BENCH_DUMPS=/absolute/path/to/dumps/llm \
  bash scripts/run_tests.sh go test ./internal/llm -run '^$' \
  -bench '^BenchmarkSSEParseReplay$' -benchmem -benchtime=100ms -count=3
```

Without `CHORD_SSE_BENCH_DUMPS`, replay is skipped and fixed-fixture benchmarks
remain available. An explicit corpus that is missing, empty, malformed, or
cannot replay fails the benchmark. Keep private dump contents out of commits.

`./scripts/bench_tui_regression.sh` is the canonical validation entry point for TUI hot paths — it combines correctness tests, alloc guards, and the stable micro-benchmark set, and accepts two result files for a `benchstat` comparison:

```bash
./scripts/bench_tui_regression.sh
./scripts/bench_tui_regression.sh ./old.txt ./new.txt   # benchstat compare
```

For a quick focused loop while iterating on streaming or rendering, run the benchmarks closest to the path you are changing, for example:

```bash
go test ./internal/tui -run '^$' -bench 'BenchmarkStream.*|BenchmarkRender.*|BenchmarkModelViewCached' -benchmem -benchtime=1x
```

To make sure every TUI benchmark case completes without turning the check into a multi-minute stability run, use a single-iteration scan:

```bash
go test ./internal/tui -run '^$' -bench=Benchmark -benchmem -benchtime=1x -count=1
```

Do not use bare `go test ./internal/tui -bench=Benchmark -benchmem` as a routine check. Go will run the package tests first and then adapt every benchmark case toward roughly one second. This package also contains cold-render, spill-I/O, paced/sleep, and end-to-end benchmarks, so the unbounded command can take several minutes even when nothing is stuck.

For a broader but bounded comparison, start with:

```bash
CHORD_BENCH_FULL=1 CHORD_BENCH_TIME=100ms ./scripts/bench_tui_regression.sh
```

Raise `CHORD_BENCH_TIME` to `1s` or more only for release checks, benchstat-quality comparisons, or when the shorter run is too noisy. Keep benchmarks with real sleeps, I/O, large fixture construction, or expensive setup outside the default stable micro-benchmark set; the default smoke path should remain comfortably below one minute. The regression script keeps benchmarks whose per-round setup runs with the timer stopped in a separate paced group that always uses a fixed iteration count, so raising `CHORD_BENCH_TIME` never inflates their wall-clock cost; add new benchmarks of that shape to `PACED_BENCH_PATTERN` in the script rather than to the stable sets.

Growing-card and large-card window benchmarks also run once per case,
independently of `CHORD_BENCH_TIME`. Smoke covers 64 streaming paragraphs and
1,024-paragraph cards; full mode includes the 256-paragraph streaming and
8,192-paragraph card cases.

When benchmark results do not explain real CPU usage, collect a profile during the problematic interaction:

```bash
CHORD_PPROF_PORT=6060 chord
go tool pprof http://127.0.0.1:6060/debug/pprof/profile?seconds=15
```

Interpreting common hotspots:

- `bubbletea.(*Program).render`, terminal writes, or renderer cell output: redraws are too frequent, or stream events are too fragmented.
- Markdown / glamour / goldmark functions during active streaming: the cheap streaming path is being bypassed.
- Viewport measurement, line wrapping, or ANSI width functions: visible-window or line-count caches may be invalidating too often.
- Animation or status tick handlers while nothing changes: stale or duplicate tick chains may be running.

Tuning trade-offs to keep in mind: larger stream micro-batch windows reduce CPU but make text feel less immediate; longer flush cadence reduces redraws but makes streamed output chunkier; more aggressive cache retention improves redraw speed but increases memory, while more aggressive eviction does the opposite. Prefer the smallest change that fixes the measured bottleneck, and keep benchmarks close to the path being optimized.

## Where new files live

Use these rules when adding a new Chord file or directory:

1. Put it in the project root or `.chord/` when users should read or edit it, repository-relative links matter, or it should move with the project. State its Git policy explicitly; a hidden path is not automatically private.
2. Put it in the config home when it is user-owned configuration that applies across projects. Credentials belong here, never in a project directory.
3. Put it in the state directory when Chord owns durable machine state such as history, bookkeeping, locks, checkpoints, or registries. Losing state may lose history or require recovery work.
4. Put it in the cache directory only when deleting it cannot lose user-authored content or durable history and Chord can rebuild it from another source.

Some features need a split layout. Keep portable, human-readable project content in the project tree, but keep high-frequency bookkeeping, locks, and rebuildable indexes in state or cache. Do not duplicate the same authoritative content across layers; each file needs one clear source of truth.

## Scope & style

- Keep PRs small and focused
- Avoid unrelated refactors in the same PR
- When changing behavior, add or update unit/regression tests.
- If tests are impractical for a change, provide clear minimal verification steps in the PR.
- When changing user-facing behavior, CLI flags, configuration, workflows, or documented development commands, update the relevant docs.

## Documentation

- User-facing docs live under `docs/` as bilingual markdown (`*.md` for English, `*_CN.md` for Chinese). They remain readable on GitHub directly.
- A Starlight (Astro) site under `website/` renders the same markdown into the public docs site. Source of truth stays in `docs/`; the site is generated by `scripts/sync-docs.mjs`.

To preview doc changes locally:

```bash
cd website
npm install        # first time only
npm run dev        # http://localhost:4321/chord/
```

To run the docs workflow link check locally, install `lychee` and use the same
scan set as GitHub Actions:

```bash
brew install lychee  # macOS, first time only
lychee --no-progress --cache --max-cache-age 30d --max-concurrency 16 --accept 200,206,429 docs/ README.md README_CN.md CONTRIBUTING.md SECURITY.md CODE_OF_CONDUCT.md
```

`make docs-check` verifies docs consistency; it does not run the `lychee` link checker.

`npm run dev` syncs the source docs once at startup. After editing `docs/`, run `npm run sync` in another terminal under `website/`, or restart the dev server. Do **not** edit the generated `.md` files under `website/src/content/docs/` or its `zh/` directory; edit their sources in `docs/`. The two `index.mdx` landing pages are maintained by hand.

When you add a new top-level page under `docs/`, also list it in:

- `docs/index.md` and `docs/index_CN.md` (the per-section navigation),
- `website/astro.config.mjs` (the Starlight sidebar) — add an entry under the right group with both English label and `zh-CN` translation.

The docs site is automatically built and deployed by `.github/workflows/docs.yml` whenever `docs/`, `website/`, the docs sync/check scripts, or the README / CONTRIBUTING files change on `main`.

## Security

If you discover a security issue, please open a [GitHub security advisory](https://github.com/keakon/chord/security/advisories/new) with reproduction steps and impact details.
