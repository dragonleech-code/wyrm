package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jskoll/wyrm/internal/tmux"
)

// Project is a discoverable wyrm config: the session name it would produce and
// where the file lives. It is the unit both the TUI's Projects panel and
// `wyrm <name>` work in, so the discovery rules live here rather than being
// implemented once per caller.
type Project struct {
	Name   string
	Path   string
	Shared bool
	// Aliases are additional exact-match names FindProject resolves to this
	// project, from the config's own session.aliases. Never set for a
	// Wildcard project — a template shared by many directories has no single
	// alias to give any one of them.
	Aliases []string
	// Root is the matched directory for a Wildcard project (absolute) and
	// empty otherwise. When set, it overrides the template config's own
	// session.root — see DiscoverWildcardProjects.
	Root string
	// Wildcard is true for a project synthesized from a [[wildcard]] pattern
	// match rather than discovered as its own file. Many Wildcard projects
	// can share the same Path (the template) while differing in Root.
	Wildcard bool
}

// nameCache memoizes ProjectName/aliases by file identity, so repeated
// discovery doesn't re-read and re-parse every config on disk.
//
// The TUI calls DiscoverProjects on a 3-second timer, and each call used to
// Load() every shared config to work out its session name — twenty projects
// meant twenty file reads and twenty TOML parses every three seconds, forever.
// Keying on (size, mtime) means an edit is still picked up on the next tick,
// which is what the timer is for.
var nameCache sync.Map // path -> nameCacheEntry

type nameCacheEntry struct {
	size    int64
	mtime   time.Time
	name    string
	aliases []string
}

// cachedProjectInfo returns ProjectName(path, shared) and the config's
// session.aliases, reusing the last result while the file is unchanged. info
// is the already-stat'ed file, since callers have had to stat it to know it
// exists.
func cachedProjectInfo(path string, shared bool, info os.FileInfo) (name string, aliases []string) {
	abs, err := filepath.Abs(path)
	if err == nil {
		path = abs
	}
	cacheKey := struct {
		path   string
		shared bool
	}{path, shared}
	if v, ok := nameCache.Load(cacheKey); ok {
		e := v.(nameCacheEntry)
		if e.size == info.Size() && e.mtime.Equal(info.ModTime()) {
			return e.name, e.aliases
		}
	}
	// One Load, not two. This used to call ProjectName (which loads) and then
	// Load again for the aliases, parsing every config on disk twice per cache
	// miss — and a cold cache is every first tick of the TUI.
	cfg, err := Load(path)
	if err != nil {
		name = ProjectName(path, shared)
	} else {
		name = projectNameFrom(cfg, path, shared)
		aliases = cfg.Session.Aliases
	}
	nameCache.Store(cacheKey, nameCacheEntry{size: info.Size(), mtime: info.ModTime(), name: name, aliases: aliases})
	return name, aliases
}

// DiscoverProjects enumerates every config wyrm can see: the local
// .wyrm.toml/.tmuxconfig in the current directory, every "<folder>.wyrm.toml"
// in the shared config directory, and every directory matched by a
// [[wildcard]] pattern. Shared entries are sorted by path for a stable order;
// the local config, when present, comes first. settings may be nil, in which
// case only local configs are returned.
func DiscoverProjects(settings *Settings) []Project {
	var projects []Project
	seen := map[string]bool{}
	add := func(path string, shared bool, info os.FileInfo) {
		abs, err := filepath.Abs(path)
		if err != nil {
			abs = path
		}
		if seen[abs] {
			return
		}
		seen[abs] = true
		name, aliases := cachedProjectInfo(path, shared, info)
		if name == "" {
			return
		}
		projects = append(projects, Project{Name: name, Path: path, Shared: shared, Aliases: aliases})
	}

	for _, name := range []string{DefaultFileName, LegacyFileName} {
		if info, err := os.Stat(name); err == nil {
			add(name, false, info)
		}
	}
	if settings != nil {
		if dir, err := settings.ResolvedSharedDir(); err == nil {
			matches, _ := filepath.Glob(filepath.Join(dir, "*"+DefaultFileName))
			sort.Strings(matches)
			for _, m := range matches {
				info, err := os.Stat(m)
				if err != nil {
					continue
				}
				add(m, true, info)
			}
		}
	}
	// A wildcard match whose name a real config already claims is dropped: a
	// pattern like "~/Code/*" covers the directory you are standing in as
	// well as its siblings, so the project with its own .wyrm.toml would
	// otherwise be listed twice — once from its file and once from the
	// template. The specific config wins, which is the same precedence
	// appendZoxideProjects applies for the same reason.
	//
	// Names, not paths, because that is the only thing the two have in
	// common: the file-based entry's Path is its own config and the
	// wildcard's is the shared template.
	byName := make(map[string]bool, len(projects))
	for _, p := range projects {
		byName[p.Name] = true
	}
	for _, w := range DiscoverWildcardProjects(settings) {
		if byName[w.Name] {
			continue
		}
		byName[w.Name] = true
		projects = append(projects, w)
	}
	return projects
}

