package main

// Verbs that act on config files rather than on running sessions: edit,
// validate, save, migrate-config, list-configs, init.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jskoll/wyrm/internal/config"
	"github.com/jskoll/wyrm/internal/editor"
	"github.com/jskoll/wyrm/internal/freeze"
	"github.com/jskoll/wyrm/internal/session"
	"github.com/jskoll/wyrm/internal/state"
	"github.com/jskoll/wyrm/internal/tmux"
	"github.com/pelletier/go-toml/v2"
)

// listConfigs prints config paths wyrm knows about: the local file (if
// present), every candidate in the shared config directory, and every
// [[wildcard]] template that currently matches at least one directory.
// These are the candidates shell completion offers for -config; -config
// itself can point at any of them regardless of the current storage setting.
func (a *app) listConfigs(args []string) error {
	fs := a.newFlagSet("list-configs")
	names := fs.Bool("names", false, "list project names and aliases instead of config paths")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if err := requireNoArgs(fs); err != nil {
		return err
	}
	settings, err := config.LoadSettings()
	if err != nil {
		return err
	}
	if *names {
		seen := map[string]bool{}
		for _, p := range config.DiscoverProjects(settings) {
			for _, name := range append([]string{p.Name}, p.Aliases...) {
				if !seen[name] {
					seen[name] = true
					_, _ = fmt.Fprintln(a.stdout, name)
				}
			}
		}
		return nil
	}
	for _, name := range []string{config.DefaultFileName, config.LegacyFileName} {
		if _, err := os.Stat(name); err == nil {
			_, _ = fmt.Fprintln(a.stdout, name)
		}
	}
	if dir, err := settings.ResolvedSharedDir(); err == nil {
		matches, _ := filepath.Glob(filepath.Join(dir, "*"+config.DefaultFileName))
		for _, m := range matches {
			_, _ = fmt.Fprintln(a.stdout, m)
		}
	}
	seen := map[string]bool{}
	for _, p := range config.DiscoverWildcardProjects(settings) {
		if !seen[p.Path] {
			seen[p.Path] = true
			_, _ = fmt.Fprintln(a.stdout, p.Path)
		}
	}
	return nil
}

