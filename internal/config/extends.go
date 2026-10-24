package config

import (
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"reflect"
	"slices"
)

// maxExtendsDepth bounds an extends chain. A legitimate chain is one or two
// links long; the bound turns a runaway chain into an error instead of a hang.
const maxExtendsDepth = 8

// loadChain reads the config at abs and everything it extends, merging each
// base underneath the file that names it. Every file's path and contents are
// written to h, in chain order, so the caller can fingerprint the result.
// visited holds the files already on the chain, to report a cycle.
//
// The merged config is not validated: a base is typically partial (a shared
// [session] block with no windows, say) and only the final merge has to stand
// on its own. Load validates that.
func loadChain(abs string, h hash.Hash, visited []string) (*Config, []string, error) {
	if slices.Contains(visited, abs) {
		return nil, nil, fmt.Errorf("extends cycle: %s", formatChain(append(visited, abs)))
	}
	if len(visited) >= maxExtendsDepth {
		return nil, nil, fmt.Errorf("extends chain deeper than %d: %s", maxExtendsDepth, formatChain(visited))
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		if len(visited) > 0 {
			return nil, nil, fmt.Errorf("%s extends %s: %w", visited[len(visited)-1], abs, err)
		}
		return nil, nil, err
	}
	_, _ = h.Write([]byte(abs))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(data)
	_, _ = h.Write([]byte{0})

	cfg, unknown, err := decode(data)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing %s: %w", abs, err)
	}
	if len(visited) > 0 {
		for i, key := range unknown {
			unknown[i] = fmt.Sprintf("%s (in %s)", key, abs)
		}
	}
	cfg.sources = []string{abs}
	if cfg.Extends == "" {
		return cfg, unknown, nil
	}

	basePath, err := ResolveRoot(filepath.Dir(abs), cfg.Extends)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: resolving extends: %w", abs, err)
	}
	base, baseUnknown, err := loadChain(basePath, h, append(visited, abs))
	if err != nil {
		return nil, nil, err
	}
	merged := mergeBase(base, cfg)
	merged.sources = append([]string{abs}, base.sources...)
	return merged, append(unknown, baseUnknown...), nil
}

func formatChain(chain []string) string {
	out := ""
	for i, p := range chain {
		if i > 0 {
			out += " -> "
		}
		out += p
	}
	return out
}

// mergeBase lays child over base:
//
//   - Each [session] field the child sets replaces the base's. Maps (env)
//     merge key by key, the child winning.
//   - A child window whose name matches a base window replaces it in place,
//     so a base layout can be adjusted one window at a time. Any other child
//     window is appended after the base's.
//
// Windows replace whole rather than merging their splits: a split tree has no
// stable identity per pane, so a partial merge could not say which pane a
// child entry meant.
func mergeBase(base, child *Config) *Config {
	out := &Config{}
	out.Session = mergeSession(base.Session, child.Session)

	out.Windows = append(out.Windows, base.Windows...)
	for _, w := range child.Windows {
		replaced := false
		if w.Name != "" {
			for i := range out.Windows {
				if out.Windows[i].Name == w.Name {
					out.Windows[i] = w
					replaced = true
					break
				}
			}
		}
		if !replaced {
			out.Windows = append(out.Windows, w)
		}
	}
	return out
}

// mergeSession copies every field the child leaves at its zero value from the
// base. Reflection keeps this in step with Session as fields are added.
func mergeSession(base, child Session) Session {
	out := child
	ov := reflect.ValueOf(&out).Elem()
	bv := reflect.ValueOf(base)
	for i := 0; i < ov.NumField(); i++ {
		f := ov.Field(i)
		b := bv.Field(i)
		if f.Kind() == reflect.Map {
			if b.Len() == 0 {
				continue
			}
			merged := reflect.MakeMapWithSize(f.Type(), b.Len()+f.Len())
			for _, k := range b.MapKeys() {
				merged.SetMapIndex(k, b.MapIndex(k))
			}
			for _, k := range f.MapKeys() {
				merged.SetMapIndex(k, f.MapIndex(k))
			}
			f.Set(merged)
			continue
		}
		if f.IsZero() {
			f.Set(b)
		}
	}
	return out
}