// DiscoverWildcardProjects expands every configured [[wildcard]] pattern into
// one Project per matching directory, all sharing that wildcard's template
// config file. Unlike a project discovered by DiscoverProjects' normal walk,
// a Wildcard project's identity is the (template path, matched directory)
// pair rather than the file alone — many directories legitimately share one
// template — so it is never deduplicated against the plain file-based scan.
//
// Root is the matched directory, not the template's own session.root — a
// wildcard template conventionally sets that to "." as a placeholder (see
// Wildcard.Config), since callers building a session for one of these
// Projects always override cfg.Session.Root with it, config.Load alone
// having no way to know which directory this particular Project stands for.
//
// Matches are cached for wildcardCacheTTL, because this is not free: a "/**"
// pattern is a full recursive walk, and the TUI calls this every three seconds
// for as long as it is open. See matchWildcardDirs.
func DiscoverWildcardProjects(settings *Settings) []Project {
	if settings == nil {
		return nil
	}
	var out []Project
	for _, wc := range settings.Wildcard {
		if wc.Pattern == "" || wc.Config == "" {
			continue
		}
		configPath, err := ExpandPath(wc.Config)
		if err != nil {
			continue
		}
		dirs, err := matchWildcardDirs(wc.Pattern)
		if err != nil {
			continue
		}
		template, templateErr := Load(configPath)
		for _, dir := range dirs {
			// A directory's own config takes precedence regardless of where
			// discovery was invoked. Do not search its parents for a template.
			local := ""
			for _, file := range []string{DefaultFileName, LegacyFileName} {
				path := filepath.Join(dir, file)
				if _, err := os.Stat(path); err == nil {
					local = path
					break
				}
			}
			if local != "" {
				p := Project{Name: filepath.Base(dir), Path: local}
				if cfg, err := Load(local); err == nil {
					p.Name = projectNameFrom(cfg, local, false)
					p.Aliases = cfg.Session.Aliases
				}
				out = append(out, p)
				continue
			}
			p := Project{
				Name:     filepath.Base(dir),
				Path:     configPath,
				Root:     dir,
				Wildcard: true,
			}
			if templateErr == nil {
				identity := template.Session
				identity.Root = dir
				if name, _, err := identity.Resolve(template.Dir()); err == nil {
					p.Name = name
				}
			}
			out = append(out, p)
		}
	}
	return out
}

// WildcardMatches reports the directories a configured [[wildcard]] currently
// matches. DiscoverWildcardProjects flattens every pattern into one project
// list, which is the right shape for building sessions but loses which pattern
// produced what — and "this pattern matches nothing" is exactly the kind of
// silent no-op `wyrm doctor` exists to surface.
func WildcardMatches(w Wildcard) ([]string, error) {
	if w.Pattern == "" {
		return nil, errors.New("pattern is empty")
	}
	return matchWildcardDirs(w.Pattern)
}

// wildcardCacheTTL is how long a pattern's matches are reused before the
// filesystem is walked again.
//
// There is no (size, mtime) identity to key on the way file-based discovery
// does — a wildcard project is not a file, and for a "/**" pattern the mtime of
// the base directory says nothing about a directory created three levels down.
// So this is a plain time bound, chosen against the two callers: the TUI
// refreshes every 3 seconds (internal/tui, listRefreshInterval), so a 10s TTL
// turns a walk-per-tick into a walk every fourth tick, and a newly created
// project appears within 10 seconds rather than 3. Bulk operations over N
// sessions collapse from N walks to one.
//
// A user who wants a new directory picked up now can press the TUI's manual
// reload, which calls InvalidateWildcardCache.
const wildcardCacheTTL = 10 * time.Second

var wildcardCache sync.Map // pattern -> wildcardCacheEntry

type wildcardCacheEntry struct {
	at   time.Time
	dirs []string
	err  error
}

// InvalidateWildcardCache drops every cached wildcard match, so the next
// discovery walks the filesystem again. Called by the paths where the user has
// explicitly asked for fresh state.
func InvalidateWildcardCache() {
	wildcardCache.Range(func(k, _ any) bool {
		wildcardCache.Delete(k)
		return true
	})
}

