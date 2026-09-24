//go:build !windows

package remote

import "fmt"

// Outside Windows (development and tests) tokens are stored as they are;
// the file is readable by its owner only.
func protectUser(data []byte) ([]byte, string, error) {
	return append([]byte(nil), data...), "plain", nil
}

func unprotectUser(scheme string, data []byte) ([]byte, error) {
	if scheme != "plain" {
		return nil, fmt.Errorf("token stored as %q cannot be read here", scheme)
	}
	return data, nil
}
