// Files, memory maps and locks for POSIX and Windows.
#include <cerrno>
#include <cstring>
#include <system_error>

#include "internal.hpp"

#ifdef _WIN32
#ifndef NOMINMAX
#define NOMINMAX
#endif
#include <windows.h>
#else
#include <fcntl.h>
#include <sys/file.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <unistd.h>
#endif

namespace trak::detail {

#ifdef _WIN32

namespace {
[[noreturn]] void fail(const std::string& what) {
  throw std::system_error(static_cast<int>(GetLastError()), std::system_category(), what);
}

std::wstring widen(const std::string& s) {
  if (s.empty()) return {};
  const int n = MultiByteToWideChar(CP_UTF8, 0, s.data(), static_cast<int>(s.size()), nullptr, 0);
  std::wstring w(static_cast<std::size_t>(n), L'\0');
  MultiByteToWideChar(CP_UTF8, 0, s.data(), static_cast<int>(s.size()), w.data(), n);
  return w;
}
}  // namespace

File::File(const std::string& path, Mode mode) {
  DWORD access = GENERIC_READ, disposition = OPEN_EXISTING;
  if (mode != Mode::Read) access |= GENERIC_WRITE;
  if (mode == Mode::Create) disposition = OPEN_ALWAYS;
  if (mode == Mode::CreateNew) disposition = CREATE_NEW;
  HANDLE h = CreateFileW(widen(path).c_str(), access, FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE, nullptr,
                         disposition, FILE_ATTRIBUTE_NORMAL, nullptr);
  if (h == INVALID_HANDLE_VALUE) {
    if (GetLastError() == ERROR_FILE_EXISTS) {
      throw std::system_error(std::make_error_code(std::errc::file_exists), path);
    }
    fail(path);
  }
  handle_ = h;
}

File::File(File&& o) noexcept : handle_(o.handle_) { o.handle_ = nullptr; }
File& File::operator=(File&& o) noexcept {
  if (this != &o) {
    close();
    handle_ = o.handle_;
    o.handle_ = nullptr;
  }
  return *this;
}
File::~File() { close(); }
bool File::is_open() const noexcept { return handle_ != nullptr; }

void File::close() noexcept {
  if (handle_) CloseHandle(static_cast<HANDLE>(handle_));
  handle_ = nullptr;
}

std::int64_t File::size() const {
  LARGE_INTEGER n;
  if (!GetFileSizeEx(static_cast<HANDLE>(handle_), &n)) fail("GetFileSizeEx");
  return n.QuadPart;
}

void File::write_at(std::string_view data, std::int64_t offset) {
  while (!data.empty()) {
    OVERLAPPED ov{};
    ov.Offset = static_cast<DWORD>(offset);
    ov.OffsetHigh = static_cast<DWORD>(offset >> 32);
    const DWORD chunk = data.size() > 0x40000000 ? 0x40000000 : static_cast<DWORD>(data.size());
    DWORD written = 0;
    if (!WriteFile(static_cast<HANDLE>(handle_), data.data(), chunk, &written, &ov)) fail("WriteFile");
    data.remove_prefix(written);
    offset += written;
  }
}

void File::truncate(std::int64_t size) {
  FILE_END_OF_FILE_INFO info;
  info.EndOfFile.QuadPart = size;
  if (!SetFileInformationByHandle(static_cast<HANDLE>(handle_), FileEndOfFileInfo, &info, sizeof info)) {
    fail("SetFileInformationByHandle");
  }
}

void File::sync() {
  if (!FlushFileBuffers(static_cast<HANDLE>(handle_))) fail("FlushFileBuffers");
}

void File::lock() {
  // Lock one byte far past any real data, so readers are never blocked.
  OVERLAPPED ov{};
  ov.Offset = 0xFFFFFFFE;
  ov.OffsetHigh = 0x7FFFFFFF;
  if (!LockFileEx(static_cast<HANDLE>(handle_), LOCKFILE_EXCLUSIVE_LOCK | LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ov)) {
    if (GetLastError() == ERROR_LOCK_VIOLATION) throw LockedError("trak: file is locked by another writer");
    fail("LockFileEx");
  }
}

Mapping::Mapping(const File& file, std::int64_t size) {
  if (size == 0) return;
  HANDLE m = CreateFileMappingW(static_cast<HANDLE>(file.handle()), nullptr, PAGE_READONLY,
                                static_cast<DWORD>(size >> 32), static_cast<DWORD>(size), nullptr);
  if (!m) fail("CreateFileMapping");
  void* p = MapViewOfFile(m, FILE_MAP_READ, 0, 0, static_cast<SIZE_T>(size));
  CloseHandle(m);
  if (!p) fail("MapViewOfFile");
  data_ = static_cast<const char*>(p);
  size_ = static_cast<std::size_t>(size);
}

void Mapping::reset() noexcept {
  if (data_) UnmapViewOfFile(data_);
  data_ = nullptr;
  size_ = 0;
}

#else

namespace {
[[noreturn]] void fail(const std::string& what) { throw std::system_error(errno, std::generic_category(), what); }
}  // namespace

File::File(const std::string& path, Mode mode) {
  int flags = mode == Mode::Read ? O_RDONLY : O_RDWR;
  if (mode == Mode::Create) flags |= O_CREAT;
  if (mode == Mode::CreateNew) flags |= O_CREAT | O_EXCL;
  flags |= O_CLOEXEC;
  fd_ = ::open(path.c_str(), flags, 0666);
  if (fd_ < 0) fail(path);
}

File::File(File&& o) noexcept : fd_(o.fd_) { o.fd_ = -1; }
File& File::operator=(File&& o) noexcept {
  if (this != &o) {
    close();
    fd_ = o.fd_;
    o.fd_ = -1;
  }
  return *this;
}
File::~File() { close(); }
bool File::is_open() const noexcept { return fd_ >= 0; }

void File::close() noexcept {
  if (fd_ >= 0) ::close(fd_);
  fd_ = -1;
}

std::int64_t File::size() const {
  struct stat st;
  if (::fstat(fd_, &st) != 0) fail("fstat");
  return static_cast<std::int64_t>(st.st_size);
}

void File::write_at(std::string_view data, std::int64_t offset) {
  while (!data.empty()) {
    const ssize_t n = ::pwrite(fd_, data.data(), data.size(), static_cast<off_t>(offset));
    if (n < 0) {
      if (errno == EINTR) continue;
      fail("pwrite");
    }
    data.remove_prefix(static_cast<std::size_t>(n));
    offset += n;
  }
}

void File::truncate(std::int64_t size) {
  if (::ftruncate(fd_, static_cast<off_t>(size)) != 0) fail("ftruncate");
}

void File::sync() {
  if (::fsync(fd_) != 0) fail("fsync");
}

void File::lock() {
  if (::flock(fd_, LOCK_EX | LOCK_NB) != 0) {
    if (errno == EWOULDBLOCK) throw LockedError("trak: file is locked by another writer");
    fail("flock");
  }
}

Mapping::Mapping(const File& file, std::int64_t size) {
  if (size == 0) return;
  void* p = ::mmap(nullptr, static_cast<std::size_t>(size), PROT_READ, MAP_SHARED, file.fd(), 0);
  if (p == MAP_FAILED) fail("mmap");
  data_ = static_cast<const char*>(p);
  size_ = static_cast<std::size_t>(size);
}

void Mapping::reset() noexcept {
  if (data_) ::munmap(const_cast<char*>(data_), size_);
  data_ = nullptr;
  size_ = 0;
}

#endif

Mapping::Mapping(Mapping&& o) noexcept : data_(o.data_), size_(o.size_) {
  o.data_ = nullptr;
  o.size_ = 0;
}

Mapping& Mapping::operator=(Mapping&& o) noexcept {
  if (this != &o) {
    reset();
    data_ = o.data_;
    size_ = o.size_;
    o.data_ = nullptr;
    o.size_ = 0;
  }
  return *this;
}

Mapping::~Mapping() { reset(); }

}  // namespace trak::detail
