//go:build !unix

package main

// lockDataDir is a no-op on systems without flock.
func lockDataDir(string) (func(), error) { return func() {}, nil }
