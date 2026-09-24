//go:build windows

package remote

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// protectUser protects data with DPAPI in the current user's scope (not
// CRYPTPROTECT_LOCAL_MACHINE): only this Windows account, on this
// computer, can unprotect it.
func protectUser(data []byte) ([]byte, string, error) {
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	err := windows.CryptProtectData(&in, windows.StringToUTF16Ptr("NodeHoster server connection"), nil, 0, nil,
		windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	if err != nil {
		return nil, "", err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), "dpapi", nil
}

func unprotectUser(scheme string, data []byte) ([]byte, error) {
	if scheme != "dpapi" {
		return nil, fmt.Errorf("token stored as %q, not with DPAPI", scheme)
	}
	if len(data) == 0 {
		return nil, errors.New("empty token")
	}
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}
