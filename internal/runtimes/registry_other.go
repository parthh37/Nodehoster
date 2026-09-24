//go:build !windows

package runtimes

// registryPythons is Windows' registry of interpreters; there is none
// elsewhere.
func registryPythons() []string { return nil }
