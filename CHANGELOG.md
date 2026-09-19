# Changelog

All notable changes to this project will be documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Fixed

- Validate replacement roots before stopping a session; bulk kill/restart now
  return failure when any requested operation fails.
- Isolate per-pane startup commands from explicit and inherited synchronization
  (an inherited `synchronize-panes` stays inherited afterwards), honor nested pane roots/environments/direct commands, and configure
  `remain_on_exit` before starting fast processes. Apply pane titles to every window.
- Preserve migration roots and ownership when invoked from a child directory or
  with a subdirectory root. Rewrite TOML syntax safely and validate before moving.
- Resolve live sessions without alias hooks from another project; prefer exact
  numeric window names over window indexes. Include attach hooks in dry runs.
- Honor wildcard projects' own configs (still tracked in zoxide) and match sanitized and worktree-derived
  session identities in the TUI. Keep doctor/help/version usable with broken settings.
- Refuse unlocked or unreadable lifecycle-history writes and sync replacement
  executables before installation.
- Run clipboard work asynchronously, bound helper processes, and deliver terminal
  notifications safely with session/window metadata from the agent scan.
- Correct Bash status formats; complete stopped projects and aliases through
  `list-configs -names` in Bash, Fish, and Zsh.

### Changed

- Build documentation strictly on pull requests with pinned Python dependencies;
  deployment permissions are restricted to the deployment job.
- Isolate integration-test tmux servers from personal configuration and plugins.

## [1.2.1] - 2026-09-05

### Fixed
- `wyrm kill <name>` no longer destroys the wrong session when the name is also a configured alias. `killByName` resolved a project or alias before checking for an exact running session, so a project's `aliases` entry colliding with an unrelated live session's exact name killed the aliased project instead of that session — `attachByName` already resolved exact session first, and `kill` now matches it.
- Creating a session whose name contains `|` (which tmux allows) no longer fails after already creating the session, with no ID left to clean it up: `new-session`'s `-F` format put the name between two IDs in a single `|`-delimited response, so a name containing `|` was misparsed as extra fields. The real name is now queried separately from the IDs.
- A relative `session.root` (`root = "."`, the common case) no longer silently resolves against the wrong directory once a config's storage location differs from the project it describes. This affected three places: `wyrm migrate-config` moving a local file into shared storage, `wyrm init` writing a new config directly into shared storage, and the per-user `default.wyrm.toml` override (which resolved against `~/.config/wyrm` instead of wherever `wyrm up` was actually run). All three now anchor the root to the project's real directory.
- The compact picker (`wyrm pick`) no longer panics when opening the context menu (`M`) with the Windows panel focused. It indexed panel geometry by the panel's enum value rather than its position among the panels actually shown, which are the same number only in the full four-panel TUI.
- The TUI's pager search no longer panics on certain Unicode input (e.g. the Kelvin sign, which lowercases to a different byte length than it started with). Search now matches on rune spans instead of assuming a lowercased copy stays byte-aligned with the original. Filter, find-pane, and pager search boxes also now delete a whole character on backspace rather than a single byte, which previously corrupted multi-byte characters like "é" into invalid UTF-8.
- The TUI no longer loses track of which session, window, or pane is selected when a filtered panel loses focus or a periodic refresh reorders the underlying list. Selection used to be a plain index into whatever list was currently visible, so leaving a filtered Sessions panel, or a session simply appearing/disappearing elsewhere, could silently retarget an action (attach, kill, rename) at a different item than the one actually selected.
- `wyrm restart` no longer treats a real failure to stop the existing session (a tmux server or permission problem, as opposed to "nothing was running") as harmless, proceed-anyway noise. It used to report "created" even when the old session was still there because it never actually died.
- `wyrm validate` now resolves `session.root`, every window's root, and every split's root the same way `wyrm up` does, instead of only checking decoded structure. A root referencing an unset environment variable used to pass validation and then fail during the actual build.
- The TUI pager's scrolling, paging, and "G"/end no longer stop one line short of the buffer's actual end. They assumed a viewport one row taller than what the pager box really renders.
- `pre_window` is no longer typed into a pane whose `run` starts a direct process instead of a shell (e.g. `run = "cat"`) — it used to land as literal input to that process. A split's own `root` also now overrides the window's root for its pane even when the window set one too, matching the documented precedence; it was previously ignored whenever the window also had a root.
- `wyrm status --watch` now fails immediately on a bad `-format` (or any other collection error), the same way it does without `--watch`, instead of looping silently forever with no output and a zero exit code. A collection or formatting failure encountered later in the loop is now reported to stderr (rate-limited to once per distinct error) instead of being discarded.
- `on_project_first_start`/`on_project_restart` no longer collide across every directory a `[[wildcard]]` pattern matches. Lifecycle history was keyed by the template config's own directory, shared by every matched directory, so only the first one ever started a project ever saw first-start — every other one was told it had already started. History is now keyed by the project's resolved root instead.
- A malformed window or split root (an unset `$VAR`, say) is now caught before `on_project_start`/`on_project_first_start` run or lifecycle state is recorded, instead of after — fixing the config and retrying used to fire `on_project_restart` even though nothing had actually been built the first time.
- A shared config's disambiguated filename (e.g. `api-123.wyrm.toml`, from two projects sharing a basename) no longer becomes its display name when `session.root` is absolute. It now displays and discovers under the name `Session.Resolve` actually derives from the root — the same name the built session gets — instead of the two disagreeing everywhere the project was shown.
- The TUI no longer silently drops errors from selecting a stale attach target, listing projects, or a project's build/kill hooks. A failed window/pane selection right before attaching, a failed `tmux list-sessions` (which used to make every project display as stopped), and a failed `on_project_start`/`on_project_exit`/`on_project_attach` hook are now all surfaced instead of discarded, without blocking an attach or a list refresh that otherwise succeeded.
- Shell completions cover every declared flag in the *right* command's branch, not just anywhere in the file — the previous check passed as long as a flag appeared anywhere in the file, so a flag present under one verb read as covered for every verb, and it didn't recognize `--var` (a custom flag type) as a flag at all. With the check fixed: bash, fish, and zsh `up`/`restart`/`validate` were missing `-var`; bash `up` was also missing `-n`/`-d`; fish `up`/`restart` were missing `-n` and `kill` was missing `-n`; validate was missing `-strict` everywhere.
- CONTRIBUTING.md said Go 1.24 (go.mod requires 1.25) and referred to `tmux.RunOutputsTolerant`, which doesn't exist — the function is `tmux.RunOutputs`.

## [1.2.0] - 2026-08-28

### Added
- Documentation for three shipped commands that had none beyond their one-line usage entry: `wyrm status` (agent status across sessions, its five output formats, `--watch`, and status-bar wiring), `wyrm send` (targets, `-r`/`-l`, sending text that starts with `-`), and `wyrm setup-tmux` (where `-a` writes, and what it validates).
- `wyrm doctor` checks wyrm's environment and configuration in one place and reports anything broken or silently doing nothing: the tmux binary and whether its version is new enough for the `size` values in a config, the settings file and any keys in it that were ignored, where project configs are looked for, each `[[wildcard]]` pattern and how many directories it matches, the config the current directory resolves to, whether the agent profiles and TUI theme compile, and which optional tools ($EDITOR, a clipboard backend, zoxide) are present. Exits non-zero on errors; `-strict` also fails on warnings. It only reads — nothing is repaired.

