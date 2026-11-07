//go:build windows

package trak

import (
	"os"
	"syscall"
	"unsafe"
)

func mmapFile(f *os.File, size int64) ([]byte, error) {
	if size == 0 {
		return nil, nil
	}
	h, err := syscall.CreateFileMapping(syscall.Handle(f.Fd()), nil, syscall.PAGE_READONLY,
		uint32(size>>32), uint32(size), nil)
	if err != nil {
		return nil, os.NewSyscallError("CreateFileMapping", err)
	}
	defer syscall.CloseHandle(h)
	addr, err := syscall.MapViewOfFile(h, syscall.FILE_MAP_READ, 0, 0, uintptr(size))
	if err != nil {
		return nil, os.NewSyscallError("MapViewOfFile", err)
	}
	return unsafe.Slice((*byte)(unsafe.Add(nil, addr)), int(size)), nil
}

func munmap(b []byte) error {
	if b == nil {
		return nil
	}
	return syscall.UnmapViewOfFile(uintptr(unsafe.Pointer(&b[0])))
}
