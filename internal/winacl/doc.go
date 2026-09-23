// Package winacl keeps the Windows access control lists NodeHoster relies on
// to keep its data directory away from other local users. It is Windows-only;
// elsewhere the data directory's file modes do the same job.
package winacl