### Changed
- Minimum Go is now 1.25. `golang.org/x/crypto` 0.52.0 and its transitive `x/sys` and `x/text` updates all declare `go 1.25.0`, so building wyrm from source needs 1.25 or newer. A `toolchain` directive pins the patch release CI and the release build actually use: `go-version-file: go.mod` installs exactly the version named there, so without it both were built against an unpatched standard library — `govulncheck` only passed on `main` because the old `go` directive was too low to run the scanner, which quietly downloaded a current toolchain instead.
- Discovery no longer re-walks the filesystem on every refresh. `[[wildcard]]` matches are cached for 10 seconds, so a `/**` pattern is not a full recursive walk on each 3-second TUI tick; `R` in the TUI clears the cache when you want a directory picked up now. Discovery also parses each config once per change instead of twice, and `wyrm kill -all` / `wyrm restart -all` run one discovery pass for the whole run rather than one per session — with 20 sessions and a `/**` pattern that was 20 complete tree walks.
- `wyrm status` bounds how many agent panes one scan reads, matching the limit the TUI has always had. Under `--watch` it previously issued one `capture-pane` per agent pane every interval with no ceiling, against the server you are working in. When the bound truncates a scan the remainder is reported (`+N unscanned`, or `summary.skipped` in JSON) rather than silently dropped.
- `wyrm setup-tmux -a` copies your `tmux.conf` to `<path>.wyrm-backup` before appending to it.
- `wyrm send -l` states that text is sent literally, which is the default, instead of being accepted and ignored — it was declared, advertised in `-h`, and never read. Passing `-l` together with `-r` is now an error rather than `-r` quietly winning.
- Agent profile construction lives in one place (`agent.ProfilesFrom`) instead of being duplicated between the TUI and `wyrm status`, which had already begun to drift.

### Security
- `wyrm clone` now shows the shell a freshly cloned repository's config would run and asks before running it, instead of printing a warning that named the flag you needed *before* you ran the command and then executing the hooks anyway. The listing covers every hook and pane command, not just `on_project_start`; a non-interactive run declines rather than proceeding unattended; `-y` skips the prompt.
- `wyrm selfupdate` now **refuses** a release whose `checksums.txt` is unsigned, or signed by a key other than the one compiled in, instead of warning and continuing. A release signing key exists as of this version, so the enforcement branch is reachable for the first time — every build from 1.2.0 onward verifies before it replaces anything.
- Release archives are signed. Verification was previously unreachable: no public key was ever assigned, `.goreleaser.yaml` published no signature, and the checksum-only path could detect a corrupted download but not a tampered release. `checksums.txt` is now signed with minisign, the public key is committed at `internal/selfupdate/signing.pub` and compiled into the binary, and CI signs every pull request with a throwaway key and asserts the signature asset exists. See [SECURITY.md](SECURITY.md) for the one-time key setup.
- Signature verification now handles minisign's prehashed `ED` algorithm, which is what minisign produces by default — a genuine signature would have failed to verify even once a key was embedded. Key IDs are checked, unknown algorithms are rejected, and the parser is tested against artifacts from the real `minisign` binary. A build with a key refuses an unsigned release; a build without one says so rather than implying it verified something.
- `on_project_detach` is no longer subject to tmux's format expansion. `run-shell` expands its argument whatever the quoting, so a hook containing `#{...}` was silently rewritten and `#(...)` was executed by tmux at expansion time. Hooks now reach the shell verbatim.

### Fixed
- A pane `command` ending in `;` is typed in full. tmux treats `;` as its command separator and strips it from the end of an argument before `send-keys` ever sees the text — `-l --` guards against key-name lookup and flag interpretation, but not against this. In a shell the lost `;` is invisible, since a trailing one is a no-op there; in `psql`, `sqlite3`, or any REPL where `;` ends a statement, the statement was typed and then never run. Applies to `wyrm send` and to config-driven pane commands alike.
- `wyrm init` no longer generates a config it then refuses to load. Values were quoted with Go's escaping rather than TOML's, and TOML has no `\a`, `\v`, or `\xNN` — so a session name, window name, or command containing a bell, a vertical tab, a control character, or an invalid UTF-8 byte produced output that failed to parse. It failed closed rather than writing a broken file, but only after every prompt had been answered.
- Two projects whose directories share a name no longer collide in `shared` storage. `~/work/api` and `~/personal/api` both mapped to `api.wyrm.toml`, so the second silently read the first one's config and built a session in the wrong root — routine in a monorepo (`services/api`, `packages/api`). A shared config now belongs to the directory its absolute `session.root` names; the first project to claim a basename keeps the plain filename and nothing on disk moves, while another project with the same basename gets `<folder>-<hash>.wyrm.toml`. `wyrm migrate-config` names the owning directory instead of just refusing.
- `wyrm setup-tmux -a` honours `$XDG_CONFIG_HOME`. It was the one path in the codebase that hardcoded `~/.config`, so anyone with the variable set had the snippet appended to a file tmux does not read, and was told it had worked.
- `wyrm setup-tmux` validates `-key-pick` and `-key-tui` before writing them. `bind-key` fails at config-load time and aborts the rest of the file, so a malformed key broke the user's entire tmux config with nothing to indicate which line did it.
- The state file that records which projects have started is now written durably and without losing concurrent writes. `AtomicWriteFile` never called `Sync`, so a crash could leave a truncated file despite the rename "succeeding", and two `wyrm up` runs starting at the same moment could erase each other's entry — making `on_project_first_start` fire a second time for a project that had already started, the one thing the file exists to prevent.
- Shell completions cover every flag their verbs accept. `-d` was missing from fish for `up` and `restart`, and zsh had no completion at all for `send` or `setup-tmux`. A test now checks the completions against the flags the source actually declares, the way the subcommand lists were already checked.
- `[[wildcard]]` and zoxide projects resolve to the right config everywhere, not just when starting. `wyrm kill <name>`, `wyrm kill -all`, `wyrm restart -all`, and the TUI's `x` on the Projects panel all loaded the config by path and lost the matched directory, so the session name came from the template's own folder: `wyrm kill widget` reported `session "templates" is not running`, and `wyrm restart -all` built a spurious session named after the template directory while leaving the real session untouched. A zoxide project, which has no config file at all, could be started but never stopped. All paths now go through one resolver, `config.Project.LoadConfig`.
- `wyrm status -w -interval 0` panicked with `non-positive interval for NewTicker`. A non-positive interval is now a usage error (exit 2).
- `on_project_attach` no longer runs when wyrm does not attach. `wyrm up -d`, `wyrm restart -d`, and `wyrm restart -all` all build a session and hand it to nobody, and the hook fired for all three.
- `wyrm send` can send text starting with `-`. The missing `--` terminator meant tmux read `wyrm send sess "-n hello"` as send-keys flags, and no spelling of the command worked. A leading `--` between the target and the text is also accepted and stripped.
- The TUI's split-pane prompt accepts a command starting with `-`, for the same reason.
- `wyrm up -n` on a config with `startup_window` no longer warns `unexpected window index` and no longer omits the `select-window`/`select-pane` commands it would run. The dry-run runner now answers `list-windows`/`list-panes` from the layout the transcript just built.
- The TUI no longer writes terminal bell and OSC 9/777 notification escapes to stdout from a background goroutine while Bubble Tea owns the screen, which could corrupt the display. Desktop and custom-command notifications are unaffected.
- Copying in the TUI reports what actually happened. `clipboard.Write` returned `nil` when no clipboard tool was installed, and every call site announced "copied ..." regardless — so on a headless host `y` silently did nothing. It now names the tools to install.

