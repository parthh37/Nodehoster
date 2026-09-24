//go:build !linux

package procmgr

// ephemeralSource is unset: other systems do not expose the range as cheaply.
// Windows defaults to 49152-65535 (netsh int ipv4 show dynamicport tcp),
// macOS to 49152-65535 (sysctl net.inet.ip.portrange).
const ephemeralSource = ""

func ephemeralRange() (lo, hi int, ok bool) { return 0, 0, false }
