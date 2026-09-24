//go:build windows

package runtimes

import (
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

// registryPythons lists the python.exe of every interpreter registered for
// all users (PEP 514: HKLM\SOFTWARE\Python\PythonCore\<tag>\InstallPath,
// 64-bit and 32-bit views), what the py launcher reads too. Only
// administrators can write there; what the entries point to is checked
// like every other interpreter before it is run.
func registryPythons() []string {
	var out []string
	for _, view := range []uint32{registry.WOW64_64KEY, registry.WOW64_32KEY} {
		core, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Python\PythonCore`, registry.ENUMERATE_SUB_KEYS|registry.QUERY_VALUE|view)
		if err != nil {
			continue
		}
		tags, _ := core.ReadSubKeyNames(-1)
		for _, tag := range tags {
			k, err := registry.OpenKey(core, tag+`\InstallPath`, registry.QUERY_VALUE|view)
			if err != nil {
				continue
			}
			exe, _, err := k.GetStringValue("ExecutablePath")
			if err != nil || exe == "" {
				if dir, _, err := k.GetStringValue(""); err == nil && dir != "" {
					exe = filepath.Join(dir, "python.exe")
				}
			}
			k.Close()
			if exe != "" && filepath.IsAbs(exe) {
				out = append(out, exe)
			}
		}
		core.Close()
	}
	return out
}