## [1.1.1] - 2026-08-28

### Added
- `a` toggle in the TUI's Sessions panel: lists known-but-stopped projects alongside the running sessions, with `Enter` starting a stopped one. Gives `wyrm pick`, which has no Projects panel, a way to start a session rather than only attach to one.

### Fixed
- A `[[wildcard]]` match no longer duplicates a project that has its own config: a pattern such as `~/code/*` covers the directory you are standing in as well as its siblings, and the specific config now wins over the template.

## [1.1.0] - 2026-08-16

### Added
- `wyrm setup-tmux` subcommand to generate or append recommended tmux popup bindings (`display-popup`) and status bar configuration with duplicate detection.
- `wyrm send` subcommand for non-interactive command and keystroke execution with dot-safe target resolution (`session[:window[.pane]]`), literal (`-l`), raw key (`-r`), and no-enter (`-n`) flags.
- Session lifecycle hooks `on_project_attach` (runs whenever attaching to a fresh or existing session) and `on_project_detach` (tmux `client-detached` hook).
- Window reordering (`<` / `>`, `K` / `J`) and cross-session window moving (`W`) directly within `wyrm tui` with modal session picker and context menu integration.
- Pane split actions directly within `wyrm tui`: split panes vertically (`s` / `v`) or horizontally (`S` / `h`) with optional startup commands and context menu integration.
- `wyrm status --watch` (`-w`) mode with `--interval` flag to continuously stream agent status updates for Waybar, Sketchybar, and Tmux status bars.
- Bulk session operations: `--all` (or `-a`) and `--yes` (or `-y`) flags for `wyrm kill` and `wyrm restart` to gracefully stop or rebuild all active tmux sessions.
- Upward directory traversal to automatically discover `.wyrm.toml` at Git repository or project root from nested subdirectories, with configurable `[discovery].upward` setting (enabled by default).
- Support for per-window (`[windows.env]`) and per-split (`[windows.splits.env]`) environment variables, cascaded with precedence: Split > Window > Session.
- `--stdout` (or `-o -`) and `-n` / `--dry-run` flags in `wyrm save` to stream generated TOML directly to stdout or preview save results without touching disk.
- Capture pane working directories (`pane_current_path`) during `wyrm save` and `freeze`, preserving relative and absolute directory paths across windows and splits.
- `wyrm init` interactive configuration wizard and starter templates (`node`, `python`, `go`, `rust`, `monorepo`, `minimal`). Supports `--template` / `-t`, `--force` / `-f`, and `--config` flags.

## [1.0.0] - 2026-08-15

### Added
- Template variables via `--var` flags for dynamic config variable interpolation (`{{ .var.name }}`).
- `wyrm status` command to display agent state across sessions with support for multiple output formats (`text`, `json`, `tmux`, `waybar`, `sketchybar`).
- TUI pager mode for viewing scrollback history with interactive search (`/`, `n`, `N`).
- Clipboard copy support in TUI with `y` key.
- Pane configuration options: `synchronize-panes`, `remain-on-exit`, and `zoom`.
- Cryptographic Ed25519 signature verification for release checksums (`checksums.txt.sig`) in `wyrm selfupdate`.
- `-no-start` flag in `wyrm clone` to clone without executing lifecycle hooks, and interactive confirmation prompt before executing untrusted post-clone hooks.

### Changed
- Improved `$EDITOR` parsing to safely handle quoted paths and arguments containing spaces.
- Hardened `interpolateString` template variable expansion using a deterministic boundary scanner.
- Switched `~/.config/wyrm/state.toml` persistence and config writes to atomic temporary-file renames.
- Enforced 30-second default request timeout on `wyrm selfupdate` HTTP client.
- Pinned all third-party GitHub Actions in CI/CD workflows to immutable commit SHAs.

### Fixed
- Sanitized terminal escape sequences (OSC/ANSI control characters) in agent desktop and terminal notifications.
- Fixed command injection risks in Windows toast notification script building and custom notification command execution.
- Fixed Git argument injection vulnerability in `wyrm clone` by explicitly delimiting flags with `--`.
- Prevented decompression bomb and unbounded memory consumption in `wyrm selfupdate` by enforcing strict file and archive size limits.
- Prevented potential recursive walk denial-of-service in wildcard configuration matching by skipping hidden and dependency directories (`.git`, `node_modules`, `vendor`, `.venv`, etc.).
- Handled tmux race condition when the tmux server exits unexpectedly during last session teardown.

## [0.8.0] - 2026-08-02

### Added
- `[tmux]` global settings (`socket`, `command`, or `WYRM_TMUX_SOCKET`/
  `WYRM_TMUX_COMMAND`) to target a non-default tmux server or a different
  binary (a wrapper/fork like `byobu` or `psmux`). Every tmux invocation for
  the run, including the final `attach-session` handoff, honors it.
- `-d` on `wyrm up` and `wyrm restart`: build (or reattach to) the session
  without attaching.
- `session.on_project_first_start` / `on_project_restart`: alongside
  `on_project_start`, which always fires, exactly one of these now also
  fires — distinguishing a project's genuine first-ever start from a later
  one. Tracked in a new `~/.config/wyrm/state.toml`, so it survives a
  `wyrm kill` and holds across process runs.
- `session.enable_pane_titles` (plus `pane_title_position`/`pane_title_format`):
  turns on tmux's live pane-border status line for the session.
- `[[wildcard]]` global settings: apply one template config to every
  directory matching a glob pattern (with a `/**` recursive form), instead of
  a `.wyrm.toml` per project. Matches are discoverable by `wyrm <name>`,
  `wyrm list-configs`, and the TUI's Projects panel (marked `~`) the same as
  any other project.
- `session.aliases`: additional exact-match names `wyrm <name>` resolves to a
  project, alongside its session name — an exact project name always wins
  over an alias collision.
- `wyrm clone <repo> [dest]`: runs `git clone`, then builds (and attaches to)
  a session for the result — a `[[wildcard]]` template if the destination
  falls under one, otherwise whatever a bare `wyrm up` from inside it would
  resolve. Requires `git` on `PATH`, wyrm's second (and still explicit,
  opt-in) dependency on a binary other than tmux.
- A linked git worktree's session name is now derived from both the main
  repository and the worktree's own directory (`wyrm-feature-x`, not just
  `feature-x`) when `session.name` is unset — read directly from `.git`'s
  own pointer file, not by shelling to git, so every `wyrm up` stays on the
  zero-dependency naming path.
- `[zoxide]` global settings (`enabled`, `track`; both default `false`):
  lists directories from your zoxide/`cd` history in `wyrm tui`'s Projects
  panel (marked `z`) alongside real wyrm projects, gracefully absent unless
  both the setting and the `zoxide` binary are present. `track` calls
  `zoxide add` after building a session.
