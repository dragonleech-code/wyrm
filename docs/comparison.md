# How wyrm compares

The table on the [home page](index.md#why-another-one) covers the basics — language, config format, runtime
deps, split trees, hooks. This one goes wider, against
[tmuxinator](https://github.com/tmuxinator/tmuxinator) (Ruby/YAML),
[tmuxp](https://github.com/tmux-python/tmuxp) (Python/YAML/JSON),
[smug](https://github.com/ivaaaan/smug) (Go/YAML), and
[sesh](https://github.com/joshmedeski/sesh) (Go, zoxide-based).

| Feature | wyrm | tmuxinator | tmuxp | smug | sesh |
|---|---|---|---|---|---|
| Nested split-tree layout w/ explicit sizes | ✅ | ❌ (preset layouts) | ❌ (preset layouts) | ❌ (preset layouts) | ❌ (single window/pane per config) |
| Pane-ID targeting (robust to base-index) | ✅ | ❌ | ❌ | ❌ | n/a |
| Save/freeze a running session to config | ✅ `wyrm save` | ❌ | ✅ `tmuxp freeze` | ❌ | ❌ |
| Built-in full-screen TUI (browse/kill/rename/preview) | ✅ `wyrm tui` | ❌ | ❌ | ❌ | ✅ (picker; flatter, no window/pane management) |
| Fuzzy session picker, no external tool needed | ✅ `wyrm pick` | ❌ | ❌ | ❌ | ✅ (built-in picker) |
| AI-coding-agent waiting/blocked detection | ✅ | ❌ | ❌ | ❌ | ❌ |
| Config validation w/ typo detection | ✅ `validate -strict`, JSON Schema | ❌ | ❌ | ❌ | partial (JSON Schema autocomplete) |
| Self-update | ✅ `wyrm selfupdate` | ❌ | ❌ | ❌ | ❌ |
| Custom tmux socket / binary override | ✅ `[tmux]` | ✅ `tmux_options`/`tmux_command` | ✅ | ❌ | ✅ `tmux_command` |
| Detached start (build without attaching) | ✅ `-d` | ✅ `attach: false` | ✅ `-d` | ✅ | n/a |
| First-start vs. restart hook distinction | ✅ `on_project_first_start`/`restart` | ✅ | ❌ | ❌ | ❌ |
| Pane titles / live pane-border status | ✅ `enable_pane_titles` | ✅ `enable_pane_titles` | ❌ | ❌ | n/a |
| Wildcard config (one template → many directories) | ✅ `[[wildcard]]` | ❌ | ❌ | ❌ | ✅ `[[wildcard]]` |
| Session aliases | ✅ `session.aliases` | ❌ | ❌ | ❌ | ✅ |
| Git-aware session naming | ✅ (worktree-aware) | ❌ | ❌ | ❌ | ✅ (git remote-based) |
| Trust prompt before running a repo's config | ✅ `wyrm allow` | ❌ | ❌ | ❌ | n/a |
| Remote host sessions over ssh | ✅ `up -host` | ❌ | ❌ | ❌ | ❌ |
| Clone-and-connect (`clone <repo>` → session) | ✅ `wyrm clone` | ❌ | ❌ | ❌ | ❌ (`sesh mkdir` is close but doesn't clone) |
| zoxide/frecency directory discovery | ✅ opt-in, `wyrm tui` only | ❌ | ❌ | ❌ | ✅ (core to sesh's design) |
| Multi-format config (YAML/JSON, cross-tool import) | ❌ (TOML only, by design) | n/a | ✅ (also reads tmuxinator/teamocil files) | n/a | n/a |
| Plugin/extension system | ❌ (hooks cover it — see below) | ❌ | ✅ (Python plugin system) | ❌ | ❌ |
| Scripting/API shell | ❌ | ❌ | ✅ `tmuxp shell` (libtmux) | ❌ | ❌ |

A few calls worth explaining:

- **wyrm's split trees stay the one thing none of these match.** Every other
  tool here either picks from tmux's preset layouts (`tiled`,
  `main-vertical`, ...) or, in sesh's case, doesn't lay out multiple panes at
  all. wyrm's `[[windows.splits]]` tree with per-pane `size` and arbitrary
  nesting is the only one of these that can round-trip a saved session back
  into an identical nested layout.
- **sesh is the closest match on the discovery side** (wildcards, aliases,
  zoxide, git-aware naming) — wyrm closed that gap deliberately, matching
  sesh's model rather than reinventing one, while keeping its own layout
  format as the template a wildcard or clone builds from.
- **Multi-format config is the one gap left open on purpose.** wyrm stays
  TOML-only; tmuxp's ability to read YAML/JSON/tmuxinator/teamocil files is
  real interoperability value if you're migrating, but it cuts against
  wyrm's "one format, strictly validated" design.
- **No plugin system, and not planning one.** wyrm's hooks
  (`on_project_start`, `on_project_first_start`, `on_project_restart`,
  `on_project_exit`, `pre_window`, `post_window`) already cover what a
  plugin would typically be for — see [hooks as an extension point](configuration.md#hooks-as-wyrms-extension-point).

