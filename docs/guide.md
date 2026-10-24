# Guide

Workflows beyond "one project, one config": trusting configs from other
people's repositories, sharing configs, starting groups of projects, worktrees,
remote hosts, keeping a running session in step with its config, and waiting
on dependencies.

## Trusting project configs

A wyrm config runs shell by design — hooks run through your `$SHELL`, and pane
commands are typed into one. That is fine for a config you wrote, and a
problem for one that arrived with somebody else's repository:
`git clone x && cd x && wyrm` would run `x`'s hooks without you having read
them.

So before wyrm runs a project config from outside your own wyrm directories,
it shows you what the config would run and asks:

```console
$ wyrm
wyrm: using config /home/me/src/someone-elses-repo/.wyrm.toml
wyrm: /home/me/src/someone-elses-repo/.wyrm.toml is not trusted yet.
wyrm: this config runs shell commands:
  on_project_start = curl https://example.com/install.sh | sh
  window dev command = npm run dev
Trust this config and run it? (y/N):
```

Answering `y` records the decision, bound to the config's exact contents. Any
edit — to the config, or to a base it [`extends`](configuration.md#extends) —
asks again. A non-interactive run (no terminal on stdin) declines.

Configs that never need a decision:

- anything in wyrm's own config directory (`~/.config/wyrm`), your
  `shared_dir`, or a `[[wildcard]]` template your settings name — you put
  those there
- a config that runs no commands at all
- the built-in default

Managing decisions by hand:

```sh
wyrm allow              # trust the config `wyrm` would use here
wyrm allow path/to/x    # trust a specific config
wyrm allow -list        # every trusted and denied config
wyrm allow -revoke      # forget the decision, so wyrm asks again
wyrm deny               # refuse a config: wyrm won't run it or ask again
```

Decisions live in `~/.config/wyrm/trust.toml` (mode 0600). `wyrm up -n` never
needs trust — it runs nothing. `wyrm kill` on an untrusted config still stops
the session, without running its `on_project_exit`.

For scripts and CI, where there is no one to ask, set `WYRM_TRUST_ALL=1`, or
turn the check off in the settings file:

```toml
# ~/.config/wyrm/config.toml
[trust]
enabled = false
```

Separately from trust, config discovery refuses a `.wyrm.toml` another user
could have written: one owned by someone else, one writable by every user, or
one sitting directly in a world-writable directory like `/tmp`. Discovery walks
up through parent directories, so without this a file planted in `/tmp` would
apply to anyone running `wyrm` beneath it. Pass `-config` to use such a file
deliberately.

## Storing configs in a shared directory

By default wyrm looks for `.wyrm.toml` in the current directory. If you'd
rather keep all your project configs in one place (e.g. to version them
together, or avoid an untracked file in every repo), set `storage = "shared"`
in wyrm's global settings file at `~/.config/wyrm/config.toml`
(`$XDG_CONFIG_HOME/wyrm/config.toml` if set):

```toml
storage = "shared"
# shared_dir = "~/.config/wyrm/settings"  # optional, this is the default
```

In shared mode, running `wyrm` in a directory named `myproject` looks for
`myproject.wyrm.toml` in the shared directory first, falling back to the
usual local search if it isn't there. `wyrm migrate-config` moves the
current directory's local config into the shared directory under the right
name for you. When invoked from a child directory, migration preserves the
discovered config's original project directory. It records `session.project_dir`
as ownership metadata and anchors relative roots before moving the file.

Two projects can share a folder name — `~/work/api` and `~/personal/api`, or
`services/api` and `packages/api` in a monorepo. The first one to claim
`api.wyrm.toml` keeps it; the next gets a distinct file with a short hash of
its path, `api-3f2a1c.wyrm.toml`, and `migrate-config` tells you when that
happens. A shared config belongs to `session.project_dir` when present,
otherwise to its absolute `session.root`.

The storage filename does not rename the session: its name comes from
`session.name` or the resolved root. Set distinct names for same-named roots:

```toml
[session]
name = "api-personal"
root = "~/personal/api"
```

`wyrm validate -explain` prints every place wyrm looked for a config, in
order, and marks the one it chose.

## One config, many directories

Two ways to share a layout between projects:

- [`[[wildcard]]`](configuration.md#wildcard-one-config-many-directories)
  applies one template to every directory matching a glob, with no file in
  any of them.
- [`extends`](configuration.md#extends) lets a project's own config build on
  a shared base, overriding only what differs.

## Starting groups of projects together

A workspace is a named list of projects in the settings file:

```toml
# ~/.config/wyrm/config.toml
[[workspace]]
name = "backend"
projects = ["api", "worker", "db"]
```

`wyrm @backend` starts every one that isn't already running and attaches to
the first (`-d` to stay detached). `wyrm kill @backend` stops them all, running
each one's `on_project_exit`. Members are project names or aliases, exactly as
`wyrm <name>` takes them; a name that matches no project stops the command
before anything starts.

## Cloning a repo into a session

`wyrm clone <repo> [dest]` runs `git clone`, then builds (and attaches to) a
session for the result — the shortcut for "get this repo and start working
in it" in one command instead of `git clone` + `cd` + `wyrm`:

```sh
wyrm clone git@github.com:jskoll/wyrm.git       # clones into ./wyrm, same as git's own default
wyrm clone git@github.com:jskoll/wyrm.git work  # clones into ./work instead
```

A cloned repository is the input the [trust prompt](#trusting-project-configs)
exists for: before starting, wyrm lists every hook and pane command the
config would run and asks. Declining leaves the clone on disk and starts
nothing. `-y` trusts the config up front, and `-no-start` (`-n`) clones
without resolving a config at all.

It needs `git` on `PATH`. If the destination falls under a `[[wildcard]]`
pattern, that template is used; otherwise wyrm resolves the config the same
way a bare `wyrm up` inside the clone would — the repo's own `.wyrm.toml` if
it has one, else your default config.

## Git worktrees

`wyrm worktree BRANCH` creates a git worktree for a branch and starts a
session in it — one session per branch, side by side:

```sh
wyrm worktree feature/login              # new branch from HEAD, or the existing one
wyrm worktree fix-123 -base origin/main  # new branch from a given ref
wyrm worktree spike -path ~/tmp/spike    # put the worktree somewhere specific
```

By default the worktree goes in `<repo>.worktrees/<branch>` beside the
repository (`/` in a branch name becomes `-`). Running it again for the same
branch reuses the worktree.

The session uses the worktree's own committed `.wyrm.toml` if it has one,
otherwise the main checkout's, rooted at the worktree. Either way the session
is named from the repository and the worktree directory — `app-feature-login`
— rather than the config's `session.name`, which would give every worktree of
a repository the same session. That naming applies to any linked worktree, not
just ones `wyrm worktree` made: it reads `.git`'s pointer file directly rather
than shelling out to git.

## Remote sessions

`wyrm up -host devbox` reads the config here and builds the session on
`devbox` over ssh; `wyrm kill -host devbox` tears it down.

```sh
wyrm up -host devbox            # build there, then attach (ssh -t)
wyrm up -host devbox -d         # build without attaching
wyrm up -host devbox -n         # print what would run
wyrm kill -host devbox
```

- `session.root` must be absolute or start with `~/`; `~` means the remote
  home directory. Relative window and split roots resolve against it as usual.
  `$VAR`s expand from your local environment.
- Hooks run on the remote host, in the session's directory, through `sh`.
- `wait_for` calls the remote host's own `wyrm`, so it has to be on the remote
  `PATH`.
- ssh runs with `BatchMode=yes` (key-based auth, no prompts) and one
  multiplexed connection per host, so a build costs one handshake. The control
  socket lives in a directory only you can enter.
- `[tmux].socket` and `[tmux].command` apply on the remote side.

## Keeping a session in step with its config

Edit a config while its session is running and the two drift. `wyrm diff`
says how, window by window:

```console
$ wyrm diff
session api differs from its config:
  + window "logs" is in the config but not running
  - window "scratch" is running but not in the config
  ~ window "code" has 2 pane(s); the config has 3
run `wyrm up -reconcile` to add missing windows, or `wyrm restart` to rebuild
```

It exits 1 when there are differences (like `diff`), and `-format json` gives
the same as data. Windows are matched by name. Pane commands aren't compared:
tmux only knows a pane's current foreground program, which rarely matches the
command line that started it.

`wyrm up -reconcile` adds the windows the config defines that the session
lacks, leaving every existing window — and whatever is running in it — alone.
No lifecycle hooks run; the new windows' `post_window` does. For anything
else (a changed split, a different command), `wyrm restart` rebuilds.

## Waiting for dependencies

A pane whose command needs something else running first — a database, a dev
server, a generated file — can wait for it:

```toml
[[windows.splits]]
command = "psql"
wait_for = "tcp:localhost:5432"

[[windows.splits]]
type = "h"
run = "npm run e2e"
wait_for = "http://localhost:3000/health"
wait_timeout = "5m"
```

The pane opens immediately; only its command waits. `wait_for` takes
`tcp:HOST:PORT`, `file:PATH`, or an `http://`/`https://` URL (ready on any
status below 400). wyrm types `wyrm wait ... && <command>` into the pane, so
the command never starts if the wait times out (default 2 minutes). The same
check is available on its own:

```sh
wyrm wait -timeout 30s tcp:localhost:5432 file:/tmp/build.done
```

## Agents waiting on you

The [TUI](index.md#the-session-manager-tui) and
[`wyrm status`](index.md#agent-status-across-sessions) show which panes have an
AI agent waiting. Two commands act on that:

```sh
wyrm agent next              # switch to the next blocked agent, cycling from this pane
wyrm agent next -state idle  # ...or the next one that finished its turn (any: either)
wyrm agent approve           # accept the prompt of the one blocked agent
wyrm agent approve web       # ...or the blocked agent in session "web" (or a pane ID like %3)
```

`approve` refuses to guess: with several agents blocked and no target, it lists
them and asks you to name one. It sends the agent profile's `approve` keys
(Enter by default, which picks the highlighted "Yes" in Claude Code's
permission prompt) — see
[agent profiles](configuration.md#tuiagentprofiles-describing-another-agent).

Bind `wyrm agent next` to a key to hop between waiting agents:

```tmux
bind a run-shell "wyrm agent next"
```

In the TUI, `b` jumps to the next blocked agent and `Y` (on the Panes panel)
approves the selected pane's prompt after confirming.

For status bars and editor plugins, `wyrm status -watch -format jsonl` streams
one compact JSON object per line whenever anything changes.

## Editor completion for `.wyrm.toml`

`wyrm schema` prints a JSON Schema for the config format, generated from the
same definitions the parser uses. Editors that understand taplo's schema
directive (taplo, VS Code's Even Better TOML) pick it up from the first line of
the file:

```toml
#:schema https://raw.githubusercontent.com/jskoll/wyrm/main/schema/wyrm.schema.json

[session]
root = "."
```

That gives key completion, descriptions on hover, and a warning on a misspelled
key as you type — before `wyrm validate` would catch it.

## Directories zoxide knows about

If you use [zoxide](https://github.com/ajeetdsouza/zoxide), `wyrm tui` can
list directories from your `cd` history in the Projects panel too — not just
ones with a wyrm config. It's opt-in (off by default) and does nothing at
all unless both a setting and the `zoxide` binary itself are present:

```toml
# ~/.config/wyrm/config.toml
[zoxide]
enabled = true
track   = true   # also call `zoxide add` after building a session
```

See [`[zoxide]`](configuration.md#zoxide-frecency-based-directory-discovery)
for how zoxide entries are deduplicated against real projects and what
starting one builds.

## Targeting a different tmux

By default wyrm talks to the default tmux server, via whatever `tmux` it
finds on `PATH`. To point it at a separate server or a different binary —
a wrapper/fork like `byobu` or `psmux`, or a specific install — add a
`[tmux]` block to the settings file:

```toml
[tmux]
socket  = "work"                    # tmux -L work
command = "/opt/tmux/bin/tmux"      # binary to invoke instead of "tmux"
```

`WYRM_TMUX_SOCKET` / `WYRM_TMUX_COMMAND` override these per-invocation and
take priority over the file. Every tmux call wyrm makes for the run —
including the final `attach-session` handoff — goes through the same
configured server and binary.
