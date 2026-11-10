//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly || windows)

package trak

import "os"

// Platforms without flock or LockFileEx get no writer lock.
func lockFile(*os.File) error { return nil }

// syncDir does nothing on these platforms.
func syncDir(string) error { return nil }
