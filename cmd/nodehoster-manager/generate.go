package main

// The executable's resources: the manifest (asInvoker, so the status icon
// runs unelevated; common controls v6, which walk requires; per-monitor
// DPI awareness), the icon and version information; mkicon also writes the
// installer's icon, so both come from one drawing. The .syso files are
// committed so that a plain `go build` produces a working program; CI
// regenerates them with the release version.
//
//go:generate go run ./internal/mkicon winres ../../installer/nodehoster.ico
//go:generate go run github.com/tc-hib/go-winres@v0.3.3 make --in winres/winres.json --arch amd64,arm64 --out rsrc
