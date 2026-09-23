//go:build !windows

package config

// secure is a no-op: Ensure creates the folders 0750 and the files in them
// are written 0600 or 0640.
func (p Paths) secure() error { return nil }
