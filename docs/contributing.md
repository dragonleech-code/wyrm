# Contributing

Thanks for taking an interest! This is a small, focused tool — issues and PRs
are welcome.

## Development setup

Requirements: the Go version in `go.mod` (currently 1.25) or newer, tmux 3.1+
(for integration tests; 3.1 is the floor for `split-window -l N%`), and
optionally [golangci-lint](https://golangci-lint.run/).

```sh
git clone https://github.com/jskoll/wyrm && cd wyrm
make build       # ./wyrm
make test        # unit + integration
make test-unit   # unit only (no tmux needed)
make lint
```

The integration tests run a real tmux server on an isolated socket
(`tmux -L wyrm-it-<pid>`) — they never touch your own sessions.

## Layout

```
main.go            subcommand dispatch, error/exit-code policy, shared helpers
cmd_session.go     up, restart, kill, attach-by-name
cmd_config.go      edit, validate, save, migrate-config, list-configs, init
cmd_ui.go          pick, tui, list
cmd_trust.go       allow, deny, and the trust prompt every verb that runs a config goes through
cmd_*.go           the rest: diff/wait, agent, worktree, workspaces (@name), remote (-host), ...
internal/config/   TOML types, parsing, validation, extends, settings, project discovery
internal/trust/    which project configs the user has agreed to run (trust.toml)
internal/wait/     readiness checks behind wait_for and `wyrm wait`
internal/tmux/     Runner interface, real exec, ssh, and dry-run implementations
internal/session/  session creation/teardown/reconcile/diff (tested against a mock Runner)
internal/sessions/ listing/killing/matching running sessions — no rendering
internal/freeze/   the reverse of session: a live tmux layout -> a config
internal/agent/    classifying what an AI agent in a pane is doing
internal/editor/   resolving $EDITOR, shared by `wyrm edit` and the TUI
internal/tui/      the Bubble Tea session manager (wyrm tui and wyrm pick)
                   panels.go is the table describing each panel — add a panel
                   there rather than in a switch
```

Each verb in `cmd_*.go` returns an `error`; `app.report` in `main.go` is the one
place that turns it into a message and an exit status. Return `usageErrf` for a
mistake in how the command was typed (exit 2) and a plain error for a failure
(exit 1).

Everything that talks to tmux goes through `tmux.Runner`, so it can be driven
by a recording mock in tests. A Runner may additionally implement
`tmux.BatchRunner` to issue several commands in one process; callers reach that
through `tmux.RunEach` / `tmux.RunOutputs`, which fall back to one call
each, so a mock only ever needs `Run`. Note that *embedding* `tmux.Exec` in a
test double promotes its `RunBatch` — hold it in a named field if the double is
meant not to batch.

`main.run` takes its stdio, runner, and attach function as parameters for the
same reason — the whole CLI is testable without a tmux server.

New behavior should come with a unit test against the mocked `tmux.Runner`
(assert the exact command sequence) and, where it changes real tmux
interaction, an integration test assertion.

## Error-handling contract

- **Structural failures** — can't parse the config, create the session, or
  create a window — return an error; the CLI exits non-zero.
- **Per-pane failures** — a split or command that fails — print a warning to
  stderr and continue, so one broken command doesn't abort the rest of the
  layout.

Keep changes consistent with this split.

## Commits

Conventional commits, please: `feat: ...`, `fix: ...`, `docs: ...`,
`test: ...`, `chore: ...`. Keep PRs focused on one change.

## Docs site

The published docs under `docs/` are built with [mkdocs-material](https://squidfunk.github.io/mkdocs-material/):

```sh
pip install -r requirements-docs.txt
make docs-serve   # live preview at http://127.0.0.1:8000
make docs-build   # build the static site into site/
```
