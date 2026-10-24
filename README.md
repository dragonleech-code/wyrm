<p align="center">
  <img src="logo.svg" alt="wyrm" width="240">
</p>

# wyrm 🐉

Repeatable tmux session layouts from a TOML config — nested split trees,
lifecycle hooks, one static binary.

Drop a `.wyrm.toml` in your project, run `wyrm`, and get the same windows,
panes, and running commands every time.

```toml
[session]
root = "."

[[windows]]
name = "code"

  [[windows.splits]]
  command = "nvim"

  [[windows.splits]]
  type = "h"          # split horizontally: new pane on the right
  size = 30           # it gets 30% of the width
  command = "npm run dev"
```

## Why another one?

| | Language | Config | Runtime deps | Layouts as split *trees* | Lifecycle hooks |
|---|---|---|---|---|---|
| tmuxinator | Ruby | YAML | Ruby | — | ✓ |
| tmuxp | Python | YAML/JSON | Python | — | ✓ |
| smug | Go | YAML | none | — | ✓ |
| **wyrm** | Go | **TOML** | none | **✓** | ✓ |

- **TOML, not YAML** — comment-friendly, no indentation traps.
- **Splits are a tree** — nest splits inside splits with explicit percentage
  sizes, instead of picking from preset layouts.
- **Pane-ID targeting** — layouts come out the same regardless of your
  `base-index` / `pane-base-index` settings.
- **One static binary** — Go stdlib plus a TOML parser, nothing at runtime
  but tmux itself.

## Install

```sh
brew install jskoll/tap/wyrm
```

On Arch Linux, from the AUR:

```sh
yay -S wyrm-bin   # or: paru -S wyrm-bin
```

