# wyrm

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

```sh
wyrm                       # use .wyrm.toml (or legacy .tmuxconfig) in the cwd
wyrm up -n                # dry run: print the tmux commands and hooks, run neither
wyrm up -d                # build the session without attaching
wyrm <name>                # attach to a running session, or start a known project, by name
wyrm -config path/to/file  # explicit config
wyrm restart              # stop the session and build it again (-n to dry-run, -d to skip attaching)
wyrm kill [name]          # destroy the session (runs on_project_exit first; -n to dry-run)
wyrm pick                 # fuzzy-pick a running session and attach to it
wyrm tui                  # full-screen session manager (browse, preview, kill, rename, start)
wyrm save                 # save the running session's layout as this folder's config
wyrm edit                 # open the resolved config in $EDITOR, creating one if needed
wyrm validate             # check the effective config parses and validates (-strict fails on warnings)
wyrm send TARGET [CMD]     # send command or keys to target session/window/pane (-l, -n, -r)
wyrm list                 # list running tmux sessions non-interactively
wyrm list-configs         # list candidate config file paths (used by shell completion)
wyrm migrate-config       # move the local config into the shared config directory
wyrm init                 # scaffold a project config interactively or with -template (-force)
wyrm clone REPO [DEST]    # git clone, then confirm and build a session for it (-y, -no-start)
wyrm selfupdate           # download and install the latest release
wyrm setup-tmux           # generate or append recommended tmux popup configuration
wyrm doctor               # check tmux, settings, configs, and optional tools (-strict)
wyrm version
```

`wyrm <name>` first looks for a *running* session by that exact name and
attaches to it. Failing that, it looks for a *config* by that name — local or
in the shared directory — and builds it, so any project can be started from
anywhere without `cd`-ing to it first. wyrm always reports which config it
resolved on stderr.

If neither `.wyrm.toml` nor `.tmuxconfig` is found, wyrm falls back to
`~/.config/wyrm/default.wyrm.toml` if you've created one, otherwise a
built-in default: a single unnamed window rooted at the current directory.
This always builds (or attaches to) a session for the current folder, even
if unrelated sessions are already running elsewhere — the interactive
picker (below) is only ever shown when you ask for it with `pick`.

If a session with the same name is already running, wyrm **reattaches** to
it instead of rebuilding it. Otherwise it builds the session fresh, then
attaches.

Run from inside an existing tmux client, wyrm switches the client to the
session instead of nesting one tmux inside another.

### Beyond one project, one config

A few features extend discovery past "one `.wyrm.toml` per directory" — see
the [configuration reference](configuration.md) for each in full:

- **`[[wildcard]]`** applies one template config to every directory matching
  a glob, for a folder of similar repos that should all get the same layout.
- **`session.aliases`** gives a project a short, fixed name (`wyrm dot` for
  `dotfiles`) that resolves exactly, alongside its real session name.
- **`wyrm clone <repo> [dest]`** runs `git clone`, then builds a session for
  the result in one step — listing the shell the repo's config would run and
  asking first, since a freshly cloned config is one you have not read yet.
- **`[zoxide]`** (opt-in) lists directories from your `cd` history in `wyrm
  tui`'s Projects panel, not just ones with a wyrm config.
- **`[tmux]`** targets a non-default tmux server or binary
  (`socket`/`command`), for every tmux call wyrm makes.

## Diagnosing a setup

`wyrm doctor` checks everything wyrm depends on and reports anything that is
broken — or, more usefully, anything that is quietly doing nothing:

```console
$ wyrm doctor
ok    tmux             3.5a at /opt/homebrew/bin/tmux
ok    tmux server      default server, 4 sessions running
warn  settings         ~/.config/wyrm/config.toml (1 ignored key)
                       → unknown key "tui.mosue" — it is ignored (a typo?)
ok    storage          shared → ~/.config/wyrm/settings (7 configs)
ok    discovery        upward (searches parent directories up to a git root)
ok    wildcard[0]      "~/code/*" → 12 directories
warn  wildcard[1]      "~/work/**" matches no directories
                       → check the pattern; "*" matches one path segment, "/**" recurses
ok    config           .wyrm.toml → session "api" in ~/code/api
ok    state            ~/.config/wyrm/state.toml (14 projects recorded as started)
err   agent            tui.agent.profiles[0]: busy_pattern "(": error parsing regexp
                       → agent markers stay off until this compiles
note  agent notify     disabled
ok    theme            ~/.config/wyrm/theme.toml
ok    tui mouse        enabled
ok    editor           nvim
warn  clipboard        no backend found
                       → install one of wl-copy, xclip, xsel; until then "y" in the TUI cannot copy
note  zoxide           disabled (zoxide.enabled = false)
note  release signing  no key compiled in; `wyrm selfupdate` verifies checksums only

1 error, 3 warnings
```

