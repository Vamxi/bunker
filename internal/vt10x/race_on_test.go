//go:build race

package vt10x

// raceBuild shrinks exhaustive tests under the race detector; make test runs
// them in full without it.
const raceBuild = true
