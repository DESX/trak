// trak: a log format that programs write and read, and people can read too.
// The format is specified in SPEC.md at the root of the repository.
//
// Writing:
//
//   auto w = trak::Writer::create("app.trak");
//   w.define("build", "{ user: str, duration: num }");
//   w.write("build", trak::Value::dict({{"user", "ana"}, {"duration", 910}}));
//
// Reading:
//
//   auto r = trak::Reader::open("app.trak");
//   auto builds = r.table("build");
//   std::cout << builds.count() << "\n";
//
// Readers and writers are not safe for concurrent use.
#pragma once

#include <cstddef>
#include <cstdint>
#include <functional>
#include <initializer_list>
#include <map>
#include <memory>
#include <optional>
#include <stdexcept>
#include <string>
#include <string_view>
#include <type_traits>
#include <utility>
#include <variant>
#include <vector>

namespace trak {

const char* version() noexcept;

// --- errors ---------------------------------------------------------------

// Base class of every error trak throws.
class Error : public std::runtime_error {
 public:
  using std::runtime_error::runtime_error;
};

// The file's bytes don't follow the format. offset() is where the problem was
// found, or -1 when unknown.
class FormatError : public Error {
 public:
  FormatError(const std::string& message, std::int64_t offset);
  std::int64_t offset() const noexcept { return offset_; }
  const std::string& message() const noexcept { return message_; }

 private:
  std::string message_;
  std::int64_t offset_;
};

// The file's major version is not 1.
class UnsupportedVersion : public FormatError {
 public:
  explicit UnsupportedVersion(const std::string& major);
};

// A schema definition is invalid. line() and column() are 1-based, or 0.
class SchemaError : public Error {
 public:
  SchemaError(const std::string& message, int line = 0, int column = 0);
  int line() const noexcept { return line_; }
  int column() const noexcept { return column_; }
  const std::string& message() const noexcept { return message_; }

 private:
  std::string message_;
  int line_;
  int column_;
};

// A value doesn't fit its schema: a bad num or enum, a missing field, ...
class ValueError : public Error {
 public:
  using Error::Error;
};

// Another writer holds the file's lock.
class LockedError : public Error {
 public:
  using Error::Error;
};

// --- letter VLQ and jumps -------------------------------------------------------

// Appends the letter VLQ for v (SPEC §4).
void append_vlq(std::string& out, std::uint64_t v);
// Decodes concatenated VLQs. offset locates data in the file for errors.
std::vector<std::uint64_t> decode_vlqs(std::string_view data, std::int64_t offset = 0);
// J(n) from SPEC §7, in O(log n).
std::uint64_t jump(std::uint64_t n) noexcept;
// Whether s matches the num grammar of SPEC §5.
bool valid_num(std::string_view s) noexcept;

// --- timestamps --------------------------------------------------------------

// Milliseconds since the Unix epoch, UTC.
using Millis = std::int64_t;
// Formats as YYYYMMDDTHHMMSS.sssZ.
std::string format_timestamp(Millis ms);
// Parses YYYYMMDDTHHMMSS.sssZ; throws ValueError.
Millis parse_timestamp(std::string_view s);
Millis now_millis();

// --- schemas -------------------------------------------------------------------

enum class Kind { Str, Num, Enum, Object, List, Map };

struct Annotation {
  std::string name;
  std::string text;
  bool has_text = false;
  std::string to_string() const;
};

struct Member;

// A node of a type tree. Suffixes (? and annotations) apply to the node they
// follow, including its prefixes (DECISIONS D1).
struct Type {
  Kind kind = Kind::Str;
  bool optional = false;
  std::vector<Annotation> annotations;
  // Leaves.
  std::vector<std::string> words;  // enum words
  bool block = false;
  std::string unknown;  // original text of an unknown leaf, read as str
  // Objects.
  std::vector<Member> members;
  // Lists and maps: the element or value type.
  std::shared_ptr<Type> elem;

  bool is_leaf() const noexcept { return kind == Kind::Str || kind == Kind::Num || kind == Kind::Enum; }
};

struct Member {
  std::string key;
  std::shared_ptr<Type> type;
  std::string description;
};

// A leaf column. path writes list indexes as [] and map keys as {}.
struct Field {
  std::string path;
  std::string type;  // "str", "num" or "enum"
  std::vector<std::string> enum_words;
  std::string unknown;
  bool block = false;
  bool optional = false;
  std::vector<Annotation> annotations;
  std::string description;
};

struct Schema {
  std::string name;
  int index = 0;
  std::string version;
  std::string text;
  std::string description;
  std::shared_ptr<Type> root;  // null when the definition can't be parsed
  std::vector<Field> fields;
  std::string error;  // why root is null

