//go:build windows

package trak

import (
	"os"
	"syscall"
	"unsafe"
)

var procLockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")

const (
	lockfileFailImmediately = 1
	lockfileExclusiveLock   = 2
	errorLockViolation      = syscall.Errno(33)
)

// lockFile locks one byte far past any real data, so readers are never blocked.
func lockFile(f *os.File) error {
	ol := new(syscall.Overlapped)
	ol.Offset, ol.OffsetHigh = 0xFFFFFFFE, 0x7FFFFFFF
	r, _, err := procLockFileEx.Call(f.Fd(), lockfileExclusiveLock|lockfileFailImmediately, 0, 1, 0,
		uintptr(unsafe.Pointer(ol)))
	if r == 0 {
		if err == errorLockViolation {
			return ErrLocked
		}
		return os.NewSyscallError("LockFileEx", err)
	}
	return nil
}

// syncDir does nothing: Windows has no directory fsync.
func syncDir(string) error { return nil }
