// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Ton Kersten

//go:build !unix

package main

// lockDataDir is a no-op on systems without flock.
func lockDataDir(string) (func(), error) { return func() {}, nil }
