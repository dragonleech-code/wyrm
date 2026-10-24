package config

import (
	"errors"
	"os"
	"path/filepath"
)

// ResolutionStep is one layer of ResolveEffective's discovery chain, as
// Explain reports it.
type ResolutionStep struct {
	Layer  string
	Detail string
	// Chosen marks the layer ResolveEffective uses: the first one that found
	// something.
	Chosen bool
}

// Explain walks the same layers ResolveEffective does, in the same order,
// and says what each one found. Five layers can otherwise only be understood
// by reading the source; this is what `wyrm validate -explain` prints.
func Explain(settings *Settings, explicitPath string) []ResolutionStep {
	var steps []ResolutionStep
	chosen := false
	add := func(layer, detail string, found bool) {
		step := ResolutionStep{Layer: layer, Detail: detail}
		if found && !chosen {
			step.Chosen, chosen = true, true
		}
		steps = append(steps, step)
	}

	if explicitPath != "" {
		add("-config", explicitPath, true)
	} else {
		add("-config", "not given", false)
	}

	cwd, cwdErr := os.Getwd()
	switch {
	case settings == nil || settings.Storage != StorageShared:
		add("shared config", "storage is not \"shared\"", false)
	case cwdErr != nil:
		add("shared config", cwdErr.Error(), false)
	default:
		if shared, err := settings.SharedConfigPath(cwd); err != nil {
			add("shared config", err.Error(), false)
		} else if _, err := os.Stat(shared); err == nil {
			add("shared config", shared, true)
		} else {
			add("shared config", shared+" (not found)", false)
		}
	}

	upward := settings.UpwardDiscoveryEnabled()
	layer := "local config"
	if upward {
		layer = "local config (searching parent directories)"
	}
	if cwdErr != nil {
		add(layer, cwdErr.Error(), false)
	} else if found, err := DiscoverIn(cwd, upward); err == nil {
		if !filepath.IsAbs(found) {
			found = filepath.Join(cwd, found)
		}
		add(layer, found, true)
	} else {
		add(layer, err.Error(), false)
	}

	if path, err := UserDefaultPath(); err != nil {
		add("user default", err.Error(), false)
	} else if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		add("user default", path+" (not found)", false)
	} else {
		add("user default", path, true)
	}

	add("built-in default", "always available", true)
	return steps
}