  bool parsed() const noexcept { return root != nullptr; }
  const std::vector<Annotation>& annotations() const;
  const Field* field(std::string_view path) const;
};

// Parses a definition. With strict, unknown leaves are errors (a 1.0 writer's
// view); without, they read as str (SPEC §9). Throws SchemaError.
std::pair<std::shared_ptr<Type>, std::string> parse_definition(std::string_view text, bool strict);

// --- values to write ---------------------------------------------------------

// A native value tree for Writer::write. Dict serves objects and maps; maps
// are written in the given order. A String given for a num leaf is
// validated against the num grammar.
class Value {
 public:
  using List = std::vector<Value>;
  using Dict = std::vector<std::pair<std::string, Value>>;
  using Data = std::variant<std::monostate, std::string, std::int64_t, std::uint64_t, double, List, Dict>;

  Value() = default;
  Value(std::nullptr_t) {}
  Value(const char* s) : data_(std::string(s)) {}
  Value(std::string s) : data_(std::move(s)) {}
  Value(std::string_view s) : data_(std::string(s)) {}
  template <class T, std::enable_if_t<std::is_integral_v<T> && !std::is_same_v<T, bool> && std::is_signed_v<T>, int> = 0>
  Value(T v) : data_(static_cast<std::int64_t>(v)) {}
  template <class T, std::enable_if_t<std::is_integral_v<T> && !std::is_same_v<T, bool> && std::is_unsigned_v<T>, int> = 0>
  Value(T v) : data_(static_cast<std::uint64_t>(v)) {}
  Value(double v) : data_(v) {}
  Value(bool) = delete;
  Value(List v) : data_(std::move(v)) {}
  Value(Dict v) : data_(std::move(v)) {}

  static Value list(std::initializer_list<Value> items) { return Value(List(items)); }
  static Value dict(std::initializer_list<std::pair<std::string, Value>> items) { return Value(Dict(items)); }

  bool is_null() const noexcept { return std::holds_alternative<std::monostate>(data_); }
  const Data& data() const noexcept { return data_; }

 private:
  Data data_;
};

// Converts a C++ value to a Value. Specialize it for your own types.
template <class T, class = void>
struct ValueTraits;

template <class T>
Value to_value(const T& v) {
  return ValueTraits<T>::to_value(v);
}

template <class T>
struct ValueTraits<T, std::enable_if_t<std::is_constructible_v<Value, T>>> {
  static Value to_value(const T& v) { return Value(v); }
};
template <class T>
struct ValueTraits<std::optional<T>> {
  static Value to_value(const std::optional<T>& v) { return v ? trak::to_value(*v) : Value(); }
};
template <class T>
struct ValueTraits<std::vector<T>, std::enable_if_t<!std::is_same_v<T, Value>>> {
  static Value to_value(const std::vector<T>& v) {
    Value::List out;
    out.reserve(v.size());
    for (const auto& x : v) out.push_back(trak::to_value(x));
    return Value(std::move(out));
  }
};
template <class T>
struct ValueTraits<std::map<std::string, T>> {
  static Value to_value(const std::map<std::string, T>& v) {
    Value::Dict out;
    for (const auto& [k, x] : v) out.emplace_back(k, trak::to_value(x));
    return Value(std::move(out));
  }
};

// --- reading --------------------------------------------------------------------

namespace detail {
struct Footer;
struct Walk;
struct ReaderState;
}  // namespace detail

// A decoded value: absent, a leaf (a view into the mapped file), an object
// (members in schema order), a list, or a map. A map entry's key is unknown
// when the entry has no leaves (SPEC §6).
struct Node;
struct MapEntry;
struct Node {
  using Object = std::vector<std::pair<std::string, Node>>;
  using List = std::vector<Node>;
  using Map = std::vector<MapEntry>;
  std::variant<std::monostate, std::string_view, Object, List, Map> data;

  bool absent() const noexcept { return std::holds_alternative<std::monostate>(data); }
};
struct MapEntry {
  std::optional<std::string_view> key;
  std::size_t key_length = 0;
  Node value;
};

// One present leaf of an event.
struct Leaf {
  std::string_view path;
  std::string_view value;
  const Type* type = nullptr;
};

class Reader;

// One event. Values are decoded lazily on first access. String views point
// into the mapped file and stay valid until the Reader is closed or refreshed.
class Event {
 public:
  const std::string& type() const;
  std::int64_t n() const;
  const std::string& timestamp() const;
  Millis millis() const;
  std::int64_t offset() const;
  const Schema& schema() const { return *schema_; }

