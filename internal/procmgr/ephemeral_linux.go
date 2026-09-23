package procmgr

import (
	"fmt"
	"os"
)

const ephemeralSource = "/proc/sys/net/ipv4/ip_local_port_range"

func ephemeralRange() (lo, hi int, ok bool) {
	b, err := os.ReadFile(ephemeralSource)
	if err != nil {
		return 0, 0, false
	}
	if _, err := fmt.Sscan(string(b), &lo, &hi); err != nil || lo > hi {
		return 0, 0, false
	}
	return lo, hi, true
}