// matchWildcardDirs resolves a wildcard pattern to the absolute directories
// it matches, reusing a recent result when one is available (see
// wildcardCacheTTL). A trailing "/**" matches every directory nested at any
// depth under the base path (not the base itself); anything else is a plain
// filepath.Glob, matching one path segment per "*" the way DiscoverProjects'
// own shared-directory glob does.
func matchWildcardDirs(pattern string) ([]string, error) {
	if v, ok := wildcardCache.Load(pattern); ok {
		e := v.(wildcardCacheEntry)
		if time.Since(e.at) < wildcardCacheTTL {
			return e.dirs, e.err
		}
	}
	dirs, err := walkWildcardDirs(pattern)
	wildcardCache.Store(pattern, wildcardCacheEntry{at: time.Now(), dirs: dirs, err: err})
	return dirs, err
}

func walkWildcardDirs(pattern string) ([]string, error) {
	expanded, err := ExpandPath(pattern)
	if err != nil {
		return nil, err
	}
	if base, ok := strings.CutSuffix(expanded, "/**"); ok {
		var dirs []string
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				// An unreadable subdirectory shouldn't cost every sibling its
				// match, so skip it rather than aborting the whole walk —
				// returning nil from a WalkDirFunc means exactly that, per
				// its documented contract, not "swallow the error".
				return nil //nolint:nilerr
			}
			if path != base && d.IsDir() {
				name := d.Name()
				if strings.HasPrefix(name, ".") || isIgnoredWildcardDir(name) {
					return filepath.SkipDir
				}
				dirs = append(dirs, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		return dirs, nil
	}

	matches, err := filepath.Glob(expanded)
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil && info.IsDir() {
			abs, err := filepath.Abs(m)
			if err != nil {
				abs = m
			}
			dirs = append(dirs, abs)
		}
	}
	return dirs, nil
}

var ignoredWildcardDirNames = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	"target":       true,
	"dist":         true,
	"build":        true,
	"venv":         true,
	"__pycache__":  true,
}

func isIgnoredWildcardDir(name string) bool {
	return ignoredWildcardDirNames[name]
}

// FindProject returns the discoverable project whose session name is name,
// or — failing that — whose session.aliases contains name. An exact project
// name always wins over an alias collision, so `wyrm <name>` stays
// deterministic even if a project happens to alias another's name.
func FindProject(settings *Settings, name string) (Project, bool) {
	return NewProjectIndex(settings).Find(name)
}

// ProjectIndex is one discovery pass, reusable for many lookups.
//
// FindProject runs a full DiscoverProjects — including the recursive wildcard
// walk — on every call, which is fine for `wyrm <name>` but not inside a loop:
// `wyrm kill --all` across 20 sessions ran 20 complete discoveries, each one
// re-walking every wildcard tree, to answer 20 questions about the same
// unchanged filesystem. Bulk callers build one of these instead.
type ProjectIndex struct {
	projects []Project
	byName   map[string]Project
	byAlias  map[string]Project
}

// NewProjectIndex runs discovery once and indexes the result by name and by
// alias, preserving FindProject's precedence: an exact project name always
// beats an alias, so `wyrm <name>` stays deterministic when a project happens
// to alias another's name. Among aliases the first discovered wins.
func NewProjectIndex(settings *Settings) ProjectIndex {
	projects := DiscoverProjects(settings)
	ix := ProjectIndex{
		projects: projects,
		byName:   make(map[string]Project, len(projects)),
		byAlias:  make(map[string]Project, len(projects)),
	}
	for _, p := range projects {
		if _, taken := ix.byName[p.Name]; !taken {
			ix.byName[p.Name] = p
		}
	}
	for _, p := range projects {
		for _, a := range p.Aliases {
			if _, taken := ix.byAlias[a]; !taken {
				ix.byAlias[a] = p
			}
		}
	}
	return ix
}

// Find resolves a session name to its project, by exact name first and then by
// alias — the same order FindProject documents.
func (ix ProjectIndex) Find(name string) (Project, bool) {
	if p, ok := ix.byName[name]; ok {
		return p, true
	}
	p, ok := ix.byAlias[name]
	return p, ok
}

// FindSession matches a live session's identity, never another project's alias.
// Exact names win over tmux's substituted spelling.
func (ix ProjectIndex) FindSession(name string) (Project, bool) {
	for _, p := range ix.projects {
		if p.Name == name {
			return p, true
		}
	}
	for _, p := range ix.projects {
		if tmux.SanitizeName(p.Name) == name {
			return p, true
		}
	}
	return Project{}, false
}

// Projects returns the discovered projects in discovery order.
func (ix ProjectIndex) Projects() []Project { return ix.projects }

