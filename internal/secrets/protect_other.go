//go:build !windows

package secrets

// Outside Windows the key file is protected by file permissions (0600) only.
func protect(data []byte) ([]byte, error)   { return append([]byte(nil), data...), nil }
func unprotect(data []byte) ([]byte, error) { return data, nil }
