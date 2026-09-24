package runtimes

import "golang.org/x/sys/cpu"

// needsBaseline reports whether this CPU lacks AVX2, which Bun's default
// x64 builds require; its "baseline" builds run without it.
func needsBaseline() bool { return !cpu.X86.HasAVX2 }