On Debian/Ubuntu or Fedora/RHEL, grab the `.deb`/`.rpm` from the
[latest release](https://github.com/jskoll/wyrm/releases/latest) and install
it directly:

```sh
sudo dpkg -i wyrm_*_linux_amd64.deb        # Debian/Ubuntu
sudo dnf install ./wyrm_*_linux_amd64.rpm  # Fedora/RHEL
```

Or via Go:

```sh
go install github.com/jskoll/wyrm@latest
```

Or build from a clone: `make install` (uses `go install` with a stamped version).

## Usage

wyrm uses git-style subcommands:

```sh
wyrm                        # use .wyrm.toml (or legacy .tmuxconfig) in the cwd
wyrm up                     # same as bare wyrm, spelled explicitly
wyrm up -n                  # dry run: print the tmux commands and hooks, run neither
wyrm up -d                  # build the session without attaching
wyrm up -reconcile          # add the windows the config defines that the running session lacks
wyrm up -host devbox        # build the session on another machine over ssh
wyrm <name>                 # attach to a running session, or start a known project, by name
wyrm @workspace             # start every project in a [[workspace]], attach to the first
wyrm -config path/to/file   # explicit config for the default build
wyrm restart                # stop the session and build it again (-n to dry-run, -d to skip attaching)
wyrm kill [name|@ws]        # destroy the session (runs on_project_exit first; -n to dry-run)
wyrm pick                   # fuzzy-pick a running session and attach to it
wyrm tui                    # full-screen session manager (browse, preview, kill, rename, start)
wyrm save                   # save the running session's layout as this folder's config
wyrm edit                   # open the resolved config in $EDITOR, creating one if needed
wyrm validate               # check the effective config (-strict fails on warnings, -explain shows discovery)
wyrm diff [name]            # how the running session differs from its config
wyrm allow [path]           # trust a project config so wyrm runs its commands (-list, -revoke)
wyrm deny [path]            # refuse a project config
wyrm status                 # agent status across sessions (-format json, jsonl, tmux, waybar, ...)
wyrm agent next|approve     # jump to the next waiting agent, or accept a blocked agent's prompt
wyrm send TARGET [CMD]      # send command or keys to target session/window/pane (-l, -n, -r)
wyrm list                   # list running tmux sessions non-interactively
wyrm list-configs           # list candidate config file paths (used by shell completion)
wyrm migrate-config         # move the local config into the shared config directory
wyrm init                   # scaffold a project config interactively or with -template (-force)
wyrm clone REPO [DEST]      # git clone, then confirm and build a session for it (-y, -no-start)
wyrm worktree BRANCH        # create a git worktree for BRANCH and start a session in it
wyrm wait TARGET...         # wait for tcp:HOST:PORT, file:PATH, or a URL to be ready
wyrm schema                 # print the JSON Schema for .wyrm.toml, for editor completion
wyrm selfupdate             # download and install the latest release (-check, -version V)
wyrm setup-tmux             # generate or append recommended tmux popup configuration (-a)
wyrm doctor                 # check tmux, settings, configs, and optional tools (-strict)
wyrm version                # print version
wyrm help                   # usage overview
```

Each subcommand takes its own flags — e.g. `wyrm kill -config path`,
`wyrm list -format json`. Run `wyrm <cmd> -h` to see them.

> **Note (breaking change in 0.3.0):** modes moved from top-level flags to
> subcommands — `wyrm -kill` is now `wyrm kill`, `wyrm -list` is now
> `wyrm list`, and so on. Bare `wyrm`, `wyrm <name>`, and `wyrm -config PATH`
> are unchanged.

`wyrm <name>` first looks for a *running* session by that exact name and
attaches to it. If there isn't one, it looks for a *config* by that name —
local or in the shared directory — and builds it. That's what makes shared
storage worth using: you can start any project from anywhere, without `cd`-ing
to it first.

`wyrm` always reports which config it resolved (on stderr), since discovery has
several layers and an unexpected session is otherwise hard to explain.

If neither `.wyrm.toml` nor `.tmuxconfig` is found, wyrm falls back to
`~/.config/wyrm/default.wyrm.toml` if you've created one, otherwise a
built-in default: a single unnamed window rooted at the current directory.
This always builds (or attaches to) a session for the current folder, even
if unrelated sessions are already running elsewhere — the interactive
picker (below) is only ever shown when you ask for it with `pick`.

## Documentation

The full documentation lives at **[jskoll.github.io/wyrm](https://jskoll.github.io/wyrm/)**:

- **[Home](https://jskoll.github.io/wyrm/)** — usage, the session manager TUI, the picker, popups,
  agent status, saving a session, shell completion, and `wyrm doctor`.
- **[Guide](https://jskoll.github.io/wyrm/guide/)** — workflows: trusting configs from other people's
  repositories, shared config storage, workspaces (`wyrm @name`), git
  worktrees, remote sessions over ssh, `wyrm diff` and `up -reconcile`,
  waiting on dependencies with `wait_for`, acting on waiting agents, and
  editor completion via the JSON Schema.
- **[Configuration reference](https://jskoll.github.io/wyrm/configuration/)** — every key in
  `.wyrm.toml` and the settings file, including `extends`, hooks,
  `hook_timeout`, the split tree, and template variables.
- **[Examples](https://jskoll.github.io/wyrm/examples/)** — annotated configs for common stacks.
- **[How wyrm compares](https://jskoll.github.io/wyrm/comparison/)** — against tmuxinator, tmuxp, smug,
  and sesh.
- **[Security](https://jskoll.github.io/wyrm/security/)** — the trust model and release signing.

The site is built from [`docs/`](https://github.com/jskoll/wyrm/tree/main/docs),
which is the source of truth; this README is the front page.

## Security

A wyrm config **executes shell commands by design** — hooks run via your
`$SHELL` (falling back to `sh`), and pane commands are typed into your shell.
Treat a config the way you would a `Makefile` or `.envrc`.

Before running a project config that didn't come from your own wyrm
directories — one that arrived with a repository you cloned, say — wyrm shows
every command it would run and asks first, and asks again whenever the config
changes. `wyrm up -n` prints everything a config would do without running any
of it. See [Security](https://jskoll.github.io/wyrm/security/) for the full model, and report
vulnerabilities privately via
[GitHub security advisories](https://github.com/jskoll/wyrm/security/advisories/new).

## Development

```sh
make build       # build ./wyrm
make test        # unit + integration (integration needs tmux; isolated socket)
make test-unit   # -short: unit tests only
make lint        # golangci-lint + gofmt
```

See [CONTRIBUTING.md](https://github.com/jskoll/wyrm/blob/main/CONTRIBUTING.md)
for the layout and error-handling conventions.

## License

[MIT](LICENSE)
