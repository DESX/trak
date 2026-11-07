//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly || windows)

package trak

import (
	"io"
	"os"
)

// Without mmap the file is read into memory.
func mmapFile(f *os.File, size int64) ([]byte, error) {
	buf := make([]byte, size)
	_, err := io.ReadFull(io.NewSectionReader(f, 0, size), buf)
	return buf, err
}

func munmap([]byte) error { return nil }