- `windows.post_window`: a shell command run (not typed) once a window's
  panes and their commands all exist — for something a pane command
  shouldn't block on, like waiting for a port to open. wyrm has no plugin
  system by design; this rounds out the hooks that already cover what one
  would typically be for.
- `f` in `wyrm tui`: a searchable, whole-server pane list — the flat view
  tmux's own `choose-tree -Z` gives you, which the Sessions → Windows →
  Panes hierarchy doesn't. Type to narrow (session, window, and command all
  match), `Enter` attaches directly. Full TUI only.

### Fixed
- `tmux.Attach` ignored `SocketName` entirely — a session built on a named
  socket via `[tmux].socket` would have attached to the default server
  instead. It's now a method on `Exec`, like every other tmux call.
- The global settings file (`~/.config/wyrm/config.toml`) silently dropped
  unknown top-level keys; a typo like `[[widcard]]` now surfaces as a
  warning, matching how project configs already report typos.

## [0.6.2] - 2026-08-01

### Added
- A `wyrm(1)` man page, installed by the Homebrew formula and the new
  `.deb`/`.rpm` packages built via `nfpm`.
- Native Linux packaging: tagged releases now build `.deb` and `.rpm`
  packages (with the completions and man page baked in) alongside the
  existing tarballs, and publish a `wyrm-bin` package to the AUR.

### Changed
- Internal: the TUI's panels are described by one table (`internal/tui/panels.go`)
  instead of nine parallel switch statements spread across four files. Title,
  row count, rows, footer hints, context menu, what `x` kills, which panel it
  feeds, and what to reload were each a separate four-case switch; adding a
  panel meant finding all nine. The four `*Cur` fields likewise became one array
  indexed by panel — two of the nine switches (`cursorFor` and `focusedCursor`)
  were the same switch written twice. `Model` drops from 42 fields to 36.
- tmux errors now lead with tmux's own diagnostic instead of the process exit
  status: `creating session: duplicate session: web` rather than
  `creating session: exit status 1 (duplicate session: web)`. The exit status was
  the one part of that message nobody could act on, and it came first.
- Internal: the three `list-*` parsers in `internal/tmux` shared a hand-copied
  split-and-validate loop. They now share one, which also closed a gap —
  `ListAllPanes` was the only parser that skipped `CheckID`, on exactly the pane
  IDs the agent scan then uses as `capture-pane` targets.
- Internal: `session.Create` was 121 lines with a 55-line if/else fork inside its
  window loop; the two branches are now `newSession` and `newWindow`. The
  `splitCtx` value introduced in 0.6.0 was only threaded through one of the three
  builder functions — `buildWindow` and `applyPanes` now take it too, dropping
  `applyPanes` from nine parameters to six.
- Internal: `agent.State.NeedsUser` is now the single definition of which states
  earn a marker; the TUI re-encoded the same rule in a parallel switch.

### Fixed
- A failure to resolve a config's absolute path was silently ignored, leaving
  relative `session.root` values to resolve against the process's working
  directory — the same class of bug as the split-pane roots fixed in 0.6.0.
- The pane-directory integration tests read `#{pane_current_path}` immediately
  after building a session, which tmux has not always populated yet; under the
  load of a full parallel test run this failed roughly one run in twenty. They
  now wait for it.

## [0.6.1] - 2026-07-31

### Changed
- Building a session issues far fewer tmux processes. Commands are collected
  while the layout is walked and typed in a single `tmux` invocation at the end,
  instead of two processes per pane. Measured against a real server on a
  three-window, six-pane config: **20 processes → 9**.
- The TUI's agent scan reads every candidate pane in one tmux process rather
  than one per pane — up to 16, every three seconds, for as long as the TUI is
  open.
- Batching is opt-in per Runner (`tmux.BatchRunner`), so the dry-run recorder
  and every test double keep working unchanged, and `wyrm up -n` still prints
  one line per tmux command.
- tmux abandons a batch at its first failure, which would silently cancel every
  command behind a single dead pane. Commands the batch never reached are
  replayed individually, preserving the warn-and-continue policy; commands that
  already succeeded are never re-issued, since replaying a `send-keys` would
  type its text a second time.

### Fixed
- `go.mod` still required `golang.org/x/term` after `internal/picker` was
  removed in 0.6.0. CI now fails on an untidy `go.mod`.

## [0.6.0] - 2026-07-31

### Fixed
- **Split panes ignored `session.root`.** `split-window` was issued without an
  explicit `-c`, so tmux started those panes in the *invoking client's* working
  directory rather than the session's. Only each window's initial pane, created
  by `new-session`/`new-window -c`, was ever in the right place. Anyone running
  `wyrm <name>` for a project outside the current folder — the thing shared
  config storage exists for — got a session whose split panes sat wherever they
  happened to be standing. Every pane now gets an explicit directory.
- `wyrm up -n` executed the `on_project_start` hook for real. A dry run exists
  so an unfamiliar config's shell can be read before it runs, and the hook is
  the part most worth reading first; a recording tmux runner covered the tmux
  commands, but hooks never went through it. Hooks are now printed as
  `# would run on_project_start: ...` and not executed.
- Renaming a session or window to a name starting with `-` failed with
  "unknown flag" instead of doing the rename: the new name is now passed after
  a `--` terminator.
- An agent pane whose screen matches none of the detector's patterns is no
  longer reported as "idle". It used to reach idle by elimination, so an agent
  whose UI had changed — or one added to `tui.agent.commands` that the detector
  has no patterns for at all, such as `aider` — showed a confident "finished,
  come look" marker permanently. Such panes now carry no marker.

### Added
- **Per-window and per-split `root`.** A relative path resolves against its
  parent, so a monorepo can say `root = "api"` on a window and have it open
  there. The only way to express this before was `pre_window = "cd api"`, which
  types a visible `cd` into every pane of the window and races that pane's own
  command. See `examples/monorepo.wyrm.toml`.
