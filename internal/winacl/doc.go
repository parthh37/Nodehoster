// Package winacl keeps the Windows access control lists NodeHoster relies on
// to keep its data directory away from other local users, and checks that a
// program NodeHoster runs as the service (an interpreter it found, a runtime
// on PATH) cannot be changed by them (CheckProgram). The access control
// lists are Windows-only; elsewhere the data directory's file modes do the
// same job, and CheckProgram looks at owners and mode bits.
package winacl