// migrateConfig moves the current directory's local config file into the
// shared config directory, named "<folderName>.wyrm.toml". It does not
// touch the storage setting itself; run this after (or before) switching
// settings.Storage to "shared".
func (a *app) migrateConfig(args []string) error {
	fs := a.newFlagSet("migrate-config")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if err := requireNoArgs(fs); err != nil {
		return err
	}
	settings, err := config.LoadSettings()
	if err != nil {
		return err
	}

	src, err := config.Discover()
	if err != nil {
		return fmt.Errorf("no local config to migrate: %w", err)
	}

	sourceConfig, err := config.Load(src)
	if err != nil {
		return err
	}
	cwd := sourceConfig.Dir()
	dst, err := settings.SharedConfigPath(cwd)
	if err != nil {
		return err
	}

	if _, err := os.Stat(dst); err == nil {
		// Name the project that owns it. "already exists, remove it first"
		// gave no hint that a different directory had claimed the file, which
		// is the common case in a monorepo full of same-named leaf folders.
		if owner, known := config.SharedConfigOwner(dst); known && !config.SamePath(owner, cwd) {
			return fmt.Errorf("%s already exists and belongs to %s — set a distinct [session].name in %s, or migrate from there",
				dst, owner, dst)
		}
		return fmt.Errorf("%s already exists, remove it first", dst)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	// A relative session.root (bare "." above all, but also e.g. "backend")
	// currently resolves against src's own directory — cwd. Moving the file
	// changes what it resolves against to the shared directory instead,
	// silently rerooting the session it builds. Canonicalize it to the same
	// absolute directory it already meant — cwd joined with the original
	// relative root, not cwd alone, which would collapse "backend" into the
	// project root itself — before the move.
	_, resolvedRoot, err := sourceConfig.Session.Resolve(cwd)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	var rewrittenRoot string
	if config.RootNeedsAbsolute(sourceConfig.Session.Root) {
		rewrittenRoot = resolvedRoot
		data, err = config.RewriteSessionString(data, "root", resolvedRoot)
		if err != nil {
			return err
		}
	}
	data, err = config.RewriteSessionString(data, "project_dir", cwd)
	if err != nil {
		return err
	}
	// Check the relocated meaning before writing or deleting anything.
	migrated, _, err := config.Decode(data)
	if err != nil {
		return fmt.Errorf("validating migrated config: %w", err)
	}
	if err := migrated.Validate(); err != nil {
		return err
	}
	_, migratedRoot, err := migrated.Session.Resolve(filepath.Dir(dst))
	if err != nil || !config.SamePath(migratedRoot, resolvedRoot) || migrated.Session.ProjectDir != cwd {
		return fmt.Errorf("migration would change the project identity or root")
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := state.AtomicWriteFile(dst, data, info.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Remove(src); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(a.stdout, "moved %s to %s\n", src, dst)
	if rewrittenRoot != "" {
		_, _ = fmt.Fprintf(a.stdout, "note: session.root was relative, rewritten to %s so the session still builds here\n", rewrittenRoot)
	}
	// Storage collisions do not change the config's resolved session name.
	if base := filepath.Base(dst); base != filepath.Base(cwd)+config.DefaultFileName {
		_, _ = fmt.Fprintf(a.stdout,
			"note: %s already belonged to another project, so this one is stored as %s\n"+
				"      set a distinct [session].name if the projects would create the same session name\n",
			filepath.Base(cwd)+config.DefaultFileName, base)
	}
	if settings.Storage != config.StorageShared {
		if settingsPath, err := config.SettingsPath(); err == nil {
			_, _ = fmt.Fprintf(a.stdout, "note: set storage = \"shared\" in %s for wyrm to use it\n", settingsPath)
		}
	}
	return nil
}

// validate checks that the effective config (the one wyrm would actually
// use) parses and validates, without building a session.
func (a *app) validate(args []string) error {
	fs := a.newFlagSet("validate")
	configPath := fs.String("config", "", "path to config file (default: .wyrm.toml, then .tmuxconfig)")
	strict := fs.Bool("strict", false, "exit non-zero if the config has warnings (typos, deprecations)")
	var vars varMapFlag
	fs.Var(&vars, "var", "set template variable (KEY=VALUE, can be repeated)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if err := requireNoArgs(fs); err != nil {
		return err
	}
	settings, err := config.LoadSettings()
	if err != nil {
		return err
	}
	cfg, source, err := config.ResolveEffective(settings, *configPath)
	if err != nil {
		return err
	}
	if len(vars) > 0 {
		cfg.Interpolate(vars)
	}
	a.printWarnings(cfg)
	// Decoding only checks structure, not what session.root, a window's
	// root, or a split's root actually resolve to once interpolation has
	// run — an unset $VAR there passed validate and then failed in `wyrm
	// up`. Run the same non-mutating resolution Create performs.
	if err := session.ValidateRoots(cfg); err != nil {
		return fmt.Errorf("%s: %w", source, err)
	}
	// Warnings are not failures by default — a deprecated `panes` list still
	// builds the session its author wanted. -strict is for CI, where "this
	// config has a typo in it" should stop the build.
	if *strict && len(cfg.Warnings()) > 0 {
		return fmt.Errorf("%s has %d warning(s) and -strict was given", source, len(cfg.Warnings()))
	}
	_, _ = fmt.Fprintf(a.stdout, "config valid: %s\n", source)
	return nil
}

// edit opens the resolved config in $EDITOR (falling back to vi), creating
// one at the location wyrm would look next time if none exists yet. After
// the editor exits, a saved-but-invalid file gets a warning rather than an
// error, matching the project's warn-don't-abort philosophy for anything
// that isn't a structural failure.
func (a *app) edit(args []string) error {
	fs := a.newFlagSet("edit")
	explicitPath := fs.String("config", "", "path to config file (default: the resolved config)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	settings, err := config.LoadSettings()
	if err != nil {
		return err
	}

	path := *explicitPath
	if path == "" {
		resolved, _, err := config.EditTarget(settings)
		if err != nil {
			return err
		}
		path = resolved
	}
	// Create the parent directory whichever way the path was arrived at:
	// `wyrm edit -config new/dir/x.toml` used to hand the editor a path it
	// couldn't write, while the flagless form worked.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	cmd, err := editor.Command(path)
	if err != nil {
		return err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	runErr := cmd.Run()

	if _, statErr := os.Stat(path); statErr == nil {
		if _, loadErr := config.Load(path); loadErr != nil {
			_, _ = fmt.Fprintf(a.stderr, "wyrm: warning: %s: %v\n", path, loadErr)
		}
	}

	if runErr != nil {
		var exitError *exec.ExitError
		if errors.As(runErr, &exitError) {
			// ExitCode is -1 when the editor was killed by a signal, and
			// os.Exit(-1) reaches the shell as 255. Report the conventional
			// 128+signal instead. Either way the editor has already said
			// whatever it wanted to, so nothing more is printed.
			if code := exitError.ExitCode(); code >= 0 {
				return silent(code)
			}
			return silent(1)
		}
		return runErr
	}
	return nil
}

// save snapshots a running session's windows, split layout, and foreground
// pane commands into a new config for the current folder (see internal/freeze).
// The target session is the one wyrm is currently attached to when run from
// inside tmux, or the folder's own session (looked up the same way a bare
// `wyrm` would resolve its name) otherwise. Like migrate-config, it refuses to
// overwrite an existing config rather than silently discarding hand-written
// hooks or comments.
func (a *app) save(args []string) error {
	fs := a.newFlagSet("save")
	var configPath, outputFlag string
	var stdoutFlag bool
	var dryRun, dryRunLong bool

	fs.StringVar(&configPath, "config", "", "path to write the saved config (default: the discovered/shared location)")
	fs.StringVar(&outputFlag, "o", "", "path to write the saved config, or '-' for stdout")
	fs.BoolVar(&stdoutFlag, "stdout", false, "print the generated config to stdout instead of saving to disk")
	fs.BoolVar(&dryRun, "n", false, "dry run: preview the save destination and generated config without writing to disk")
	fs.BoolVar(&dryRunLong, "dry-run", false, "alias for -n")

	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if err := requireNoArgs(fs); err != nil {
		return err
	}

	if outputFlag == "-" {
		stdoutFlag = true
	} else if outputFlag != "" {
		if configPath != "" && configPath != outputFlag {
			return usageErrf("cannot specify both -config and -o with different paths")
		}
		configPath = outputFlag
	}

	if stdoutFlag && configPath != "" {
		return usageErrf("cannot specify both -stdout and -config")
	}

	settings, err := config.LoadSettings()
	if err != nil {
		return err
	}

	sessionID, sessionName, err := a.saveTarget(settings)
	if err != nil {
		return err
	}

	if stdoutFlag {
		cfg, err := freeze.Config(a.runner, sessionID, sessionName, ".")
		if err != nil {
			return err
		}
		data, err := toml.Marshal(cfg)
		if err != nil {
			return err
		}
		_, err = a.stdout.Write(data)
		return err
	}

	dest, err := a.saveDestination(settings, configPath)
	if err != nil {
		return err
	}

	cfg, err := freeze.Config(a.runner, sessionID, sessionName, a.saveRoot(sessionID, dest))
	if err != nil {
		return err
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		return err
	}

	if dryRun || dryRunLong {
		_, _ = fmt.Fprintf(a.stdout, "# Dry run: would save session %s to %s\n%s", sessionName, dest, string(data))
		return nil
	}

	if err := state.AtomicWriteFile(dest, data, 0o644); err != nil {
		return err
	}

	if _, loadErr := config.Load(dest); loadErr != nil {
		_, _ = fmt.Fprintf(a.stderr, "wyrm: warning: %s: %v\n", dest, loadErr)
	}

	_, _ = fmt.Fprintf(a.stdout, "saved session %s to %s\n", sessionName, dest)
	return nil
}

// saveTarget picks the session to snapshot: the one wyrm is attached to when
// run inside tmux, else the current folder's own.
func (a *app) saveTarget(settings *config.Settings) (sessionID, sessionName string, err error) {
	if a.insideTmux() {
		return tmux.CurrentSession(a.runner)
	}
	cfg, _, err := config.ResolveEffective(settings, "")
	if err != nil {
		return "", "", err
	}
	name, _, err := cfg.Session.Resolve(cfg.Dir())
	if err != nil {
		return "", "", err
	}
	id, ok, err := tmux.FindSessionID(a.runner, name)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", "", fmt.Errorf(
			"no running session named %q for this folder (run it from inside the session you want to save, or start it with wyrm first)", name)
	}
	return id, name, nil
}

// saveDestination resolves where the snapshot is written, refusing to clobber
// an existing config.
func (a *app) saveDestination(settings *config.Settings, explicit string) (string, error) {
	dest, exists := explicit, false
	if dest == "" {
		var err error
		dest, exists, err = config.EditTarget(settings)
		if err != nil {
			return "", err
		}
	} else if _, statErr := os.Stat(dest); statErr == nil {
		exists = true
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	if exists {
		return "", fmt.Errorf("%s already exists, remove it first", dest)
	}
	return dest, nil
}

// saveRoot decides what to write as the saved config's session.root.
//
// "." is preferred, because it keeps the config portable — a repo can commit
// its .wyrm.toml and every clone works. But "." is only correct when the
// session's own directory is the one the config is being written next to;
// saving session "api" into ~/web, or into the shared config directory, would
// otherwise produce a config that rebuilds the layout in the wrong place. In
// those cases write the session's real path and say so.
func (a *app) saveRoot(sessionID, dest string) string {
	path, err := tmux.SessionPath(a.runner, sessionID)
	if err != nil || path == "" {
		return "."
	}
	destDir, err := filepath.Abs(filepath.Dir(dest))
	if err != nil {
		return path
	}
	if absPath, err := filepath.Abs(path); err == nil && absPath == destDir {
		return "."
	}
	_, _ = fmt.Fprintf(a.stderr,
		"wyrm: warning: the session's directory (%s) is not where this config is being written (%s); saving an absolute session.root\n",
		path, destDir)
	return path
}

// init scaffolds a project configuration either interactively through a
// guided CLI wizard or non-interactively using a starter template (-template).
func (a *app) init(args []string) error {
	fs := a.newFlagSet("init")
	var templateFlag, templateFlagShort string
	var forceFlag, forceFlagShort bool
	var configPath string

	fs.StringVar(&templateFlag, "template", "", "starter template (node, python, go, rust, monorepo, minimal)")
	fs.StringVar(&templateFlagShort, "t", "", "alias for -template")
	fs.BoolVar(&forceFlag, "force", false, "overwrite existing config without prompting")
	fs.BoolVar(&forceFlagShort, "f", false, "alias for -force")
	fs.StringVar(&configPath, "config", "", "path to write config file (default: .wyrm.toml)")

	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if err := requireNoArgs(fs); err != nil {
		return err
	}

	templateName := templateFlag
	if templateName == "" {
		templateName = templateFlagShort
	}
	force := forceFlag || forceFlagShort

	settings, err := config.LoadSettings()
	if err != nil {
		return err
	}

	dest, exists, err := a.initDestination(settings, configPath)
	if err != nil {
		return err
	}

	reader := bufio.NewReader(a.in())

	if exists && !force {
		_, _ = fmt.Fprintf(a.stdout, "%s already exists. Overwrite? [y/N]: ", dest)
		line, readErr := reader.ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		ans := strings.TrimSpace(strings.ToLower(line))
		if ans != "y" && ans != "yes" {
			return fmt.Errorf("%s already exists, use --force to overwrite", dest)
		}
	}

	cwd, _ := os.Getwd()
	defaultName := filepath.Base(cwd)
	if defaultName == "" || defaultName == "." || defaultName == "/" {
		defaultName = "myproject"
	}

	// A generated config normally names its own directory with root = ".":
	// the file and the project it describes live in the same place. That
	// breaks the moment the file is written into the shared config
	// directory instead — "." would then resolve against the shared
	// directory, not this project. Anchor it to the current directory
	// explicitly whenever that's where this config is headed.
	defaultRoot := "."
	if sharedDir, serr := settings.ResolvedSharedDir(); serr == nil {
		if destDir, derr := filepath.Abs(filepath.Dir(dest)); derr == nil &&
			filepath.Clean(destDir) == filepath.Clean(sharedDir) {
			defaultRoot = cwd
		}
	}

	var content string
	if templateName != "" {
		content, err = config.GetTemplate(templateName, defaultName, defaultRoot)
		if err != nil {
			return usageErrf("%v", err)
		}
	} else {
		content, err = a.runInitWizard(reader, defaultName, defaultRoot)
		if err != nil {
			return err
		}
	}

	data := []byte(content)
	cfg, unknown, err := config.Decode(data)
	if err != nil {
		return fmt.Errorf("generating config: %w", err)
	}
	if len(unknown) > 0 {
		return fmt.Errorf("generating config: unknown keys %v", unknown)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validating config: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}

	if err := state.AtomicWriteFile(dest, data, 0o644); err != nil {
		return err
	}

	a.printWarnings(cfg)
	_, _ = fmt.Fprintf(a.stdout, "wrote %s\n", dest)
	return nil
}

func (a *app) initDestination(settings *config.Settings, explicit string) (string, bool, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err == nil {
			return explicit, true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", false, err
		}
		return explicit, false, nil
	}

	dest, exists, err := config.EditTarget(settings)
	if err != nil {
		return "", false, err
	}
	return dest, exists, nil
}

func (a *app) runInitWizard(reader *bufio.Reader, defaultSessionName, defaultRoot string) (string, error) {
	sessionName, err := promptLine(reader, a.stdout, "Session name", defaultSessionName)
	if err != nil {
		return "", err
	}
	if sessionName == "" {
		sessionName = defaultSessionName
	}

	sessionRoot, err := promptLine(reader, a.stdout, "Root directory", defaultRoot)
	if err != nil {
		return "", err
	}
	if sessionRoot == "" {
		sessionRoot = defaultRoot
	}

	_, _ = fmt.Fprintln(a.stdout, "\nChoose configuration method:")
	_, _ = fmt.Fprintln(a.stdout, "  1) Custom layout (interactive wizard)")
	_, _ = fmt.Fprintln(a.stdout, "  2) Node.js (dev server + test watch)")
	_, _ = fmt.Fprintln(a.stdout, "  3) Python (editor + pytest + repl)")
	_, _ = fmt.Fprintln(a.stdout, "  4) Go (editor + test watch + server)")
	_, _ = fmt.Fprintln(a.stdout, "  5) Rust (editor + test + check + run)")
	_, _ = fmt.Fprintln(a.stdout, "  6) Monorepo (services + packages + git)")
	_, _ = fmt.Fprintln(a.stdout, "  7) Minimal (editor + shell)")

	choice, err := promptLine(reader, a.stdout, "Select [1-7]", "1")
	if err != nil {
		return "", err
	}

	switch strings.TrimSpace(choice) {
	case "2", "node", "nodejs":
		return config.GetTemplate("node", sessionName, sessionRoot)
	case "3", "python", "py":
		return config.GetTemplate("python", sessionName, sessionRoot)
	case "4", "go", "golang":
		return config.GetTemplate("go", sessionName, sessionRoot)
	case "5", "rust", "rs":
		return config.GetTemplate("rust", sessionName, sessionRoot)
	case "6", "monorepo":
		return config.GetTemplate("monorepo", sessionName, sessionRoot)
	case "7", "minimal":
		return config.GetTemplate("minimal", sessionName, sessionRoot)
	default:
		// Custom layout wizard
	}

	var windows []config.WindowSpec
	winIdx := 1
	for {
		defaultWinName := "main"
		if winIdx > 1 {
			defaultWinName = fmt.Sprintf("window%d", winIdx)
		}

		_, _ = fmt.Fprintf(a.stdout, "\n--- Window %d ---\n", winIdx)
		winName, err := promptLine(reader, a.stdout, "Window name", defaultWinName)
		if err != nil {
			return "", err
		}
		if winName == "" {
			winName = defaultWinName
		}

		_, _ = fmt.Fprintln(a.stdout, "Layout presets:")
		_, _ = fmt.Fprintln(a.stdout, "  1) Single pane")
		_, _ = fmt.Fprintln(a.stdout, "  2) 2-pane vertical split (left / right)")
		_, _ = fmt.Fprintln(a.stdout, "  3) 2-pane horizontal split (top / bottom)")
		_, _ = fmt.Fprintln(a.stdout, "  4) 3-pane (editor left, 2 stacked right)")
		_, _ = fmt.Fprintln(a.stdout, "  5) 3-pane main horizontal (top / 2 bottom)")

		layoutChoice, err := promptLine(reader, a.stdout, "Select layout [1-5]", "2")
		if err != nil {
			return "", err
		}

		preset := config.PresetTwoPaneVertical
		switch strings.TrimSpace(layoutChoice) {
		case "1":
			preset = config.PresetSingle
		case "2":
			preset = config.PresetTwoPaneVertical
		case "3":
			preset = config.PresetTwoPaneHorizontal
		case "4":
			preset = config.PresetThreePaneEditorStack
		case "5":
			preset = config.PresetThreePaneMainHorizontal
		}

		var commands []string
		switch preset {
		case config.PresetSingle:
			cmd1, err := promptLine(reader, a.stdout, "Command (leave empty for shell)", "$EDITOR .")
			if err != nil {
				return "", err
			}
			commands = append(commands, cmd1)
		case config.PresetTwoPaneVertical:
			cmd1, err := promptLine(reader, a.stdout, "Left pane command", "$EDITOR .")
			if err != nil {
				return "", err
			}
			cmd2, err := promptLine(reader, a.stdout, "Right pane command (leave empty for shell)", "")
			if err != nil {
				return "", err
			}
			commands = append(commands, cmd1, cmd2)
		case config.PresetTwoPaneHorizontal:
			cmd1, err := promptLine(reader, a.stdout, "Top pane command", "$EDITOR .")
			if err != nil {
				return "", err
			}
			cmd2, err := promptLine(reader, a.stdout, "Bottom pane command (leave empty for shell)", "")
			if err != nil {
				return "", err
			}
			commands = append(commands, cmd1, cmd2)
		case config.PresetThreePaneEditorStack:
			cmd1, err := promptLine(reader, a.stdout, "Main/editor pane command", "$EDITOR .")
			if err != nil {
				return "", err
			}
			cmd2, err := promptLine(reader, a.stdout, "Top-right pane command (leave empty for shell)", "")
			if err != nil {
				return "", err
			}
			cmd3, err := promptLine(reader, a.stdout, "Bottom-right pane command (leave empty for shell)", "")
			if err != nil {
				return "", err
			}
			commands = append(commands, cmd1, cmd2, cmd3)
		case config.PresetThreePaneMainHorizontal:
			cmd1, err := promptLine(reader, a.stdout, "Top/main pane command", "$EDITOR .")
			if err != nil {
				return "", err
			}
			cmd2, err := promptLine(reader, a.stdout, "Bottom-left pane command (leave empty for shell)", "")
			if err != nil {
				return "", err
			}
			cmd3, err := promptLine(reader, a.stdout, "Bottom-right pane command (leave empty for shell)", "")
			if err != nil {
				return "", err
			}
			commands = append(commands, cmd1, cmd2, cmd3)
		}

		windows = append(windows, config.WindowSpec{
			Name:     winName,
			Preset:   preset,
			Commands: commands,
		})

		more, err := promptLine(reader, a.stdout, "Add another window? [y/N]", "N")
		if err != nil {
			return "", err
		}
		ans := strings.TrimSpace(strings.ToLower(more))
		if ans != "y" && ans != "yes" {
			break
		}
		winIdx++
	}

	return config.GenerateCustomConfig(sessionName, sessionRoot, windows), nil
}

func promptLine(reader *bufio.Reader, w io.Writer, prompt string, defaultVal string) (string, error) {
	if defaultVal != "" {
		_, _ = fmt.Fprintf(w, "%s [%s]: ", prompt, defaultVal)
	} else {
		_, _ = fmt.Fprintf(w, "%s: ", prompt)
	}
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultVal, nil
	}
	return line, nil
}