Most of what wyrm gets wrong, it gets wrong silently: a `[[wildcard]]` pattern
that matches nothing, a misspelled settings key that is therefore ignored, an
agent `busy_pattern` that fails to compile and disables the markers, a theme
file that stops the TUI starting, a tmux too old for the `size` values in your
config, or no clipboard tool for `y` to use. Each looks identical to "the
feature just isn't working".

It only reads — nothing is repaired or written. It exits non-zero if anything
is an error; `-strict` also fails on warnings, which makes it usable as a CI
check alongside `wyrm validate -strict`.

## Editing, validating, and listing

`wyrm edit` opens the config wyrm would actually use — wherever discovery
(local, shared, or `-config`) finds it — in `$EDITOR` (falling back to
`vi`). If none exists yet, it creates one at the right spot for your
`storage` setting (a local `.wyrm.toml`, or the shared path `migrate-config`
would use) before opening it. If `$EDITOR` isn't in the environment at all —
which is what a pane launched by the tmux server sees, since the server's
environment never sourced your shell's rc files — wyrm asks your login shell
for it rather than dropping you into `vi`. After you save, wyrm re-parses the
file and prints a warning (not an error) if it doesn't validate — you're free
to save a work-in-progress and fix it later.

`wyrm validate` runs that same parse-and-validate check non-interactively,
without opening an editor or building a session — handy in a pre-commit hook
or CI for a repo that versions its `.wyrm.toml`.

Validation also reports keys wyrm doesn't recognise. A misspelled key is silently dropped by any TOML parser, so a config whose every key was a typo used to validate clean; those now surface as warnings, and `wyrm validate -strict` exits non-zero when there are any — useful in CI. Deprecations (the `.tmuxconfig` filename, the flat `panes` list) count as warnings too.

`wyrm list` prints the running tmux sessions non-interactively (unlike
`pick`, no interactive UI) for scripts and status bars. Add `-format json`
or `-format toml` for machine-readable output, or `-format names` for a bare
newline-separated list (handy for piping into `fzf` or another tool),
instead of the default aligned table:

```sh
wyrm list                  # name / window count / attached marker, one per line
wyrm list -format json | jq .
wyrm list -format names | fzf | xargs wyrm
```

`wyrm list-configs` prints the config file paths wyrm knows about — the
local file (if present) and every config in the shared directory (see the
[configuration reference](configuration.md)) — regardless of the current
`storage` setting. It exists mainly to back shell completion for `-config`,
but works standalone too.

`wyrm list-configs -names` lists discoverable project names and aliases,
including stopped projects. Shell completion combines these with live sessions.

## Picking a running session

`wyrm pick` opens an interactive, fuzzy list of the tmux sessions currently
running (most-recently-active first) and attaches to the one you choose. It's
handy from a plain shell, where tmux's own `choose-tree` isn't available
because you aren't attached to a client yet.

It is `wyrm tui` in a compact two-panel form — Sessions over Windows, with the
filter already open so typing narrows the list immediately. Same program, same
keys: anything you learn in one works in the other.

