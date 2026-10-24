//go:build !unix

package config

// checkDiscoverable has no ownership model to consult off Unix.
func checkDiscoverable(_, _ string) error { return nil }
