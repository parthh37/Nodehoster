//go:build !windows

// Command nodehoster-manager is NodeHoster's desktop manager and
// notification-area icon, a Windows program.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "NodeHoster Manager is a Windows program; on this system use the web console.")
	os.Exit(1)
}