| Key | Action |
|---|---|
| type | fuzzy-filter the focused panel |
| ↑ / ↓, `j` / `k` | move the selection |
| `Enter` | attach to the selected session (or `switch-client` if you're already in tmux), landing on the selected window |
| `Tab`, `1` / `2` | move between the Sessions and Windows panels |
| `x` | kill the selected session or window, after a `y`/`n` confirm (no `on_project_exit` hook — it's a plain tmux kill) |
| `r` | rename the selection |
| `Esc` | clear the filter |
| `?` | the full key reference |
| `q`, `Ctrl-C` | quit |

```
┌Sessions──────────────┐┌%3 nvim───────────────────────────────┐
│  api-server     (2w) ││                                      │
│● wyrm           (3w) ││  … live preview of the selected pane │
│  notes          (1w) ││                                      │
└──────────────────────┘│                                      │
┌Windows───────────────┐│                                      │
│0: code               ││                                      │
│1: server             ││                                      │
└──────────────────────┘└──────────────────────────────────────┘
/wyr_
```

Both interfaces are built into the binary — there's no dependency on `fzf` or
any other external tool, keeping wyrm a single static binary. Colors come from
the same optional `theme.toml` the TUI uses.

> **Note (changed in 0.6.0):** `pick` used to be a separate implementation with
> its own bindings (`Ctrl-X` to kill, `Ctrl-W` for windows, `Ctrl-U` to clear
> the filter). Those are now `x`, the Windows panel, and `Esc`. Two UIs offering
> the same actions under different keys was the reason for the change.

If you already know the session's name, `wyrm <name>` skips the picker and
attaches (or `switch-client`s) directly to it — exact match only, no fuzzy
matching. Combined with shell completion (below), this means `wyrm <TAB>`
tab-completes to real running session names.

## The session manager TUI

`wyrm tui` opens a full-screen, keyboard-driven session manager in the spirit
of lazygit. Where `pick` is a one-shot "choose and attach", the TUI is for
_browsing and managing_ everything at once — your project configs, the running
sessions, and the windows and panes inside them — with a live preview of the
selected pane.

```
┌ Projects ─────┐┌ %1 nvim ─────────────────────────┐
│ ● webapp      ││ (live capture of the selected     │
│   dotfiles    ││  pane — refreshed every second —  │
├ Sessions ─────┤│  or, on the Projects panel, the   │
│ ● webapp  2w  ││  selected config's contents)      │
│   notes   1w  ││                                   │
├ Windows ──────┤│                                   │
│ 0: code       ││                                   │
│ 1: servers    ││                                   │
├ Panes ────────┤│                                   │
│ %1 nvim       ││                                   │
│ %2 npm        ││                                   │
└───────────────┘└───────────────────────────────────┘
 ↵: attach  x: kill  r: rename  n: new-win  L: layout  tab/1-4: focus  ?: help
```

The four left panels form a hierarchy: **Projects** (every `.wyrm.toml` wyrm can
discover — the local one plus the shared directory, marked `●` when a session by
that name is running) → **Sessions** (running now, or everything known with `a`) → **Windows** → **Panes**.
Windows track the selected session and panes track the selected window. The main
panel previews the selection: the live pane contents (via `capture-pane`) for the
session panels, or the config file's contents on the Projects panel. Focus starts
on **Sessions** — the usual reason to open the TUI is to get back to something
already running.

By default the **Sessions** panel lists what tmux is running. Pressing `a`
widens it to every session you could be in: the running ones first, then each
discovered project that isn't running, marked `stopped`. `Enter` on a stopped
row builds its session (exactly like `Enter` on **Projects**), so `wyrm pick` —
which shows no Projects panel — can start a session too, not only reach one.

A project only appears there if wyrm can discover it: a `.wyrm.toml` in the
current directory or the shared config directory, a `[[wildcard]]` pattern
match, or (opt-in) a zoxide-known directory. If the panel shows only the
project you are standing in, that is what to configure — see
[`configuration.md`](configuration.md).

| Key | Action |
|---|---|
| `Tab` / `Shift-Tab`, `1`–`4` | move focus between panels |
| `↑` / `↓`, `j` / `k` | move the selection in the focused panel |
| `f` | find a pane across every session at once |
| `Enter` | attach — lands on the exact window/pane under the cursor (or, on Projects and on a stopped **Sessions** row, starts/attaches the config's session) |
| `x` | kill the focused session / window / pane (or, on Projects, stop the session running `on_project_exit`) — with a confirm |
| `r` | rename the focused session or window |
| `n` | new window in the current session |
| `<` / `>` (or `K` / `J`) | reorder focused window up or down (`swap-window`) |
| `W` | move focused window to another session |
| `s` / `v` | split focused pane vertically (new pane below) |
| `S` | split focused pane horizontally (new pane to the right) |
| `L` | cycle the focused window through tmux's standard layouts |
| `z` | toggle zoom on the focused pane |
| `e` | edit the selected project's config in `$EDITOR` |
| `a` | on **Sessions**, also list known-but-stopped projects — `Enter` starts one |
| `R` | reload the project and session lists |
| `?` | show the full keyboard-shortcut help overlay (scrollable) |
| `q` / `Ctrl-C` | quit |

Press `?` at any time for a full-screen cheat sheet of every binding — laid out
in two columns, or one on a narrow terminal, and scrollable (`↑`/`↓` or `j`/`k`,
`Esc` to close) when it's taller than the screen. Like
`pick`, attaching from the TUI uses `switch-client` when you're already inside
tmux and `attach-session` otherwise. When run inside tmux, the pane the TUI
itself occupies shows a placeholder instead of a preview, to avoid capturing the
TUI into its own view. It also pairs well with tmux's `display-popup` for a
floating session manager over your current work:

```sh
# ~/.tmux.conf — prefix + g opens the session manager in a popup
bind g display-popup -d "#{pane_current_path}" -w 80% -h 80% -E "wyrm tui"
```

### Colors

The TUI ships with [Nord](https://www.nordtheme.com): frost blue on the focused
panel, dim polar-night gray on the rest, aurora yellow while a filter is active,
and a gray selection band that leaves each row's own colors (teal window
indices, green "running" dots) intact.

Override any of it with `~/.config/wyrm/theme.toml`
(`$XDG_CONFIG_HOME/wyrm/theme.toml` if set) — every role is optional, and the
ones you leave out keep their Nord default:

```toml
accent   = "#88c0d0"  # focused panel border + title, live preview title
subtle   = "#4c566a"  # blurred borders, hints, help footer
filter   = "#ebcb8b"  # the panel being filtered: border, title, prompt
selected = "#434c5e"  # selection band in the focused panel
text     = "#eceff4"  # text on the selection band
trail    = "#3b4252"  # selection band in the other panels
index    = "#8fbcbb"  # window indices and pane IDs
active   = "#a3be8c"  # running / attached dots
error    = "#bf616a"  # failed actions
blocked  = "#ebcb8b"  # an agent waiting on an answer (⏸)
idle     = "#8fbcbb"  # an agent that finished its turn (✓)
```

Values are `#rgb` or `#rrggbb`. A misspelled role or an unparseable color is an
error rather than a silently ignored line — `wyrm tui` says which one and
exits, since the alt screen would otherwise wipe the message on its way up.

Colors are literal hex rather than terminal palette indices, so a theme looks
the same wherever it runs; Lipgloss degrades them on terminals without true
color and drops them entirely under [`NO_COLOR`](https://no-color.org).

`wyrm tui` and `wyrm pick` are the same [Charm](https://charm.sh) stack
program (Bubble Tea / Lipgloss); the core build/attach path stays free of it,
so `wyrm up` never renders anything.

## tmux popup integration

`wyrm setup-tmux` prints the tmux configuration that binds `wyrm pick` and
`wyrm tui` to popup windows, so the session manager is one keystroke away from
inside tmux:

```sh
wyrm setup-tmux                        # print the snippet
wyrm setup-tmux -a                     # append it to your tmux.conf
wyrm setup-tmux -key-pick C-j \
                -key-tui  C-w          # choose the bindings
wyrm setup-tmux -status=false          # omit the status-bar line
```

`-a` writes to `$TMUX_CONF` if set, else `$XDG_CONFIG_HOME/tmux/tmux.conf`
(`~/.config/tmux/tmux.conf` by default) if it exists, else `~/.tmux.conf`. It
copies the file to `<path>.wyrm-backup` first, and does nothing if the snippet
is already present, so re-running is safe.

Key specifications are checked before anything is written. `bind-key` fails at
config-load time and takes the rest of the file with it, so a bad key would
break your whole tmux config with no indication of which line did it —
`wyrm setup-tmux -key-pick 'C-j; kill-server'` is refused up front instead.

## Agent status across sessions

`wyrm status` reports what the AI agents running in your panes are doing —
across every session, not just the one you're attached to — for a status bar or
a script. It's the same detection the TUI's markers use — see
[`[[tui.agent.profiles]]`](configuration.md#tuiagentprofiles-describing-another-agent)
for teaching it about another agent — without the interactive UI:

```sh
wyrm status                             # ⏸ 2 blocked · ✓ 1 idle
wyrm status -v                          # one line per agent pane
wyrm status -format json | jq .
wyrm status -session api                # only that session
wyrm status --watch --interval 2s       # stream, for a bar that reads stdin
```

`-format` picks the consumer:

| Format | For |
|---|---|
| `text` | a terminal, or `-v` for the per-pane list (the default) |
| `json` | scripts — full per-pane detail plus a summary |
| `tmux` | `status-right`, with tmux `#[fg=...]` styling |
| `waybar` | Waybar's JSON protocol (text, alt, tooltip, class) |
| `sketchybar` | sketchybar's `icon=`/`label=` key-value output |

For tmux's own status bar, `wyrm setup-tmux` emits the wiring:

```tmux
set -g status-right '#(wyrm status --format tmux) | %H:%M '
```

Reading a pane's contents costs a `capture-pane` call each, and under `--watch`
that repeats every interval against the server you're working in. So a scan
looks at a bounded number of agent panes; if there are more, the extra ones are
reported rather than quietly dropped — `+3 unscanned` in the text and tmux
formats, `summary.skipped` in JSON.

## Sending keys to a session

`wyrm send` types into a session, window, or pane without attaching — for
scripts, hooks, or driving a long-running process from another terminal:

```sh
wyrm send api "npm test"              # type it in the session's active pane, press Enter
wyrm send api:build "make"            # target a window
wyrm send api:build.1 "ls"            # target a pane
wyrm send -n api "partial input"      # no trailing Enter
wyrm send -r api C-c                  # send a raw tmux key symbol
wyrm send api -- "-v --flag"          # text starting with a dash
```

Text is sent literally by default, so it arrives exactly as written; `-l` states
that explicitly. `-r` is the opposite — it sends tmux key names like `C-c`,
`Escape`, or `Up`, and combining the two is an error. A `--` between the target
and the text lets you send anything starting with `-`.

Targets are `session`, `session:window`, or `session:window.pane`; a raw tmux ID
(`$1`, `@1`, `%3`) works too. Window names match case-insensitively, or by index.

## Initializing a new configuration

`wyrm init` scaffolds a well-commented `.wyrm.toml` config, either interactively via a step-by-step wizard or from a starter template:

```sh
wyrm init                       # interactive wizard (session name, root, layout presets, commands)
wyrm init -template go          # non-interactively scaffold a Go starter template
wyrm init -template node        # Node.js template
wyrm init -template python      # Python template
wyrm init -template rust        # Rust template
wyrm init -template monorepo    # Monorepo template
wyrm init -template minimal     # Minimal 2-pane template
wyrm init -force                # overwrite an existing config without confirmation
```

## Saving a running session

`wyrm save` snapshots a running tmux session's windows, split layout, and
sizes into a new config for the current folder — the reverse of building a
session from one. Run it from inside the session you want to capture, or
from a plain shell in the session's folder (it looks up the session the same
way a bare `wyrm` would).

```sh
wyrm save                  # writes .wyrm.toml (or the shared-storage path)
wyrm save -config PATH     # write to PATH instead of the resolved location
wyrm save --stdout         # print generated TOML to stdout (or -o -)
wyrm save -n               # dry run preview without writing to disk
```

tmux keeps no record of what was originally typed into a pane, so each
split's `command` is captured as whatever program is currently running in
that pane's foreground (`nvim`, `npm`, ...) — the same approach tools like
tmuxp's `freeze` use. That's usually enough to relaunch the same programs,
but it won't recover one-off shell commands that have already finished, and
it can't capture `pre_window`, `on_project_start`/`on_project_exit`, or
comments — those are yours to add by hand afterward, e.g. with `wyrm edit`.

Like `migrate-config`, `save` refuses to overwrite an existing config
rather than silently discarding hooks or comments you've already written —
remove or rename the file first if you want to re-save over it.

## Shell completion

Completion scripts for bash, zsh, and fish live in
[`completions/`](https://github.com/jskoll/wyrm/tree/main/completions).
They complete flag names, `-format`'s values, `-config` (to the local file
and every config in the shared directory, via `wyrm list-configs`), and a
bare argument (to running session names, via `wyrm list -format names`) —
so any completion involving live state shells back out to wyrm itself
rather than guessing.

`brew install jskoll/tap/wyrm` installs all three automatically. Installing
some other way:

```sh
# bash (needs bash-completion installed)
source completions/wyrm.bash                                 # this shell only
cp completions/wyrm.bash /usr/local/etc/bash_completion.d/    # every shell (macOS + Homebrew's bash-completion)

# zsh
cp completions/_wyrm ~/.zsh/completions/_wyrm   # any directory on your $fpath
# then: autoload -Uz compinit && compinit

# fish
cp completions/wyrm.fish ~/.config/fish/completions/wyrm.fish  # auto-loaded
```

See the [configuration reference](configuration.md) for the full `.wyrm.toml`
format, or the [examples](examples.md) for ready-to-use configs.