// LoadConfig returns the config this project builds from, with the project's
// own identity applied on top of what the file says.
//
// It exists because a Project's config is not always just Load(p.Path). Two
// kinds of project carry identity the file cannot:
//
//   - A Wildcard project shares one template with every other directory the
//     pattern matched, so only p.Root says which directory this one stands for.
//     The template's session.root ("." by convention) would otherwise resolve
//     against the template's own directory.
//   - A project with no config file of its own (Path == "" — the TUI's
//     zoxide-known directories) builds from the user's default config, or
//     wyrm's built-in one, rooted at p.Root. Its session.name is cleared for
//     the same reason: the directory is the project's identity, and a named
//     default config would otherwise give every such directory the same
//     session name — which is also the name every caller looks the running
//     session up by.
//
// Every caller that turns a Project into a session — building it, attaching to
// it, or killing it — must go through here. Three of them used to call
// Load(p.Path) directly, which for a wildcard project resolved the session name
// from the template's directory: `wyrm kill <project>` reported the wrong
// session as not running, and `wyrm restart -all` built a spurious session
// rooted in the shared config directory.
func (p Project) LoadConfig() (*Config, error) {
	if p.Path == "" {
		cfg, err := LoadUserDefault()
		if err != nil {
			return nil, err
		}
		if cfg == nil {
			cfg, err = LoadDefault()
			if err != nil {
				return nil, err
			}
		}
		cfg.Session.Name = ""
		cfg.Session.Root = p.Root
		return cfg, nil
	}
	cfg, err := Load(p.Path)
	if err != nil {
		return nil, err
	}
	if p.Wildcard {
		cfg.Session.Root = p.Root
	}
	return cfg, nil
}

// ProjectName is the session name a config produces: its explicit
// session.name, else the name derived from its resolved root, else the
// filename with the .wyrm.toml suffix stripped.
//
// A shared config deliberately skips the root-derived branch. Its root is
// relative to the shared directory, not to any project, so deriving a name
// from it would name every shared project after the shared folder.
func ProjectName(path string, shared bool) string {
	cfg, err := Load(path)
	if err != nil {
		return projectNameFallback(path, shared)
	}
	return projectNameFrom(cfg, path, shared)
}

// projectNameFrom is ProjectName for a config the caller has already loaded,
// so discovery does not parse the same file twice.
func projectNameFrom(cfg *Config, path string, shared bool) string {
	if cfg != nil {
		if cfg.Session.Name != "" {
			return cfg.Session.Name
		}
		// A shared config's root is safe to derive a display name from only
		// when it doesn't depend on cfg.Dir() — the shared directory, not any
		// project. An empty or plain-relative root resolves against it (see
		// Session.Resolve's "." default), which would name every shared
		// project sharing that convention after the shared folder — but an
		// absolute (or ~/$-expanded) root names the project it actually
		// points at, exactly what Session.Resolve derives when the session
		// is actually built. Falling back to the shared filename in that
		// case (as this used to do unconditionally) let a disambiguated
		// filename like "api-123.wyrm.toml" display and discover a project
		// as "api-123" while the session it built and searched for was
		// named "api" — the two never agreeing anywhere.
		if !shared || rootIndependentOfConfigDir(cfg.Session.Root) {
			if name, _, err := cfg.Session.Resolve(cfg.Dir()); err == nil {
				return name
			}
		}
	}
	return projectNameFallback(path, shared)
}

// rootIndependentOfConfigDir reports whether root resolves to the same place
// regardless of the directory it's resolved against — true for an absolute
// path or one starting with "~" or containing a "$" expansion, false for ""
// (which defaults to ".") or a bare relative path.
func rootIndependentOfConfigDir(root string) bool {
	return root != "" && (filepath.IsAbs(root) || strings.HasPrefix(root, "~") || strings.Contains(root, "$"))
}

func projectNameFallback(path string, shared bool) string {
	base := filepath.Base(path)
	if shared {
		return strings.TrimSuffix(base, DefaultFileName)
	}
	if cwd, err := os.Getwd(); err == nil {
		return filepath.Base(cwd)
	}
	return base
}

// CheckSharedRoot reports the problem with a shared config whose session.root
// is relative, and whether there is one. A shared config lives in the shared
// directory rather than in the project, so a relative root — "." above all —
// resolves against the shared directory and builds a session rooted in the
// wrong place. Callers warn rather than refuse: the session still builds, and
// some configs legitimately set only a name.
func CheckSharedRoot(p Project, cfg *Config) (string, bool) {
	if !p.Shared || cfg == nil {
		return "", false
	}
	root := cfg.Session.Root
	if root == "" || filepath.IsAbs(root) || strings.HasPrefix(root, "~") || strings.Contains(root, "$") {
		return "", false
	}
	return "shared config " + p.Path + " has a relative session.root (" + root +
		"), which resolves against the shared directory — use an absolute path or ~/...", true
}
