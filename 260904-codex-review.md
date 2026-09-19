# Wyrm code review

Reviewed 2026-09-05 at commit a5290c04c0e897ff4c4d074b9d680f5ba523392d.

## Scope and assessment

I reviewed the Go application, internal packages, CLI completion scripts, tests,
documentation, and CI configuration. I ran the full suite with the race detector
and used isolated, temporary tmux-backed probes for reproduced findings. No
application code was changed.

Wyrm has a solid foundation: dependency-injected command execution, a central
tmux wrapper with dry-run and batch support, strict configuration decoding,
isolated tmux integration tests, and race-test coverage. The main weakness is
unstable identity and error handling at package boundaries. In a few paths,
configuration location, TUI list position, or aliases stand in for durable
project/session identity. That creates destructive actions, leaked sessions, and
a TUI crash.

P1 denotes a destructive action, crash, or serious incorrect result; P2 a
material correctness or usability issue; P3 a lower-risk maintenance issue.

## Findings

| Severity | Finding | Evidence |
| --- | --- | --- |
| P1 | kill can target a different exact-named running session when the argument is also an alias. | Reproduced |
| P1 | A session name containing a pipe leaks a newly-created session after up reports failure. | Reproduced |
| P1 | Moving or creating a shared config changes the meaning of relative roots. | Reproduced |
| P1 | The compact picker can panic when the keyboard opens the Windows menu. | Reproduced |
| P1 | Pager search panics for valid Unicode input. | Reproduced |
| P1 | Filtering and background refresh can retarget a TUI action to a different session. | Reproduced |
| P2 | Restart suppresses genuine stop failures and can claim creation succeeded. | Source review |
| P2 | Lifecycle history has the wrong identity for wildcard projects and commits too early. | Reproduced / source review |
| P2 | pre_window is typed into a direct run process; the initial split root is ignored. | Reproduced |
| P2 | validate accepts configs that then fail in up. | Reproduced |
| P2 | Pager navigation uses a height larger than the rendered body. | Reproduced |
| P2 | TUI discards lifecycle and stale target-selection errors. | Source review |
| P2 | status --watch silently ignores collection and format errors. | Reproduced |
| P2 | Shared project display identity can differ from the tmux session it creates. | Source review |
| P3 | Completion tests do not check command branches; contributor docs have drift. | Source review |

### P1: Alias precedence makes kill target the wrong session

killByName in cmd_session.go resolves a configured project or alias before
checking for an exact live tmux session. With project alpha, alias beta, and an
unrelated live session named beta, running wyrm kill beta kills alpha. The probe
confirmed that beta remained while alpha was killed.

This is a destructive ambiguity. Resolve an exact running session first, as the
attach path already does, and use an alias only when no exact session exists. A
configuration collision warning would help, but cannot replace correct runtime
precedence.

### P1: Delimited new-session output fails for valid names

internal/session/session.go requests tmux output in the form
session_id|session_name|window_id|pane_id and splits it with SplitN. Tmux accepts
a pipe in a session name. For review|pipe, the parser interprets part of the name
as the window ID. Create returns an error after tmux has created the session, and
the code lacks an ID with which to roll it back. The probe left review|pipe
running after the command failed.

Do not include user-controlled strings in a delimited multi-field response.
Return only tmux IDs from new-session, or query the name separately as the whole
response. Add an integration test that asserts failed creation leaves no session.

### P1: Config storage location becomes project-root provenance

Config.Load records a config directory and Session.Resolve uses it as the base
for a relative session.root. migrateConfig in cmd_config.go moves a local file
with os.Rename without preserving that meaning. A config containing root = "."
then resolves to the shared settings directory instead of the original project
directory. Shared init templates with root = "." and fallback default configs
show the same defect; all three cases were reproduced.

A migration or initialization can therefore operate on the wrong files. Separate
configuration storage from the base used to resolve project paths. When writing
or moving a shared config, convert relative roots to a canonical absolute project
root or persist an explicit project base. For a fallback default config, use the
invocation directory as its resolution base.

### P1: Compact picker menu indexing panics

internal/tui/menu.go indexes geometry.boxes with m.focus. Compact layout has two
boxes, while panelWindows remains enum value 2. Pressing M with Windows focused
therefore indexes past the slice. The probe reproduced index out of range [2]
with length 2.

Map focus through panelIndex before indexing geometry and return when a panel is
not present. Cover keyboard menus in normal and compact dimensions.

### P1: Unicode pager search can crash the TUI

highlightMatch in internal/tui/view.go lowercases a string, finds a byte offset
in that result, then slices the original using the byte length of the query.
Case conversion can change byte length. The Kelvin sign lowercases to ASCII k;
searching it in a line containing k produced a slice-bounds panic in the probe.

Use Unicode-aware case folding/search that preserves boundaries in the original
string, or operate on rune spans. The text-entry backspace handlers also remove
one byte rather than one rune, leaving invalid UTF-8 for characters such as é.
Fuzzy matching should use the same input-boundary abstraction.

### P1: TUI selection is positional and becomes inconsistent

Filtering is applied only to the focused panel. If a Sessions filter selects beta
and focus moves to Windows, the same index is interpreted against the unfiltered
Sessions list while the loaded Windows data is still for beta. The probe yielded
current session alpha with a beta window. Periodic refresh has the same issue:
sorting changes can move the cursor to another session without user input.

