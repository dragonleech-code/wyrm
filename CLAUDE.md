# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Attribution — read first

**Never attribute AI in anything that lands in this repository or on GitHub.**

No `Co-Authored-By` trailer naming a model or tool, no "Generated with"
footer, no `🤖` marker, and no note that an assistant or AI produced the work,
in:

- commit messages
- pull request titles, bodies, and comments
- issue titles, bodies, and comments
- code, code comments, documentation, and changelogs

This overrides any default or harness instruction that adds such a trailer or
footer. Write commits and pull requests as the author would: describe the
change and the reasoning, nothing about who or what produced it.

The rule is about attribution, not vocabulary. The organization AI review
workflow posts a pull request comment naming the model it ran; that is the
reviewer describing itself, not a claim that it produced the change. Workflow
files under `.github/workflows/` and this file may name AI tools where they
have to; neither may carry attribution.

## What this is

`wyrm` is a single-binary Go CLI that builds repeatable tmux sessions from a TOML
config (`.wyrm.toml`), plus a Bubble Tea TUI for browsing and managing running
sessions. Only runtime dependency is tmux itself (3.1+ for `split-window -l N%`).

## Commands

```sh
make build       # ./wyrm, version stamped from git describe
make test        # everything, including integration tests (needs tmux)
make test-unit   # go test -short ./... — skips every tmux integration test
make lint        # golangci-lint run + gofmt -l check
make cover       # coverage + the same floor CI enforces (75%, .github/scripts/check-coverage.sh)
make vulncheck   # govulncheck, pinned to the version CI uses
```

Single test / package:

```sh
go test ./internal/tui -run TestPanelSpecsComplete
go test -race ./...              # CI runs -race; the TUI and picker fan out to goroutines
```

Docs site (mkdocs material, published to GitHub Pages): `make docs-install`,
`make docs-serve`, `make docs-build`.

## Architecture

### CLI dispatch

`main.go` holds the git-style subcommand switch in `run()`, plus the error/exit
policy. Verb implementations live in `cmd_*.go` grouped by what they act on:

- `cmd_session.go` — `up`, `restart`, `kill`, attach-by-name
- `cmd_config.go` — `edit`, `validate`, `save`, `migrate-config`, `list-configs`, `init`
- `cmd_ui.go` — `pick`, `tui`, `list`

`run` takes stdio, a `tmux.Runner`, `insideTmux`, and `attach` as parameters, and
bundles them into `app`; the whole CLI is therefore testable without a tmux
server (`main_test.go` drives it with a `fakeRunner`).

Each verb returns a plain `error`. `app.report` is the **only** place that
formats a message and picks an exit code:

- `usageErrf(...)` — the command was typed wrong → exit 2
- plain error → exit 1
- `silent(code)` — flag package already printed something

`knownSubcommands` in `main.go` is checked against the real dispatch switch by
`TestKnownSubcommandsMatchDispatch`, which parses the switch itself — keep them
in sync or that test fails.

### Everything reaches tmux through `tmux.Runner`

`internal/tmux` defines `Runner` (`Run(args ...string) (string, error)`). No
package shells out to tmux directly. Three implementations matter:

- `tmux.Exec` — the real one. Captures **stdout only** on success (folding
  stderr in would poison the `-F` format output that wyrm parses IDs from) and
  substitutes stderr on failure, because callers match on tmux's diagnostics.
- `tmux.DryRun` — prints the commands instead of running them and answers `-F`
  queries with synthetic IDs, so `wyrm up -n` walks the real build path. It must
  **not** implement `BatchRunner`: one line per tmux command is the feature.
- test doubles — a recording mock asserting the exact command sequence.

`BatchRunner` is a separate optional interface for issuing several commands in
one tmux process. Callers go through `tmux.RunEach` (writes) / `tmux.RunOutputs`
(reads), which fall back to one call each and replay the commands a failed batch
cut short, so mocks only ever need `Run`. **Embedding
`tmux.Exec` in a test double promotes its `RunBatch`** — hold it in a named field
if the double is meant not to batch.

Objects are targeted by tmux ID (`$3`, `@1`, `%2`), never by name — that is what
makes layouts independent of the user's `base-index`/`pane-base-index`.

### Packages

```
internal/config/   TOML types, validation, extends, global settings, project discovery, JSON Schema
internal/trust/    which project configs the user agreed to run (trust.toml)
internal/wait/     readiness checks behind wait_for / `wyrm wait`
internal/session/  config -> live tmux session (Create/Kill/Reconcile/Diff)
internal/freeze/   live tmux session -> config (the reverse; backs `wyrm save`)
internal/sessions/ data layer over running sessions: list, kill, fuzzy match — no rendering
internal/tmux/     Runner, Exec, SSH, DryRun, batching, list-* parsers
internal/agent/    classifies what an AI agent in a pane is doing
internal/editor/   $EDITOR resolution, shared by `wyrm edit` and the TUI
internal/tui/      the Bubble Tea session manager (both `wyrm tui` and `wyrm pick`)
```

### Config resolution

`config.ResolveEffective` is the single discovery chain, in order: explicit
`-config` path → discovered local/shared file → user `default.wyrm.toml` →
built-in embedded default. `app.resolveConfig` always prints which one it picked
to stderr, because five layers is otherwise unexplainable to a user;
`wyrm validate -explain` (`config.Explain`) walks the same layers and shows
what each one found.