- **`run` on a split**, making the command the pane's own process instead of
  typing it into a shell. No shell underneath means the pane closes when the
  command exits (good for a dev server you'd rather see die loudly), the text
  never enters shell history, and a command starting with `#` is finally
  runnable. Mutually exclusive with `command`, which is rejected rather than
  silently resolved.
- **`[session.env]`**, environment variables passed to every window and pane as
  it is created. Requires tmux 3.2+; everything else still works on 3.1+.
- **`[[tui.agent.profiles]]`**, describing another agent's on-screen chrome so
  the markers work for something other than Claude Code. `tui.agent.commands`
  now means only "also inspect these commands with the built-in patterns", which
  is what a wrapper script needs; a profile is what a different agent needs. A
  profile with no commands or an uncompilable pattern is an error reported
  before the TUI takes the screen, not a silent fallback.
- `wyrm restart -n` and `wyrm kill -n` dry-run the teardown half, printing the
  `on_project_exit` hook and the `kill-session` without running either. The
  session lookup still happens for real, since it is what names the target.
- Config keys wyrm doesn't recognise are reported as warnings. A misspelled key
  is dropped silently by any TOML parser, so a config whose every key was a typo
  passed `wyrm validate` — the exact mistake validate exists to catch.
- `wyrm validate -strict` exits non-zero when the config has any warnings
  (unknown keys, deprecations), for use in CI.

### Changed
- **`wyrm pick` is now the same program as `wyrm tui`**, in a compact two-panel
  form (Sessions over Windows) with the filter already open. It was previously a
  separate hand-rolled raw-terminal UI — its own escape-sequence decoder, frame
  renderer, signal handling and key names — offering a strict subset of what the
  TUI already did. Consequences:
  - The bindings are the TUI's: `x` kills (was `Ctrl-X`), the Windows panel
    replaces the `Ctrl-W` drill-down, `Esc` clears the filter (was `Ctrl-U`).
    `?` now shows a full key reference, and `pick` gains rename, layout cycling,
    the live pane preview, mouse support, and the agent markers.
  - `Enter` still attaches in one press: in compact mode it commits the filter
    *and* activates, unlike the full TUI where `/` is one tool among several.
  - `pick` now honors `theme.toml`. `NO_COLOR` is handled by Lipgloss rather
    than by wyrm's own code.
  - With no sessions running it still prints one line and exits 0 rather than
    opening a full-screen program onto an empty list.
- Internal: the session-listing data layer moved from `internal/picker` to
  `internal/sessions` (`List`, `Kill`, `FuzzyMatch`, `FormatRow`), so the TUI no
  longer imports a UI package to get at a struct. `internal/picker` is gone —
  about 690 lines of terminal handling deleted, with coverage up rather than
  down.
- Internal: the CLI's verbs moved out of `main.go` into `cmd_session.go`,
  `cmd_config.go`, and `cmd_ui.go`, and each now returns an error instead of an
  exit code. One place turns that into a message and a status, replacing the 37
  hand-written copies of it. No behaviour change; the full CLI test suite is
  unchanged.
- Internal: "no tmux server is running" is now carried by a wrapped
  `tmux.ErrNoServer` sentinel rather than being re-derived by matching tmux's
  English diagnostic at each call site. The text match remains as a fallback for
  Runners other than the real one.
- Internal: errors are wrapped with `%w` rather than formatted with `%v`, so the
  chain survives; `errorlint` now enforces it.
- The TUI's refresh tickers now idle while the terminal is unfocused, and
  refresh at once when focus returns. A session manager in a background tab was
  spending a `capture-pane` every second and a full list sweep every three,
  forever, for a screen nobody was looking at.
- Project discovery memoizes each config's session name by (size, mtime) instead
  of re-reading and re-parsing every config on disk. The TUI runs discovery on a
  3-second timer, so twenty shared projects meant twenty file reads and twenty
  TOML parses every three seconds. An edit is still picked up on the next tick.
- CI additionally runs `go test -short` (the no-tmux path the Makefile
  advertises), `govulncheck`, and a coverage floor — the coverage number was
  previously printed and discarded. `gosec`, `errorlint`, `copyloopvar`,
  `nilerr`, and `usetesting` were added to the linter set.

## [0.5.1] - 2026-07-31

### Added
- `M` opens the context menu on the current selection, from the keyboard. The
  menu was previously reachable only by right-click, which some terminals never
  deliver: iTerm2 (among others) keeps the right button for its own context menu
  and doesn't forward button 3 to the application, so the menu was unreachable
  there regardless of tmux's settings.

## [0.5.0] - 2026-07-31

### Added
- The TUI is now driveable with the mouse, on by default. Click to focus a
  panel and select a row, double-click to attach (or start a project from the
  Projects panel), and scroll the panel under the pointer with the wheel.
- Right-clicking a row opens a context menu of the actions for that panel —
  attach, rename, new window, cycle layout, zoom, kill — targeting the row
  under the pointer rather than the previous selection. It takes the keyboard
  (`↑`/`↓`, `Enter`, `Esc`) as well as the mouse, and shows each action's key
  binding so it teaches the shortcuts instead of replacing them.
- `m` toggles mouse capture for the current run, and `[tui] mouse = false` in
  `~/.config/wyrm/config.toml` disables it permanently — capturing the mouse
  takes click-drag text selection away from the terminal, so it has to be
  surrenderable.
- Sessions, windows, and panes running an AI coding agent are now marked with
  what that agent needs: `⏸` when it's stopped on a prompt it can't answer
  itself, `✓` when it has finished its turn and is waiting for the next
  instruction. A working agent is deliberately unmarked. Windows and sessions
  take the state of their most urgent pane, so a marker on a session means
  something inside it wants attention. Configurable through `[tui.agent]`
  (`enabled`, `commands`); recognises `claude` out of the box.
- Two new theme roles, `blocked` and `idle`, for those markers.

## [0.4.1] - 2026-07-25

### Fixed
- `wyrm edit` and the TUI's `e` no longer fall back to `vi` when `$EDITOR` is
  missing from the environment. A pane the tmux server launches inherits the
  server's environment, which never sourced the shell rc files that export
  `$EDITOR`, so a user with `EDITOR=nvim` still got dropped into `vi`. Editor
  resolution now asks the login shell before giving up, and is shared by both
  entry points so they can't drift.

## [0.4.0] - 2026-07-25

### Added
- The TUI's colors can be themed from `~/.config/wyrm/theme.toml`
  (`$XDG_CONFIG_HOME/wyrm/theme.toml` if set). Nine roles — `accent`,
  `subtle`, `filter`, `selected`, `text`, `trail`, `index`, `active`, `error`
  — each an optional `#rgb`/`#rrggbb` value layered over the built-in default.
  A misspelled role or an unparseable color fails with a message naming it,
  rather than being dropped in silence.

### Changed
- The TUI now ships a [Nord](https://www.nordtheme.com) theme: frost-blue
  focused borders, polar-night blurred ones, teal window indices, green status
  dots, aurora-red errors. The selection is a background band instead of
  reverse video, so a row's own colors survive being selected, and the focused
  panel switches to an aurora-yellow accent — border, title, and footer prompt
  — while a filter is active.

## [0.3.1] - 2026-07-25

### Changed
- `wyrm tui` now opens with the **Sessions** panel focused instead of
  Projects, so the first preview is a live pane rather than a config file.

## [0.3.0] - 2026-07-25

### Added
- `wyrm restart` stops the session and builds it again from the current
  config — previously `wyrm kill && wyrm`.
- `wyrm <name>` now falls back to *starting* a known project when no session
  by that name is running, looking through the local and shared configs. This
  is what makes `storage = "shared"` useful: any project can be started from
  anywhere, without `cd`-ing to it first.
- `wyrm kill <name>` kills a session by name, running its `on_project_exit`
  hook when wyrm can find the matching config. Previously `kill` could only
  target the current folder's session.
- `wyrm up -n` prints the tmux commands a build would issue, without running
  any of them or consulting a running session.
- `wyrm` reports which config it resolved (on stderr) before building.
  Discovery has five layers; an unexpected session was otherwise unexplainable.
- Config validation now emits warnings for config that builds but probably
  doesn't do what was meant: `layout` set alongside `splits` (it's ignored),
  `splits` and `panes` both set, a first split entry with a `type` (it leaves
  the window's initial pane empty), and use of the deprecated flat `panes`
  list or the `.tmuxconfig` filename.
- The TUI gained `/` to filter the focused panel, `PgUp`/`PgDn`/`g`/`G`
  navigation, and a 3-second refresh of the project and session lists.
- `wyrm save -config PATH` writes to PATH instead of the resolved location,
  for symmetry with `up`/`kill`/`validate`/`edit`.
- A mistyped subcommand that falls through to attach-by-name and finds no
  matching session now hints at the nearest real subcommand, e.g.
  `wyrm klil` suggests `kill`.

### Changed
- **BREAKING:** nested splits are now created breadth-first — every entry at a
  level is created before wyrm descends into any of their `children`. A
  `size` is therefore a share of the space its own level was given, rather
  than of whatever an earlier sibling's children happened to leave behind.
  This makes `wyrm save` → `wyrm` a faithful round trip for nested layouts,
  which it was not: a container that wasn't the last sibling rebuilt to a
  visibly different window. Hand-written configs that were tuned against the
  old sequential behavior may need their `size` values revisited.
- **BREAKING:** `session.root` now expands a leading `~`, and errors on an
  undefined `$VAR` instead of silently expanding it to an empty string.
  `root = "~/code/app"` previously produced a literal directory named `~`,
  and `root = "$UNSET/api"` silently became `/api`.
- **BREAKING:** a relative `session.root` resolves against the directory the
  config was loaded from, not the process's working directory. Starting a
  project from the TUI (or now `wyrm <name>`) rooted the session wherever the
  user happened to be standing rather than at the project.
- **BREAKING:** a config with no `[[windows]]` is now a validation error.
  `wyrm validate` used to bless configs that `wyrm` would then refuse.
- **BREAKING:** `wyrm list` with an unknown `-format` exits 2, like any other
  bad flag value, rather than 1.
- Subcommands now reject stray positional arguments. `wyrm list json` — which
  looks like it should work — silently printed the default table format.
- `wyrm pick`: `Ctrl-X` now asks for confirmation before killing a session,
  matching the TUI. `Delete` no longer kills a session at all: it was
  unconfirmed, undocumented, and sat one key away from Home and End.
- **BREAKING:** the CLI moved from top-level flags to git-style subcommands.
  `wyrm -kill` → `wyrm kill`, `-pick` → `pick`, `-tui` → `tui`, `-save` →
  `save`, `-edit` → `edit`, `-validate` → `validate`, `-list` → `list`,
  `-list-configs` → `list-configs`, `-migrate-config` → `migrate-config`,
  `-version` → `version`. Per-mode flags now sit on their subcommand
  (`wyrm kill -config PATH`, `wyrm list -format json`). Bare `wyrm`,
  `wyrm <name>`, and `wyrm -config PATH` are unchanged, and a new `wyrm up`
  spells out the default build/attach. As a side effect, modes can no longer
  be combined or silently ignored — each subcommand parses only its own flags,
  and `wyrm help` / `wyrm <cmd> -h` document them.
- Lifecycle hooks (`on_project_start` / `on_project_exit`) now run via `$SHELL`
  (falling back to `sh`) instead of a hardcoded `bash`, so they work on systems
  without bash and honor the user's shell.
- Each subcommand's `-h` now prints its one-line synopsis before its flags,
  instead of a bare stdlib flag dump with no context.
- `wyrm version`'s unstamped-build format changed from `dev (rev)` to
  `dev+rev`, matching semver's `+build-metadata` convention so tooling that
  greps for a semver-ish token isn't thrown by the parentheses.

### Fixed
- A parse error in the user's `~/.tmux.conf` no longer corrupts the session.
  `new-session` is the command that starts the tmux server, so the server's
  config errors landed on stderr at exactly the moment wyrm was parsing that
  command's `-F` output — and the field count still matched, so the check
  passed and every later command targeted a session ID with a diagnostic glued
  to the front of it. wyrm now reads stdout only, and validates every tmux
  object ID it parses.
- A freshly built session opens on its **first** window, focused on that
  window's first pane, as documented. Windows and splits were created without
  `-d`, so tmux made each new one current and the session landed on the *last*
  window in the config.
- `pre_window` now runs exactly once in every pane of the window, as
  documented. It was emitted per split-tree *entry*, which both skipped panes
  (a first entry with a `type` left the window's initial pane untouched) and
  repeated itself (a nested container reuses its parent's pane, so a two-level
  nest typed it twice).
- Pane commands are sent with `send-keys -l --`, so a command that happens to
  be a tmux key name is typed rather than pressed. `command = "up"` used to
  press the Up arrow and Enter, re-running the previous shell history entry;
  a command starting with `-` was parsed as a flag.
- A build that fails partway through now tears the half-built session down.
  It used to be left running, so the next `wyrm` found it, reported "already
  running, attaching", and handed over a session missing most of its windows
  with no sign anything had gone wrong.
- wyrm now reports the session name tmux actually assigned. Some tmux builds
  rewrite `.` and `:` to `_`, so a project in a directory called `example.com`
  became the session `example_com` — after which the next run couldn't find
  it, tried to create a duplicate, and failed, and `wyrm kill` could never
  find it at all. Session lookup also falls back to the sanitized form.
- `wyrm save` no longer records an idle shell as a pane's command. Replaying
  `zsh` into a shell nested a second one in every idle pane, so leaving the
  session took two exits per pane.
- `wyrm save` writes an absolute `session.root` (and says so) when the
  session's own directory isn't where the config is being written, instead of
  always writing `.`.
- The shared config directory now honors `$XDG_CONFIG_HOME`, like the settings
  file next to it. The default was hardcoded to `~/.config/wyrm/settings`, so
  a user with `XDG_CONFIG_HOME` set had wyrm read settings from one place and
  look for shared configs in another.
- `wyrm tui`: the selected row is highlighted across its whole width. lipgloss
  ends every styled run with a full SGR reset and doesn't restore the outer
  style, so wrapping a row in reverse-video switched it off at the first
  colored span — the window index was highlighted and the name next to it
  wasn't, on every row of the Windows and Panes panels.
- `wyrm tui`: failed actions are reported in the footer. Errors rendered only
  when the preview happened to be empty — which in normal use it never is — so
  a failed kill, rename, or new-window looked identical to a successful one.
- `wyrm tui`: the pane preview shows the *end* of the pane's visible region
  rather than the beginning, so the prompt and latest output are visible.
- `wyrm tui`: terminals between 8 and 16 rows no longer render more lines than
  the screen has, which silently sliced the top panels away — including in the
  `display-popup -h 80%` recipe the README suggests. The minimum size is now
  enforced and reported accurately.
- `wyrm tui`: panel heights are allocated by content instead of in equal
  quarters, which gave the Panes panel as much room as Sessions and left every
  list showing two rows at a time in an 80x24 terminal.
- `wyrm tui`: the selection is visible in unfocused panels, so the session and
  window a pane belongs to stay readable while the cursor is elsewhere.
- `wyrm tui`: `n`, `z`, and `L` only act on the panels whose footer advertises
  them; they previously fired from any panel, acting on a selection in a panel
  that didn't have focus.
- `wyrm tui`: `Enter` no longer confirms a kill — `x` followed by a reflexive
  `Enter` destroyed a session. Only `y` confirms.
- `wyrm tui`: the layout cycle (`L`) restarts per window, so the first press
  on a window always changes something. The rename prompt has a width, so a
  long name no longer scrolls the cursor off the screen.
- `wyrm pick`: `SIGTERM`/`SIGHUP` restore the terminal instead of leaving it in
  raw mode with the cursor hidden — reachable via `pkill wyrm`, closing the
  terminal, or killing the tmux pane it ran in. `SIGWINCH` now redraws.
- `wyrm pick`: navigation keys no longer leak into the fuzzy filter. Only the
  first byte of an escape sequence was consumed, so Home, End, PgUp, and PgDn
  each typed a `~`, and a shifted arrow typed `;2A`.
- `wyrm pick`: session names are padded and truncated by display width rather
  than rune count, so a CJK or emoji name doesn't break the column alignment
  and a long name no longer runs over the window count and attached marker.
- `wyrm edit -config new/dir/x.toml` creates the parent directory, as the
  flagless form already did, instead of handing the editor an unwritable path.
- `wyrm edit` returns 1 rather than 255 when the editor is killed by a signal.
- Lifecycle hook output is streamed to stderr and the hook is announced before
  it runs. Output was captured and discarded unless the hook failed, so a slow
  `on_project_start` blocked wyrm behind a blank screen.
- `startup_window` / `startup_pane` are now resolved to tmux object IDs
  (`@window`, `%pane`) via `list-windows`/`list-panes` before being targeted,
  instead of being interpolated into a `session:window.pane` string. A window
  name containing `.` (e.g. `app.web`) was previously misparsed by tmux's `-t`
  syntax — the same hazard already guarded against for session names.
- `wyrm tui`: pane-preview colors are preserved (`capture-pane -e`), so the
  live preview reflects the pane's real colors instead of flattening them.

### Deprecated
- The flat `[[windows.panes]]` list and the `.tmuxconfig` filename are slated
  for removal in 1.0. Use the `splits` tree and `.wyrm.toml`; `wyrm save` only
  emits `splits`.

### Internal
- Project discovery moved into `internal/config` and is now shared by the TUI's
  Projects panel and `wyrm <name>` instead of living only in the TUI.
- The "no server running" probe and the tmux ID shapes are defined once in
  `internal/tmux`, rather than duplicated between it and `internal/picker`.
- CI runs `-race`, reports coverage, and builds the release artifacts on every
  PR so a broken `.goreleaser.yaml` is caught before tag time.
- New tests cover the areas that couldn't previously fail: pane *geometry* for
  nested splits (including a `save` → `up` round trip against a real tmux),
  the selection highlight (rendered with a forced color profile, since lipgloss
  degrades to plain text in a non-TTY test process), and the session-level
  kill/rename actions, which had no coverage anywhere.
- The three shipped examples that taught the deprecated flat `panes` format
  now use the `splits` tree; `basic.wyrm.toml` keeps it as a labelled
  reference. A test loads every shipped example so they can't rot.
- Shell completions (bash/zsh/fish) rewritten for the subcommand surface.
- Added Dependabot (Go modules + GitHub Actions, monthly, grouped).
- `wyrm version` on an unstamped `go install` now reports the VCS revision from
  the build info instead of a bare `dev`.

## [0.2.1] - 2026-07-24

### Fixed
- `wyrm -tui`: the `?` help overlay no longer runs off the top of the screen
  when it's taller than the terminal (only the bottom was visible, with no way
  to reach the rest). It now scrolls — `↑`/`↓` or `j`/`k`, `PgUp`/`PgDn`,
  `g`/`G`, `Esc` to close — with a position indicator, and lays its sections out
  in two columns (collapsing to one on a narrow terminal) so it fits without
  scrolling in more cases.

## [0.2.0] - 2026-07-24

### Added
- `wyrm -tui`: a full-screen, keyboard-driven session manager in the spirit of
  lazygit. Four stacked panels — Projects, Sessions, Windows, Panes — drive a
  live preview of the selected pane (`capture-pane`, refreshed each second), or
  the selected config's contents on the Projects panel. Navigate with
  `Tab`/`1`-`4` and `j`/`k`; `Enter` attaches, landing on the exact window/pane
  under the cursor (or starts/attaches a project's session). Manage tmux
  directly: `x` kills the focused session/window/pane (with a confirm), `r`
  renames, `n` opens a new window, `L` cycles the window layout, `z` toggles
  pane zoom. On the Projects panel, `e` edits a config in `$EDITOR` and `x`
  stops a session running its `on_project_exit` hook. Press `?` for a
  full-screen overlay of every binding. Like `-pick`, it uses `switch-client`
  inside tmux and `attach-session` otherwise, and suppresses the preview of the
  pane it is itself running in. Shell completion now offers `-tui`.

## [0.1.12] - 2026-07-24

### Fixed
- `wyrm -pick`: fixed the picker leaving a trail of stale header lines that
  marched down the screen as you navigated inside a narrow tmux
  `display-popup`. An over-long row (such as the footer) wrapped onto a
  second physical line, desyncing the renderer's line count from the rows on
  screen so each redraw's cursor reposition undershot. The picker now
  disables terminal autowrap while running, clipping long rows at the right
  margin instead. This is the real cause of the popup rendering issue 0.1.11
  attempted to address; the synchronized-update (DEC 2026) change from 0.1.11
  is reverted, as it did not fix the problem.

## [0.1.11] - 2026-07-24

### Fixed
- `wyrm -pick`: fixed screen jitter when the session list is taller than
  the picker's viewport, most visible inside a small tmux `display-popup`.
  The frame now reserves the terminal's bottom row so its trailing newline
  no longer scrolls the display on every keypress, and each repaint is
  wrapped in synchronized-update mode (DEC 2026) so it presents atomically.

## [0.1.10] - 2026-07-18

### Added
- `wyrm -pick`: `Ctrl-W` on a selected session shows its windows (names
  only) so you can jump straight to one — `Enter` selects a window and
  attaches or switches directly to it, `Esc` backs out to the session list.
  `Ctrl-C` now always quits the picker outright, from either view.

## [0.1.9] - 2026-07-18

### Added
- `wyrm -save`: snapshot a running tmux session's windows, split layout,
  and sizes into a new config for the current folder — the reverse of
  building a session from one. Each split's `command` is captured from
  whatever program is currently running in that pane's foreground, since
  tmux keeps no record of what was originally typed. Like
  `-migrate-config`, it refuses to overwrite an existing config.

### Changed
- A bare `wyrm` with no config file now always builds (or attaches to) a
  session for the current folder, even if unrelated tmux sessions are
  already running elsewhere. Previously it opened the interactive picker
  instead whenever *any* session was running; that's now only triggered
  explicitly via `-pick`.

## [0.1.8] - 2026-07-13

### Added
- Dynamic shell completion for bash, zsh, and fish (`completions/`),
  installed automatically by the Homebrew formula. Completes flag names,
  `-format`'s values, `-config` (real local/shared config paths), and a
  bare argument (real running session names).
- `wyrm <name>`: attach or switch directly to a running session by exact
  name, without the interactive picker — what shell completion completes a
  bare argument to.
- `-list -format names`: bare newline-separated session names, for
  completion and scripting (e.g. piping into `fzf`).
- `-list-configs`: list candidate config file paths (local + shared
  directory), regardless of the current `storage` setting.

## [0.1.7] - 2026-07-13

### Added
- Color in `wyrm -pick`: window counts in cyan, `(attached)` in green.
  Respects [`NO_COLOR`](https://no-color.org) to disable it.

## [0.1.6] - 2026-07-12

### Fixed
- CI: fixed a lint error (unused test parameter) and made the dotted-
  session-name integration tests added in 0.1.5 tolerant of tmux builds
  that sanitize or reject `.` in session names outright instead of
  preserving it, rather than failing on them. No functional changes; this
  release is otherwise identical to 0.1.5.

## [0.1.5] - 2026-07-12

### Added
- `wyrm -edit`: open the resolved config (local, shared, or `-config`) in
  `$EDITOR`, creating one at the right location for your `storage` setting
  if none exists yet. Warns (without failing) if the saved file doesn't
  validate.
- `wyrm -validate`: check that the effective config parses and validates,
  without building a session — useful in CI or a pre-commit hook.
- `wyrm -list` (`-format table|json|toml`): print running tmux sessions
  non-interactively, for scripts and status bars, as an alternative to the
  interactive `-pick` UI.

### Fixed
- Creating, killing, attaching to, or switching to a session whose name
  contains a `.` (e.g. `wyrm.vim`) could fail outright: tmux's `-t` target
  syntax uses `.` as the window.pane separator, so such names were
  misparsed by `has-session`, `new-window`, `kill-session`, and
  `attach-session` alike — even with an `=` exact-match prefix, which only
  guards against prefix ambiguity, not this. `wyrm` now looks up and targets
  every session by its stable tmux session ID instead of its name, which
  sidesteps the issue entirely.

## [0.1.4] - 2026-07-12

### Added
- Shared config storage: set `storage = "shared"` in a new global settings
  file (`~/.config/wyrm/config.toml`) to keep project configs in one
  directory (default `~/.config/wyrm/settings`) instead of `.wyrm.toml` next
  to each project, named `<folderName>.wyrm.toml`. `wyrm -migrate-config`
  moves an existing local config there.
- Custom default config: drop a `default.wyrm.toml` next to the global
  settings file (`~/.config/wyrm/default.wyrm.toml`) to replace wyrm's
  built-in fallback used when no project config is found.

## [0.1.3] - 2026-07-12

### Added
- `wyrm -pick`: an interactive fuzzy picker for running tmux sessions. Type to
  filter, arrow keys (or Ctrl-N/Ctrl-P) to move, Enter to attach (or
  `switch-client` when already inside tmux), Ctrl-X to kill the highlighted
  session, Esc/Ctrl-C to cancel. Running bare `wyrm` in a directory with no
  config also opens the picker when sessions are already running. The picker is
  built in — no fzf or other runtime dependency.

## [0.1.2] - 2026-07-11

### Fixed
- Release pipeline: the Homebrew formula publish to `jskoll/homebrew-tap`
  was failing (invalid tap token, and CI actions pinned to the deprecated
  Node 20 runtime). Both are fixed; this release is otherwise identical to
  0.1.1.

## [0.1.1] - 2026-07-11

### Added
- Homebrew tap: `brew install jskoll/tap/wyrm` (goreleaser publishes the
  formula to `jskoll/homebrew-tap` on each release).

## [0.1.0] - 2026-07-11

### Added
- `.wyrm.toml` as the default config name; the original `.tmuxconfig` still
  works as a fallback.
- `-version` flag.
- Config validation with helpful errors (unknown split types, out-of-range
  sizes).
- Unit test suite (mocked tmux runner) and integration tests against a real
  tmux server on an isolated socket.
- CI (GitHub Actions, macOS + Linux), golangci-lint, goreleaser config.

### Changed
- Run from inside an existing tmux client, `wyrm` now switches the client to
  the target session instead of nesting one tmux inside another.
- Creating a session now **reattaches** to an existing session with the same
  name instead of killing and rebuilding it.
- When no `.wyrm.toml` or `.tmuxconfig` is found, wyrm falls back to a
  built-in default config instead of erroring out.
- Panes are targeted by tmux pane ID (`%N`) instead of index, so layouts no
  longer depend on the user's `base-index` / `pane-base-index` settings.
- `pre_window` runs in every pane before its command (as documented), not
  just the first.
- Split-tree semantics defined precisely: each entry splits the pane of the
  previous entry at its level; children work within their parent's pane.
- Structural failures exit with an error; per-pane failures warn and
  continue (now a stated contract).

### Removed
- Arbitrary `time.Sleep` synchronization (tmux commands are synchronous).
- Config-path allowlist validation — configs execute commands by design, so
  restricting their location added friction without security (see README).

### Fixed
- `wyrm -kill` no longer runs `on_project_exit` when the session isn't
  running.

[Unreleased]: https://github.com/jskoll/wyrm/compare/v1.2.1...HEAD
[1.2.1]: https://github.com/jskoll/wyrm/compare/v1.2.0...v1.2.1
[1.2.0]: https://github.com/jskoll/wyrm/compare/v1.1.1...v1.2.0
[1.1.1]: https://github.com/jskoll/wyrm/compare/v1.1.0...v1.1.1
[1.1.0]: https://github.com/jskoll/wyrm/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/jskoll/wyrm/compare/v0.8.0...v1.0.0
[0.8.0]: https://github.com/jskoll/wyrm/compare/v0.7.0...v0.8.0
[0.6.2]: https://github.com/jskoll/wyrm/compare/v0.6.1...v0.6.2
[0.6.1]: https://github.com/jskoll/wyrm/compare/v0.6.0...v0.6.1
[0.6.0]: https://github.com/jskoll/wyrm/compare/v0.5.1...v0.6.0
[0.5.1]: https://github.com/jskoll/wyrm/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/jskoll/wyrm/compare/v0.4.1...v0.5.0
[0.4.1]: https://github.com/jskoll/wyrm/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/jskoll/wyrm/compare/v0.3.1...v0.4.0
[0.3.1]: https://github.com/jskoll/wyrm/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/jskoll/wyrm/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/jskoll/wyrm/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/jskoll/wyrm/compare/v0.1.12...v0.2.0
[0.1.12]: https://github.com/jskoll/wyrm/compare/v0.1.11...v0.1.12
[0.1.11]: https://github.com/jskoll/wyrm/compare/v0.1.10...v0.1.11
[0.1.10]: https://github.com/jskoll/wyrm/compare/v0.1.9...v0.1.10
[0.1.9]: https://github.com/jskoll/wyrm/compare/v0.1.8...v0.1.9
[0.1.8]: https://github.com/jskoll/wyrm/compare/v0.1.7...v0.1.8
[0.1.7]: https://github.com/jskoll/wyrm/compare/v0.1.6...v0.1.7
[0.1.6]: https://github.com/jskoll/wyrm/compare/v0.1.5...v0.1.6
[0.1.5]: https://github.com/jskoll/wyrm/compare/v0.1.4...v0.1.5
[0.1.4]: https://github.com/jskoll/wyrm/compare/v0.1.3...v0.1.4
[0.1.3]: https://github.com/jskoll/wyrm/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/jskoll/wyrm/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/jskoll/wyrm/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/jskoll/wyrm/releases/tag/v0.1.0