  // A leaf's value bytes, or nullopt when absent. Paths are as written in the
  // file ("tags.1"); when map keys contain '.', the first matching leaf wins.
  std::optional<std::string_view> raw(std::string_view path) const;
  std::optional<std::string> get(std::string_view path) const;
  std::optional<std::int64_t> as_int(std::string_view path) const;
  std::optional<double> as_float(std::string_view path) const;
  std::vector<Leaf> leaves() const;
  Node native() const;

 private:
  friend class Reader;
  friend class Table;
  Event(const Reader* r, const Schema* s, std::shared_ptr<const detail::Footer> f);
  const detail::Walk& walk() const;
  const Reader* reader_;
  const Schema* schema_;
  std::shared_ptr<const detail::Footer> footer_;
  mutable std::shared_ptr<detail::Walk> walk_;
};

// The events of one type, numbered from 1.
class Table {
 public:
  const Schema& schema() const { return *schema_; }
  const std::string& name() const { return schema_->name; }
  std::int64_t count() const;
  std::optional<Event> latest() const;
  // Throws std::out_of_range for a missing n.
  Event get(std::int64_t n) const;
  // Events a to b inclusive, descending when a > b, until fn returns false.
  void range(std::int64_t a, std::int64_t b, const std::function<bool(const Event&)>& fn) const;
  // The first event with timestamp >= ms, or nullopt.
  std::optional<Event> at_time(Millis ms) const;

 private:
  friend class Reader;
  Table(const Reader* r, const Schema* s) : reader_(r), schema_(s) {}
  std::shared_ptr<const detail::Footer> footer_at(std::int64_t n) const;
  const Reader* reader_;
  const Schema* schema_;
};

// A read-only, memory-mapped view of a trak file. It takes no lock.
class Reader {
 public:
  static Reader open(const std::string& path);
  Reader(Reader&&) noexcept;
  Reader& operator=(Reader&&) noexcept;
  ~Reader();

  const std::string& version() const;
  std::int64_t end() const;
  std::int64_t size() const;
  // Why the last complete footer line after end() is invalid, or "".
  const std::string& tail_error() const;
  std::int64_t tail_error_offset() const;
  // Footer lookups so far, and a way to forget cached footers and latest
  // nodes, for navigation bound tests.
  std::int64_t footer_reads() const;
  void clear_cache() const;

  const std::vector<Schema>& types() const;
  const Schema& schema(std::string_view name) const;  // throws std::out_of_range
  Table table(std::string_view name) const;
  // Picks up objects appended since open; returns whether anything changed.
  bool refresh();
  void close();
  std::string_view bytes() const;

  void events(const std::function<bool(const Event&)>& fn) const;
  void events_backward(const std::function<bool(const Event&)>& fn) const;

 private:
  friend class Event;
  friend class Table;
  friend class Writer;
  friend std::size_t check(const std::string& path);
  Reader();
  std::unique_ptr<detail::ReaderState> s_;
};

// --- writing --------------------------------------------------------------------

struct Options {
  bool overwrite = false;  // let create replace an existing file
  bool sync = false;       // fsync after each object
};

// Appends schemas and events to a trak file, holding an exclusive advisory
// lock on it until close.
class Writer {
 public:
  // Creates a file and writes its magic line. Fails if it exists unless
  // opts.overwrite.
  static Writer create(const std::string& path, Options opts = {});
  // Opens an existing file for appending, truncating any torn tail.
  static Writer open_append(const std::string& path, Options opts = {});
  Writer(Writer&&) noexcept;
  Writer& operator=(Writer&&) noexcept;
  ~Writer();

  const Schema& define(const std::string& name, std::string definition);
  // Timestamped now (or the type's last timestamp, if now is earlier).
  std::int64_t write(const std::string& type, const Value& value);
  // A timestamp earlier than the type's last one is a ValueError.
  std::int64_t write(const std::string& type, const Value& value, Millis timestamp);
  template <class T, std::enable_if_t<!std::is_same_v<T, Value>, int> = 0>
  std::int64_t write(const std::string& type, const T& value) {
    return write(type, to_value(value));
  }
  std::vector<const Schema*> types() const;
  // fsync. Objects are handed to the OS as they are written.
  void flush();
  void close();

 private:
  struct State;
  Writer();
  std::int64_t write_impl(const std::string& type, const Value& value, std::optional<Millis> ts);
  std::unique_ptr<State> s_;
};

// Validates a whole file and returns its number of objects. Throws
// FormatError on the first problem. A torn tail is allowed only when it holds
// no complete footer line.
std::size_t check(const std::string& path);

}  // namespace trak
