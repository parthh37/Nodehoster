package importer

import (
	"os"
	"path/filepath"
	"strings"
)

// fixtureFiles serves C:\sites\<name>\web.config from testdata/iis.
func fixtureFiles(p string) ([]byte, error) {
	rel, ok := strings.CutPrefix(p, `C:\sites\`)
	if !ok {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(filepath.Join("testdata", "iis", filepath.FromSlash(strings.ReplaceAll(rel, `\`, "/"))))
}
