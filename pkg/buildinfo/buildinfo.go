// Package buildinfo carries version identity stamped in at link time.
package buildinfo

var (
	// Version is the semantic version or development revision.
	Version = "dev"
	// Commit is the source commit embedded in the binary.
	Commit = "unknown"
)
