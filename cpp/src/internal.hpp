// Internal declarations shared by the trak implementation files.
#pragma once

#include <cstdint>
#include <memory>
#include <string>
#include <string_view>
#include <unordered_map>
#include <variant>
#include <vector>

#include "trak/trak.hpp"

namespace trak::detail {

[[noreturn]] void format_error(std::int64_t offset, const std::string& message);

// --- footers (SPEC §5, §6, §8) ---

struct Footer {
  bool schema = false;
  std::int64_t start = 0;   // the object's first byte
  std::int64_t fstart = 0;  // the footer's first byte
  std::int64_t end = 0;     // the byte after the footer's LF
  std::string name;
  std::uint64_t num = 0;  // schema: type index; event: n
  std::string stamp;      // schema: version; event: timestamp
  std::vector<std::uint64_t> vlqs;

  std::uint64_t node() const noexcept { return schema ? 0 : num; }
  std::size_t values_begin() const noexcept { return 6 + static_cast<std::size_t>(vlqs[5]); }
  // Resolves a pointer stored in this footer to the target's end offset.
  std::int64_t target(std::uint64_t pointer) const;
};

Footer parse_footer(std::string_view buf, std::int64_t fstart, std::int64_t end, std::int64_t min_start);
void append_schema_footer(std::string& out, const std::string& name, int index, const std::string& version,
                          const std::vector<std::uint64_t>& vlqs);
void append_event_footer(std::string& out, const std::string& name, std::uint64_t n, const std::string& stamp,
                         const std::vector<std::uint64_t>& vlqs);

bool is_version(std::string_view s) noexcept;
bool canonical_version(std::string_view s) noexcept;
bool valid_type_name(std::string_view s) noexcept;
bool valid_stamp_syntax(std::string_view s) noexcept;

// --- schemas ---

// Builds a Schema. When not strict, parse failures are kept in Schema::error.
Schema build_schema(const std::string& name, int index, const std::string& version, const std::string& text,
                    bool strict);

// --- values ---

void encode_value(const Type& root, const Value& value, std::string& body, std::vector<std::uint64_t>& vals);

struct LeafRef {
  std::size_t index;
};
struct WNode;
struct WMapEntry;
struct WObject {
  std::vector<WNode> members;  // in schema order
};
struct WList {
  std::vector<WNode> items;
};
struct WMap {
  std::int64_t plen = 0;
  std::vector<WMapEntry> entries;
};
// A decoded node: absent, a leaf, an object, a list or a map.
struct WNode {
  std::variant<std::monostate, LeafRef, WObject, WList, WMap> data;
};
struct WMapEntry {
  std::uint64_t klen = 0;
  std::int64_t first = -1;  // index of the entry's first leaf, or -1
  WNode child;
};

struct LeafRec {
  const Type* type;
  std::int64_t start;
  std::int64_t voff;
  std::int64_t length;
  std::int64_t plen;
  std::int64_t path_off() const noexcept { return type->block ? voff + length + 5 : start; }
};

// Computes every leaf's offsets from an event's value VLQs (SPEC §8).
struct Walk {
  Walk(const Type& root, const std::uint64_t* vals, std::size_t count, std::int64_t start, std::int64_t end);
  std::vector<LeafRec> leaves;
  WNode tree;

 private:
  std::uint64_t next();
  WNode object(const Type& t, std::int64_t plen);
  WNode node(const Type& t, std::int64_t plen);
  const std::uint64_t* vals_;
  std::size_t count_;
  std::size_t vi_ = 0;
  std::int64_t off_;
  std::int64_t end_;
};

// --- platform ---

class File {
 public:
  enum class Mode { Read, Append, Create, CreateNew };
  File() = default;
  File(const std::string& path, Mode mode);
  File(File&& o) noexcept;
  File& operator=(File&& o) noexcept;
  ~File();
  std::int64_t size() const;
  void write_at(std::string_view data, std::int64_t offset);
  void truncate(std::int64_t size);
  void sync();
  void lock();
  void close() noexcept;
  bool is_open() const noexcept;
#ifdef _WIN32
  void* handle() const noexcept { return handle_; }
#else
  int fd() const noexcept { return fd_; }
#endif

 private:
#ifdef _WIN32
  void* handle_ = nullptr;
#else
  int fd_ = -1;
#endif
};

class Mapping {
 public:
  Mapping() = default;
  Mapping(const File& file, std::int64_t size);
  Mapping(Mapping&& o) noexcept;
  Mapping& operator=(Mapping&& o) noexcept;
  ~Mapping();
  std::string_view view() const noexcept { return {data_, size_}; }
  void reset() noexcept;

 private:
  const char* data_ = nullptr;
  std::size_t size_ = 0;
};

// --- reader state ---

using FooterPtr = std::shared_ptr<const Footer>;

struct ReaderState {
  std::string path;
  File file;
  Mapping map;
  std::string_view buf;
  std::int64_t size = 0;
  std::int64_t magic_end = 0;
  std::int64_t end = 0;
  std::string version;
  mutable std::unordered_map<std::int64_t, FooterPtr> cache;
  mutable std::unordered_map<int, FooterPtr> latest;
  std::vector<Schema> schemas;
  std::unordered_map<std::string, std::size_t> by_name;
  std::vector<FooterPtr> schema_footers;
  std::string tail_error;
  std::int64_t tail_error_offset = -1;
  mutable std::int64_t footer_reads = 0;

  void load();
  FooterPtr footer(std::int64_t end) const;
  FooterPtr last() const;
  FooterPtr prev(const Footer& f) const;
  FooterPtr follow(const Footer& f, std::uint64_t pointer) const;
  std::int64_t recover(std::int64_t size);
  std::vector<FooterPtr> validate_tail(std::int64_t end) const;
  std::vector<FooterPtr> schema_chain(const FooterPtr& f) const;
  FooterPtr latest_footer(const Schema& s) const;
  FooterPtr navigate(FooterPtr f, std::uint64_t m) const;
  const Schema* schema_of(const Footer& f) const;
};

}  // namespace trak::detail
