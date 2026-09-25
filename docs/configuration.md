# Configuration reference

## Where wyrm looks for a config

By default wyrm looks for `.wyrm.toml` (then the legacy `.tmuxconfig`) in the
current directory. To keep project configs in one shared place instead,
create a global settings file at `~/.config/wyrm/config.toml`
(`$XDG_CONFIG_HOME/wyrm/config.toml` if set):

| Key | Type | Default | Description |
|---|---|---|---|
| `storage` | string | `local` | `local` (search the cwd, as always) or `shared` |
| `shared_dir` | string | `~/.config/wyrm/settings` | Directory to search in `shared` mode; `~` and `$VAR` are expanded |

In `shared` mode, wyrm looks for `<folderName>.wyrm.toml` (the current
directory's basename) inside `shared_dir` first, falling back to the normal
local search if it's missing. Run `wyrm migrate-config` to move an existing
local config into the shared directory under the right name.

Migration preserves the discovered config's own directory even when invoked
from a child directory. It anchors relative roots and records `session.project_dir`
as ownership metadata, so a root such as `backend` remains discoverable from
the original project directory. Comments and TOML key spellings are preserved.

### When two projects share a folder name

Basenames collide: `~/work/api` and `~/personal/api` both want
`api.wyrm.toml`, and so do `services/api` and `packages/api` in a monorepo.

A shared config belongs to `session.project_dir` when present, otherwise to
the directory its absolute `session.root` names.
The first project to claim a basename keeps the plain filename; any other
project with that basename gets `<folder>-<hash>.wyrm.toml`, where the hash is
eight characters of its absolute path — stable, so a project always resolves to
the same file. `wyrm migrate-config` reports it when it applies, and names the
owning directory if you try to migrate onto a file that is already taken.

Nothing already on disk moves: a config that has no collision keeps the name it
has. A config with no `session.root`, or a relative one, identifies no
particular project, so it is treated as belonging to whoever is asking — which
is how configs written before this worked, and continue to work.

The storage filename does not rename the session: its name comes from
`session.name` or the resolved root. Set distinct names for same-named roots:

```toml
[session]
name = "api-personal"
root = "~/personal/api"
```

## Creating a config (`wyrm init`)

Run `wyrm init` to generate a new `.wyrm.toml` for the current project. By default it guides you through an interactive wizard (session name, root directory, window layout presets, and pane commands), or you can scaffold starter templates non-interactively:

```sh
wyrm init                       # interactive wizard
wyrm init -template go          # Go template
wyrm init -template node        # Node.js template
wyrm init -template python      # Python template
wyrm init -template rust        # Rust template
wyrm init -template monorepo    # Monorepo template
wyrm init -template minimal     # Minimal 2-pane template
wyrm init -force                # overwrite an existing config without confirmation
```

## `[[wildcard]]` — one config, many directories

A `[[wildcard]]` entry applies one template config to every directory
matching a glob pattern, instead of writing a `[[session]]`-per-project
config for each. It's for a folder of similar repos — a client's projects, a
monorepo's packages — that should all get the same layout without
maintaining one file per directory.

```toml
[[wildcard]]
pattern = "~/work/*"
config  = "~/.config/wyrm/settings/_client-template.wyrm.toml"

[[wildcard]]
pattern = "~/code/**"       # nested at any depth, not just one level
config  = "~/.config/wyrm/settings/_dev-template.wyrm.toml"
```

| Key | Type | Description |
|---|---|---|
| `pattern` | string | A glob (`*`, `?`, `[...]`); `~` and `$VAR` are expanded. A trailing `/**` matches every directory nested at any depth under the base, instead of one level |
| `config` | string | Path to the template config applied to every matching directory |

A directory that has its own config is never listed twice: a pattern like
`~/code/*` matches the directory you are standing in as well as its siblings,
and that directory's own `.wyrm.toml` (or shared config) wins over the
template.

The template is an ordinary `.wyrm.toml`. Its `session.name` is normally left
unset, since each matched directory's basename supplies it. Its
`session.root` is always overridden with the matched directory regardless of
what the file says — but `wyrm` still requires a config to set *something*
there (or a name) to parse at all, so the convention is to write
`root = "."` as a placeholder:

```toml
# ~/.config/wyrm/settings/_client-template.wyrm.toml
[session]
root = "."

[[windows]]
name = "code"
  [[windows.splits]]
  command = "nvim"
```

`wyrm <name>` (where `<name>` is a matched directory's basename), the TUI's
Projects panel, and `wyrm list-configs` all pick up wildcard matches the same
way they pick up any other project — matched entries are marked `~` in the
TUI to distinguish them from a project with its own config file.

Matching a pattern means walking the filesystem, and a `/**` pattern walks the
whole subtree. Results are cached for 10 seconds, so a long-running TUI is not
re-walking your home directory on every refresh. A directory you create shows up
within that window; press `R` in the TUI to see it immediately.

## `[tmux]` — which tmux wyrm talks to

| Key | Type | Default | Description |
|---|---|---|---|
| `tmux.socket` | string | — | Passed as `tmux -L <name>`, selecting a separate tmux server instead of the default one |
| `tmux.command` | string | `tmux` | Binary to invoke in place of `tmux` — a full path, or a wrapper/fork like `byobu` or `psmux` |

```toml
[tmux]
socket  = "work"
command = "/opt/tmux/bin/tmux"
```

Both can also be set per-invocation via `WYRM_TMUX_SOCKET` / `WYRM_TMUX_COMMAND`,
which take priority over the settings file. Every tmux invocation for the
process — including the final `attach-session` handoff — goes through the
same configured binary and socket.

## `[tui]` — session manager preferences

The same global settings file configures `wyrm tui`. Both sections are
optional; the defaults below apply when the file (or a key) is absent.

| Key | Type | Default | Description |
|---|---|---|---|
| `tui.mouse` | bool | `true` | Capture the mouse. `false` leaves your terminal's own click-drag text selection alone; `m` toggles it for one run |
| `tui.agent.enabled` | bool | `true` | Mark sessions, windows, and panes whose AI agent is waiting on you |
| `tui.agent.commands` | array | `["claude"]` | The `#{pane_current_command}` values the **built-in** detector inspects |
| `tui.agent.profiles` | array | — | Full descriptions of other agents (below) |

```toml
[tui]
mouse = true

[tui.agent]
enabled  = true
commands = ["claude", "myclaude-wrapper"]
```

Agent detection costs two `tmux` invocations per refresh: one `list-panes` to
find the candidates, and one batched call that captures all of them together.
Panes running an ordinary shell are never captured, and the refresh pauses
entirely while the terminal is unfocused. Set `enabled = false` to stop it
altogether.

### Which panes are marked

A pane is marked only on positive evidence:

| Marker | Meaning |
|---|---|
| ⏸ | the agent is stopped on a question it can't answer itself |
| ✓ | the agent finished its turn and is waiting for the next instruction |
| *(none)* | busy, or nothing recognisable on screen |

An agent whose screen matches nothing is reported as *unknown* and carries no
marker. That is deliberate: a marker that says "come look" when wyrm has no idea
is worse than no marker, and it means a detector broken by a UI change goes
quiet rather than lying.

### `[[tui.agent.profiles]]` — describing another agent

`commands` widens which panes the **built-in** (Claude Code) detector looks at,
which is what you want for a wrapper script. It does not teach wyrm another
agent's UI — that needs a profile:

| Key | Type | Description |
|---|---|---|
| `commands` | array | `#{pane_current_command}` values this profile claims. Required |
| `busy` | array | Lowercase substrings that appear only while a turn is running |
| `blocked` | array | Substrings that appear only around a prompt awaiting an answer |
| `idle` | array | Substrings of the idle input box |
| `busy_pattern` | string | Regular expression for a live indicator no fixed string catches, e.g. a running timer |

```toml
[[tui.agent.profiles]]
commands     = ["aider"]
busy_pattern = 'thinking \d+s'
idle         = ["aider> "]
```

Match the agent's own **chrome** — the text it draws around its input box and
prompts — never the prose it is displaying. A pane is a screenful of arbitrary
text, and an agent merely *quoting* a question is not asking one.

A numbered choice list (`1.` / `2.` on adjacent lines near the bottom) counts as
blocked for every profile, since that's a property of what a selector looks like
rather than of any one agent.

Defining any profile replaces the built-in one entirely, so one agent's chrome
can't decide another's state — list Claude Code yourself if you still want it.
A profile with no `commands`, or a `busy_pattern` that doesn't compile, is an
error reported before the TUI starts rather than a silent fallback.

### `[tui.agent.notify]` — being told when an agent needs you

Off by default. When enabled, the TUI notifies you as an agent changes state,
so you can leave it running and get on with something else:

```toml
[tui.agent.notify]
enabled = true          # nothing is delivered until this is on
```

| Key | Type | Default | Description |
|---|---|---|---|
| `enabled` | bool | `false` | Master switch — with this off, nothing below is delivered |
| `desktop` | bool | `true` | OS notification (`osascript` on macOS, `notify-send` on Linux) |
| `bell` | bool | `false` | Terminal bell (`\a`) |
| `osc` | bool | `false` | OSC 9 / OSC 777 escape, for terminals that turn it into a notification |
| `on_blocked` | bool | `true` | Notify when an agent starts waiting on an answer |
| `on_idle` | bool | `false` | Notify when an agent finishes and goes idle |
| `command` | string | — | Run this instead of the desktop notification (see below) |

`bell` and `osc` are written to the terminal, so they only apply while
`wyrm tui` is running. `desktop` and `command` work regardless.

Terminal writes are serialized with the TUI renderer. Notification and
clipboard helpers, zoxide, and non-interactive tmux commands time out after
ten seconds. Interactive attachment, editors, and lifecycle hooks are not limited.

`command` **replaces** the desktop notification rather than adding to it — if
you set one, `desktop` is not delivered. The command runs through your `$SHELL`
with the notification in its environment:

| Variable | Contents |
|---|---|
| `WYRM_NOTIFY_TITLE` | Short summary line |
| `WYRM_NOTIFY_MESSAGE` | Body text |
| `WYRM_NOTIFY_STATE` | `blocked` or `idle` |
| `WYRM_NOTIFY_SESSION` | Session name the agent is in |
| `WYRM_NOTIFY_PANE` | tmux pane ID (`%3`) |

```toml
[tui.agent.notify]
enabled = true
on_idle = true
command = 'terminal-notifier -title "$WYRM_NOTIFY_TITLE" -message "$WYRM_NOTIFY_MESSAGE"'
```

`wyrm doctor` reports which channels are configured, so you can tell whether a
silent setup is switched off or merely undeliverable.

## `[zoxide]` — frecency-based directory discovery

`wyrm tui`'s Projects panel can list directories [zoxide](https://github.com/ajeetdsouza/zoxide)
knows about — from your ordinary `cd` history — alongside your wyrm
projects, so a directory you visit often shows up there whether or not it
has a config of its own. This is opt-in and gracefully absent: it does
nothing unless both `enabled = true` is set here *and* the `zoxide` binary
is actually on `PATH`. Unlike `[tui].mouse`/`[tui].agent`, it defaults to
**off** — it's a real dependency on a binary other than tmux and a
side-effecting write into zoxide's own database, not a pure UI convenience,
so it shouldn't activate just because zoxide happens to be installed.

| Key | Type | Default | Description |
|---|---|---|---|
| `zoxide.enabled` | bool | `false` | List zoxide-known directories in the TUI's Projects panel |
| `zoxide.track` | bool | `false` | Call `zoxide add` after building a session, so using wyrm to reach a directory also teaches zoxide about it |

```toml
[zoxide]
enabled = true
track   = true
```

A zoxide directory is skipped if its basename collides with an
already-discovered wyrm project's name — the wyrm project wins, since it's
the more specific match. Selecting a zoxide-only entry builds a session from
your `default.wyrm.toml` (or wyrm's built-in default) rooted at that
directory, marked `z` in the Projects panel to distinguish it from a project
with its own config or a `~`-marked wildcard match. This is a `wyrm
tui`-only feature — `wyrm <name>` and `wyrm pick` don't resolve zoxide
directories by name.

## `[discovery]` — upward config traversal

When `wyrm` is run inside a subdirectory of a repository or project, it
automatically traverses parent directories to find the repository's `.wyrm.toml`.
Traversal stops at git repository boundaries (`.git`), the user's home
directory, or the filesystem root.

| Key | Type | Default | Description |
|---|---|---|---|
| `discovery.upward` | bool | `true` | Walk upward from the current working directory to find `.wyrm.toml` |

```toml
[discovery]
upward = true
```

## Custom default config

When no project config is found at all, wyrm falls back to
`default.wyrm.toml` next to the global settings file (`~/.config/wyrm/`, or
`$XDG_CONFIG_HOME/wyrm/`) if present, otherwise its built-in default. This
file uses the same `[session]` / `[[windows]]` format documented below.

## `[session]`

| Key | Type | Default | Description |
|---|---|---|---|
| `name` | string | basename of `root` | tmux session name |
| `root` | string | `.` | Default working directory for every window and pane; `~` and `$VAR` are expanded |
| `project_dir` | string | — | Original project directory recorded by migration for shared-config ownership; independent of `root` |
| `on_project_start` | string | — | Shell command run (via your $SHELL, or sh, in `root`) before the session is created |
| `on_project_exit` | string | — | Shell command run before `wyrm kill` destroys the session |
| `on_project_attach` | string | — | Shell command run every time you attach to the session (fresh build or reattach) |
| `on_project_detach` | string | — | Shell command run inside tmux when client detaches |
| `on_project_first_start` | string | — | Runs alongside `on_project_start`, but only the very first time this project is ever started |
| `on_project_restart` | string | — | Runs alongside `on_project_start` on every start after the first |
| `startup_window` | string | first window | Window (name or index) to focus after creation. Without it the session opens on the first window, focused on its first pane |
| `startup_pane` | int | — | Pane to focus within `startup_window` (uses your `pane-base-index`) |
| `env` | table | — | Environment variables set in every pane of the session (below) |
| `aliases` | array | — | Additional exact-match names `wyrm <name>` resolves to this project, alongside its session name (below) |
| `enable_pane_titles` | bool | `false` | Turn on tmux's live pane-border status line for the session |
| `pane_title_position` | string | `top` | `top` or `bottom` |
| `pane_title_format` | string | `#{pane_index}: #{pane_current_command}` | tmux format string shown on the pane-border line |

At least one of `name` / `root` is required.

### `on_project_first_start` / `on_project_restart`

`on_project_start` always fires on a fresh build. Alongside it, exactly one
of these two also fires, based on whether this project (identified by its
resolved session root) has ever started a session before — tracked in
`~/.config/wyrm/state.toml` (`$XDG_CONFIG_HOME/wyrm/state.toml` if set), so
the distinction survives a `wyrm kill` and holds across process runs, not
just within one.

```toml
[session]
on_project_first_start = "npm install"   # only the very first time
on_project_restart     = "npm run migrate"  # every start after that
```

Neither fires for a config with no on-disk identity — the built-in default,
or one loaded some other way than from a file.

### `aliases`

```toml
[session]
name    = "dotfiles"
aliases = ["dot", "df"]
```

`wyrm dot` and `wyrm df` now attach to (or start) the same project as `wyrm
dotfiles` — handy for a project you jump to constantly, where a short fixed
name beats remembering (or fuzzy-typing) the full one. Matching is exact
only, same as the session name itself: an alias never partially matches, and
an exact project name always wins over an alias collision if two projects'
names/aliases happen to overlap.

### `[session.env]`

```toml
[session.env]
NODE_ENV = "development"
API_URL = "http://localhost:3000"
```

The variables are passed to tmux as each window and pane is created, so they
reach the shell in every pane. Setting them once on the session with
`set-environment` would not: that only affects processes started afterward.

Requires tmux 3.2 or newer (for `-e` on `new-session` / `split-window`). Every
other wyrm feature works on 3.1+.

## Hooks as wyrm's extension point

wyrm has no plugin system, and isn't planning to build one — a real plugin
loader (discovering executables, a versioned data contract, running
third-party code at points a config doesn't obviously flag) is a lot of new
machinery for a gap the six hooks below already cover: env prep, calling
external tools, waiting on a condition, conditional logic, notifications —
each one line of shell, run via your `$SHELL` (falling back to `sh`), with
its own working directory:

| Hook | Runs |
|---|---|
| `session.on_project_start` | Before the session is created, every fresh build |
| `session.on_project_first_start` | Alongside `on_project_start`, only the very first time this project is ever started |
| `session.on_project_restart` | Alongside `on_project_start`, every start after the first |
| `session.on_project_attach` | Every time you attach to the session (fresh build or reattach) |
| `session.on_project_detach` | Inside tmux when client detaches |
| `session.on_project_exit` | Before `wyrm kill` destroys the session |
| `windows.pre_window` | Typed into every pane of a window, before that pane's own command |
| `windows.post_window` | Run once a window's panes and their commands all exist |

A failure in any of them is reported and the build continues — see each
hook's own section above for exact timing and semantics. If a real need
ever shows up that these genuinely can't express (structured output fed
back into a build decision, say, which a shell exit code can't carry), that
would be worth a fresh design conversation — not something to route around
with a bigger hook.

## `[[windows]]`

Startup commands are sent with pane synchronization temporarily disabled,
then the configured or inherited setting is restored. Direct `run` processes
start after the layout and `remain_on_exit` options are ready, including fast
commands such as `true`. Leading typeless children reuse their parent's pane;
their root and environment apply when that physical pane is created.

| Key | Type | Default | Description |
|---|---|---|---|
| `name` | string | — | Window name |
| `root` | string | session root | This window's working directory. A relative path resolves against `session.root`, so `root = "api"` means the `api` folder inside the project |
| `env` | table | — | Environment variables set for all panes of this window, overriding `session.env` |
| `pre_window` | string | — | Command typed once into **every pane of the window**, before that pane's own command (e.g. `nvm use 18`) |
| `post_window` | string | — | Shell command **run** (not typed) once all of the window's panes and their commands exist |
| `splits` | list | — | Split tree (below) — the recommended layout format |
| `panes` | list | — | Legacy flat pane list (below); ignored when `splits` is set |
| `layout` | string | `tiled` | tmux layout applied after legacy `panes` (`even-horizontal`, `main-vertical`, ...). Ignored when `splits` is set — a named layout would discard the tree's sizes — and wyrm warns if you set both |
| `synchronize` | bool | `false` | Turn on tmux's `synchronize-panes` for this window, so typing goes to every pane at once. `synchronize_panes` is an accepted alias |
| `remain_on_exit` | bool | `false` | Keep the window's panes open after their command exits, instead of closing them — so you can read what a failed command printed |

`post_window` is a real subprocess — the same shape as `on_project_start`/
`on_project_exit`, run via your `$SHELL` (falling back to `sh`) in the
window's own root — not typed into a pane the way `pre_window` is. That
makes it the place for something a pane command shouldn't block on, like
waiting for a port to open before doing anything else:

```toml
[[windows]]
name = "db"
post_window = "until nc -z localhost 5432; do sleep 1; done"

  [[windows.splits]]
  run = "docker compose up db"
```

A failure warns and continues, same as every other per-window failure — see
[Hooks as wyrm's extension point](#hooks-as-wyrms-extension-point) below.

## `[[windows.splits]]` — the split tree

| Key | Type | Default | Description |
|---|---|---|---|
| `type` | string | — | `h`/`horizontal` or `v`/`vertical`. **Omit** to target the pane created by the previous entry (or the window's first pane) without splitting |
| `size` | int | tmux default | Percentage of space given to the new pane (1–99) |
| `command` | string | — | **Typed** into the pane's shell; entries starting with `#` are comments and skipped |
| `run` | string | — | **Run** as the pane's own process, with no shell under it (below). Mutually exclusive with `command` |
| `root` | string | window root | This pane's working directory, relative to the window's root unless absolute |
| `env` | table | — | Environment variables for this pane and its children, overriding window and session env |
| `children` | list | — | Nested splits, applied inside this entry's pane |
| `zoomed` | bool | `false` | Start with this pane focused and zoomed. `zoom` is an accepted alias. Only one pane per window can be zoomed — the last one that asks wins |
| `remain_on_exit` | bool | `false` | Keep this pane open after its command exits (the per-pane form of the window option) |

### `command` vs `run`

`command` types the text into the pane's shell, exactly as if you had. The
shell stays underneath, so when the program exits you get your prompt back —
which is what you want for `nvim` or a REPL.

`run` makes the command *be* the pane's process. There is no shell, so:

- the pane closes when the command exits (unless you've set tmux's
  `remain-on-exit`), which suits a long-running server you'd rather see die
  loudly than silently;
- the text never lands in your shell history;
- a command starting with `#` is runnable — `command` treats those as comments.

```toml
  [[windows.splits]]
  type = "h"
  run = "npm run dev"       # the pane *is* the dev server
```

Working directories cascade: a pane uses its own `root`, else its window's,
else the session's. All panes get an explicit directory from wyrm, so a session
built by `wyrm <name>` from an unrelated folder is still rooted correctly.

How the tree is walked: each entry with a `type` splits the pane of the
previous entry at the same level (the window's initial pane for the first
entry). `children` do the same, starting from their parent's pane.

Every entry at a level is created before wyrm descends into any of their
`children`, so a `size` is always a share of the space its own level was given
— not of whatever an earlier sibling's children happened to leave behind. This
is what lets `wyrm save` capture a nested layout and rebuild it unchanged.

Give the *first* entry at a level a `type` and it splits the pane it was handed
rather than filling it, leaving that pane an empty shell; wyrm warns when a
config does that. Omit the `type` on the first entry to put it in the pane
itself.

```toml
[[windows]]
name = "dev"

  [[windows.splits]]
  command = "nvim"            # window's first pane

  [[windows.splits]]
  type = "h"                  # split it: new right-hand pane, 30%
  size = 30
  command = "npm run dev"

    [[windows.splits.children]]
    type = "v"                # split the right-hand pane: bottom half
    size = 50
    command = "npm test -- --watch"
```

## `[[windows.panes]]` — legacy flat list

!!! warning "Deprecated"
    The flat `panes` list (and the `.tmuxconfig` filename) are retained for
    backward compatibility. New configs
    should use the `splits` tree, which is strictly more expressive.
    `wyrm save` only ever emits `splits`.

```toml
[[windows]]
name = "tests"
layout = "even-horizontal"

[[windows.panes]]
command = "npm test -- --watch"

[[windows.panes]]
command = "# scratch"          # comment: pane is created, nothing runs
```

Panes split alternately h/v, then `layout` (default `tiled`) evens them out.
