//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package trak

import (
	"os"
	"syscall"
)

func mmapFile(f *os.File, size int64) ([]byte, error) {
	if size == 0 {
		return nil, nil
	}
	return syscall.Mmap(int(f.Fd()), 0, int(size), syscall.PROT_READ, syscall.MAP_SHARED)
}

func munmap(b []byte) error {
	if b == nil {
		return nil
	}
	return syscall.Munmap(b)
}
