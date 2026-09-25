# Examples

Full configs live in [`examples/`](https://github.com/jskoll/wyrm/tree/main/examples)
in the repo — copy one in as `.wyrm.toml` and adjust.

## [minimal.wyrm.toml](https://github.com/jskoll/wyrm/blob/main/examples/minimal.wyrm.toml)

The smallest useful config: one window, editor left, shell right.

```toml
[session]
root = "."          # session name defaults to this directory's name

[[windows]]
name = "main"

  [[windows.splits]]
  command = "nvim"

  [[windows.splits]]
  type = "h"        # split horizontally: new pane to the right
  size = 30         # new pane gets 30% of the width
```

## [basic.wyrm.toml](https://github.com/jskoll/wyrm/blob/main/examples/basic.wyrm.toml)

Legacy flat `panes` format across three windows (editor, tests, server),
with lifecycle hooks and a `startup_window`.

!!! warning "Deprecated format"
    This is the only example still using the flat `[[windows.panes]]` list,
    kept as a reference for existing configs. It and the `.tmuxconfig`
    filename are deprecated, and wyrm warns when it loads a
    config using either. New configs should use the `splits` tree — every
    other example below does.

## [nested-splits.wyrm.toml](https://github.com/jskoll/wyrm/blob/main/examples/nested-splits.wyrm.toml)

Demonstrates the split tree with nested `children`, `pre_window` interpreter
setup, and `startup_pane` targeting.

## [monorepo.wyrm.toml](https://github.com/jskoll/wyrm/blob/main/examples/monorepo.wyrm.toml)

Per-window and per-split `root` so each window opens in its own package,
`[session.env]` reaching every pane, and `run` for the dev servers.

## [nodejs.wyrm.toml](https://github.com/jskoll/wyrm/blob/main/examples/nodejs.wyrm.toml)

A Node.js project layout: editor, dev server, and a test watcher window,
using the `splits` tree.

## [php-symfony.wyrm.toml](https://github.com/jskoll/wyrm/blob/main/examples/php-symfony.wyrm.toml)

A PHP/Symfony layout: editor, `symfony serve`, and a database window,
using the `splits` tree.

## [python.wyrm.toml](https://github.com/jskoll/wyrm/blob/main/examples/python.wyrm.toml)

A Python project layout: editor, test runner, and a REPL window,
using the `splits` tree.

## [hooks.wyrm.toml](https://github.com/jskoll/wyrm/blob/main/examples/hooks.wyrm.toml)

Every lifecycle and identity option a project's config can set:
`on_project_first_start`/`on_project_restart` alongside `on_project_start`,
`post_window`, `aliases`, and `enable_pane_titles`.

## [wildcard-template.wyrm.toml](https://github.com/jskoll/wyrm/blob/main/examples/wildcard-template.wyrm.toml)

The template a [`[[wildcard]]`](configuration.md#wildcard-one-config-many-directories)
entry points at — applied to every directory a glob pattern matches, instead
of a `.wyrm.toml` per directory. Goes in the shared config directory, not a
project root.
