"""Exclusive advisory lock on a writer's file: flock on POSIX, LockFileEx on Windows."""

from __future__ import annotations

import sys

from .errors import TrakError

if sys.platform == "win32":  # pragma: no cover - exercised in Windows CI
    import ctypes
    import msvcrt
    from ctypes import wintypes

    class _Overlapped(ctypes.Structure):
        _fields_ = [
            ("Internal", ctypes.c_void_p),
            ("InternalHigh", ctypes.c_void_p),
            ("Offset", wintypes.DWORD),
            ("OffsetHigh", wintypes.DWORD),
            ("hEvent", wintypes.HANDLE),
        ]

    _kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    _LockFileEx = _kernel32.LockFileEx
    _LockFileEx.argtypes = [
        wintypes.HANDLE,
        wintypes.DWORD,
        wintypes.DWORD,
        wintypes.DWORD,
        wintypes.DWORD,
        ctypes.POINTER(_Overlapped),
    ]
    _LockFileEx.restype = wintypes.BOOL
    _EXCLUSIVE, _FAIL_IMMEDIATELY = 2, 1

    def lock(fd: int) -> None:
        # Lock one byte far past any real data so readers are never blocked.
        ov = _Overlapped(0, 0, 0xFFFFFFFE, 0x7FFFFFFF, None)
        handle = msvcrt.get_osfhandle(fd)
        if not _LockFileEx(handle, _EXCLUSIVE | _FAIL_IMMEDIATELY, 0, 1, 0, ctypes.byref(ov)):
            raise TrakError("file is locked by another writer")

else:
    import fcntl

    def lock(fd: int) -> None:
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise TrakError("file is locked by another writer") from None
