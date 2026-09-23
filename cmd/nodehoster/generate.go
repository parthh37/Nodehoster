package main

// The executable's resources: the product icon (shared with NodeHoster
// Manager, whose generator draws it), version information and a manifest
// (asInvoker; long-path aware, for deep node_modules trees). The .syso files
// are committed so that a plain `go build` produces them; CI regenerates
// them with the release version.
//
//go:generate go run github.com/tc-hib/go-winres@v0.3.3 make --in winres/winres.json --arch amd64,arm64 --out rsrc