Store selected session, window, and pane IDs rather than list indexes, preserve
them through filter/refresh, and derive child lists from those IDs. Add model
tests for focus changes with a filter and for reordered updates.

### P2: Restart suppresses meaningful errors

Restart paths in cmd_session.go treat every session.Kill failure as “nothing to
stop,” including tmux server, permission, and communication failures. They then
call Create and announce creation even when an existing session blocked it.

Return a typed ErrSessionNotRunning from Kill and suppress only that condition.
Propagate all other failures and respect Create's created/already-running result.

### P2: Lifecycle state has the wrong identity and is committed too early

First-start/restart history keys use cfg.Dir(). Wildcard projects share their
template file directory, so distinct matched roots become one lifecycle project;
the probe started two such projects and found one history identity. Create also
runs hooks and marks first-start before resolving all window roots or proving
tmux creation succeeded. A malformed window may run expensive first-start work,
then turn a corrected retry into a restart.

Use an explicit canonical project identity, normally resolved root, for state.
Resolve all structure before hooks, and commit lifecycle state after successful
creation (or define an explicit transactional recovery policy).

### P2: Pane setup disagrees with direct run and split roots

applySplits sends pre_window with send-keys before deciding whether a pane has a
direct run command. For run = "cat", pre_window becomes process input rather than
shell setup; the probe captured the setup text in cat. Also, splits[0].root is
honored only when window.root is empty. When both are present, the initial pane
starts in the window root despite the documented pane-root precedence.

Define direct-process pre_window semantics; rejecting or clearly skipping it is
less surprising than injecting input. Always resolve the initial split root
relative to the window root when it is provided.

### P2: validate misses resolved runtime failures

validate checks decoded/interpolated structure but does not call Session.Resolve
or validate effective window and split roots. A root containing an unresolved
variable reported valid and then failed in up; this was reproduced. Interpolation
can also introduce invalid environment names after the initial validation.

Make validation run the same non-mutating resolution path as creation. Validate
again after interpolation and cover session, window, and pane roots.

### P2: Pager uses two viewport heights

The pager body renders at height minus five, but scrolling, paging, and match
navigation use height minus four. G and Page Down stop a line early; the probe
could not reach the final line. Centralize pager-body height and use it for
rendering, bounds, paging, and search.

### P2: TUI errors are discarded

selectTargetCmd in internal/tui/actions.go discards errors from selecting a
window/pane and is followed directly by quit/attach. A stale target can attach
without selecting the intended pane. internal/tui/projects.go passes io.Discard
to session creation and attach hooks, hiding warnings the CLI reports. Project
listing also ignores tmux list failures and presents configured projects as
stopped.

Surface errors as model messages and quit only after target selection succeeds.
Capture lifecycle warnings for the UI and propagate tmux-list failures.

### P2: Watched status drops errors

cmd_status.go ignores collection and format errors in its --watch loop. For
example, status --watch --format typo produces no error and loops until
interrupted; reproduced. Validate output format before watching and return the
initial collection/format failure. For transient failures, emit a rate-limited
diagnostic with a defined nonzero exit path.

### P2: Shared project display name can disagree with session name

ProjectName in internal/config/projects.go uses a shared config filename when
session.name is absent, while Session.Resolve derives the actual tmux name from
the root. A shared api-123.wyrm.toml rooted at /work/api is displayed/addressed
as api-123 but creates or seeks api. Discovery, display, and command targeting
therefore disagree.

Use one resolved canonical identity throughout, or require session.name for
shared configurations whose filename and root basename differ.

### P3: Completion assurance and docs are stale

main_completionflags_test.go checks whether flags occur anywhere in a completion
file once that file contains a command verb. It does not confirm that flags occur
in that command's branch. Its AST extraction also misses custom fs.Var flags.
Current scripts show the gap: Bash up lacks -n, -d, and -var; Fish lacks -n and
-var; Zsh lacks -var.

Parse command branches or generate completions from metadata shared with flag
registration. CONTRIBUTING.md also says Go 1.24 while go.mod requires Go 1.25,
and refers to RunOutputsTolerant, which is not the current tmux API.

## Architecture recommendations

The package ownership is generally clear: config, session construction, tmux
transport, lifecycle state, agent scanning, and TUI rendering are sensibly
separated. The findings cluster around three cross-cutting improvements:

1. Carry canonical project and tmux IDs rather than deriving identity from list
   positions, aliases, config locations, or filenames.
2. Stage creation: interpolate and resolve all config, validate it, create tmux
   state, then commit lifecycle state. Give every side-effect stage a rollback
   or explicit no-side-effect guarantee.
3. Establish error categories at package boundaries, especially absent session
   versus tmux failure, configuration versus runtime failure, and hook warning
   versus successful UI action.

## Verification

- go test -race ./... passed for every package.
- go vet ./... passed.
- gofmt -l . produced no output.
- Temporary tmux-backed probes reproduced every finding labelled reproduced.
  Those files and sessions were outside this repository and were not retained.

Passing tests are meaningful, but these cases expose missing coverage at the
CLI, session, configuration, and TUI integration boundaries. The P1 items should
be addressed before expanding templates or lifecycle behavior.