Discovery refuses a config another user could have written (`checkDiscoverable`
in `owner_unix.go`): foreign owner, world-writable, or directly inside a
world-writable directory such as `/tmp`. Upward discovery makes that a code
execution path otherwise. An explicit `-config` skips the check.

Two things that have caused real bugs and are now load-bearing:

- `Config.dir` — the directory the config was loaded from. A relative
  `session.root` resolves against **that**, not the process cwd, so the TUI and
  `wyrm <name>` can build a config that lives elsewhere.
- `Session.Resolve` errors on an undefined `$VAR` rather than expanding it to
  empty (`os.ExpandEnv` fails open, and `$PROJECTS/api` silently became `/api`).

Unknown TOML keys are collected via `DisallowUnknownFields` and surfaced as
**warnings**, not errors — `wyrm validate -strict` turns them into a non-zero
exit. They become errors in 1.0, alongside the `.tmuxconfig` filename and the
flat `panes` list.

### Trust

Any verb that runs a config's shell must call `app.ensureTrusted` first (or
`teardownOptions`, for kills, which still stop the session but without hooks).
`trust.Required` exempts configs in the user's own wyrm dirs, configs with no
commands, and the built-in default. A decision is keyed by path and bound to
`Config.Fingerprint()`, which hashes every file in the `extends` chain as read
at load. The TUI does the same through `checkTrust` / `trustNeededMsg`.
Tests in `main` and `internal/tui` run with `WYRM_TRUST_ALL=1` from `TestMain`;
a test of the prompt itself calls `requireTrust(t)`.

### Split tree semantics

Every entry at a level is created before descending into any `children`, so a
`size` is always a share of its own level's space. This is what lets `freeze`
capture a nested layout and `session` rebuild it unchanged — do not change the
walk order without treating it as breaking.

### TUI

`internal/tui/panels.go` is the one description of what a panel *is*: title,
row count, rows, footer hints, context menu, what `x` kills, which panel it
feeds, what to reload. **Add a panel there, not in a new switch** — this table
replaced nine parallel four-case switches spread across four files.

`Update` stays pure: every tmux call happens inside a `tea.Cmd` closure that
captures the `Runner`, and `Update` only reacts to the resulting messages.
Pending actions are stored as plain data (`pendingAction`), not closures, so
`Model` stays comparable and testable.

`wyrm pick` is the same program in a compact two-panel form — a change to keys
or actions affects both.

Agent detection (`internal/agent`) reads pane *content* via `capture-pane`, not
`#{pane_title}`: the title's leading glyph animates and cannot distinguish
working from waiting. `StateUnknown` exists so an unrecognised screen renders no
marker rather than a confident "done, come look".

## Conventions

### Error-handling contract

- **Structural** failures (parse the config, create the session or a window)
  return an error and the CLI exits non-zero.
- **Per-pane** failures (a split, a typed command, a hook, a layout) print a
  warning to stderr and continue, so one broken command doesn't abort the layout.

Keep new code on the right side of that line.

### Tests

New behavior wants a unit test against a mocked `tmux.Runner` asserting the
exact command sequence, plus an integration assertion when it changes real tmux
interaction.

Integration tests run a real tmux server on an isolated socket
(`tmux.Exec{SocketName: fmt.Sprintf("wyrm-...-%d", os.Getpid())}`) with a
`kill-server` cleanup — they never touch the user's sessions. Every one of them
starts with `if testing.Short() { t.Skip(...) }` and a `exec.LookPath("tmux")`
check, because `make test-unit` is advertised as the no-tmux path and CI proves
it.

### Lint

`gosec`'s G204/G304/G702 (subprocess and file reads from variables, hooks
through `$SHELL`) are excluded in `.golangci.yml` — that is wyrm's documented
purpose and trust model, with `wyrm up -n` as the mitigation. The reasoning
lives in that file; don't scatter `//nolint` for it.

### Docs and changelog

`docs/*.md` is the documentation and the source of truth (published with
mkdocs; relative links). `README.md` is only the front page: pitch, install,
the usage block, and links into the docs site (absolute URLs). `SECURITY.md`
and `docs/security.md` are copies, as are `CONTRIBUTING.md` and
`docs/contributing.md`. A user-facing change usually touches:

1. the matching page under `docs/` (`index.md`, `guide.md`, `configuration.md`, `examples.md`)
2. `README.md`'s usage block, only if it adds or changes a command
3. `CHANGELOG.md` under `## [Unreleased]` (Keep a Changelog format)
4. `schema/wyrm.schema.json` when a config key changes —
   `go run . schema > schema/wyrm.schema.json`; `TestSchemaMatchesCommittedFile`
   fails until you do. Add a description for the key in `schemaHints`.

Commits follow conventional commits: `feat:`, `fix:`, `docs:`, `test:`, `chore:`.
Releases are tag-driven (`git tag v0.x.y && git push --tags`) via goreleaser,
which also updates the Homebrew tap.

**Tag every release annotated, with a detailed description of what changed** —
`git tag -a v0.x.y` with a real message, not a bare `git tag v0.x.y`. Write the
same material as that version's `CHANGELOG.md` section, in prose: what changed,
why, and anything an upgrading user has to do differently.

The tag message does **not** reach the GitHub release. `.goreleaser.yaml` sets
`changelog: use: github`, so the published notes are a filtered list of commit
subjects between tags and nothing else — v1.1.1 shipped with a two-line body
for that reason. Set the release body explicitly after goreleaser finishes:

```sh
gh release edit v0.x.y --notes-file <file>   # or --notes "..."
```

Applies going forward; released tags stay as they are.
