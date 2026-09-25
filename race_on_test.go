//go:build race

package main

// raceBuild shrinks exhaustive tests under the race detector; make test runs
// them in full without it.
const raceBuild = true
