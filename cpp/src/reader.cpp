// Reading: recovery, navigation, tables and events (SPEC §7, §8, §9, §11).
#include <algorithm>
#include <cstdlib>
#include <unordered_map>

#include "internal.hpp"

namespace trak {
namespace detail {

namespace {
constexpr std::size_t kCacheSize = 4096;

std::int64_t last_lf_before(std::string_view buf, std::int64_t lo, std::int64_t hi) {
  // Index of the last LF in [lo, hi), or lo - 1 if none.
  for (std::int64_t i = hi - 1; i >= lo; --i) {
    if (buf[static_cast<std::size_t>(i)] == '\n') return i;
  }
  return lo - 1;
}
}  // namespace

void ReaderState::load() {
  const std::int64_t new_size = file.size();
  if (new_size == 0) format_error(0, "empty file: missing magic line");
  map = Mapping(file, new_size);
  buf = map.view();
  size = new_size;
  const std::string_view head = buf.substr(0, 64);
  const auto nl = head.find('\n');
  if (nl == std::string_view::npos || head.substr(0, 5) != "trak " || !canonical_version(head.substr(5, nl - 5))) {
    format_error(0, "bad magic line, expected 'trak MAJOR.MINOR'");
  }
  version = std::string(head.substr(5, nl - 5));
  const std::string major = version.substr(0, version.find('.'));
  if (major != "1") throw UnsupportedVersion(major);
  magic_end = static_cast<std::int64_t>(nl) + 1;
  end = recover(size);
  latest.clear();
  schemas.clear();
  by_name.clear();
  schemas.reserve(schema_footers.size());
  for (std::size_t i = 0; i < schema_footers.size(); ++i) {
    const Footer& f = *schema_footers[i];
    const std::string text(buf.substr(static_cast<std::size_t>(f.start), static_cast<std::size_t>(f.fstart - f.start)));
    schemas.push_back(build_schema(f.name, static_cast<int>(i + 1), f.stamp, text, false));
    by_name[f.name] = i;
  }
}

FooterPtr ReaderState::footer(std::int64_t at) const {
  ++footer_reads;
  if (auto it = cache.find(at); it != cache.end()) return it->second;
  if (at <= magic_end || at > size || buf[static_cast<std::size_t>(at - 1)] != '\n') {
    format_error(at, "pointer doesn't land on the end of an object");
  }
  const std::int64_t fstart = last_lf_before(buf, magic_end - 1, at - 1) + 1;
  auto f = std::make_shared<const Footer>(parse_footer(buf, fstart, at, magic_end));
  if (cache.size() >= kCacheSize) cache.clear();
  cache.emplace(at, f);
  return f;
}

FooterPtr ReaderState::last() const { return end == magic_end ? nullptr : footer(end); }

FooterPtr ReaderState::prev(const Footer& f) const { return f.start == magic_end ? nullptr : footer(f.start); }

FooterPtr ReaderState::follow(const Footer& f, std::uint64_t pointer) const { return footer(f.target(pointer)); }

// Finds the end of the last valid footer (SPEC §11) and the schema chain it sees.
std::int64_t ReaderState::recover(std::int64_t total) {
  tail_error.clear();
  tail_error_offset = -1;
  schema_footers.clear();
  std::int64_t pos = total;
  if (pos > magic_end && buf[static_cast<std::size_t>(pos - 1)] != '\n') pos = last_lf_before(buf, magic_end - 1, pos) + 1;
  while (pos > magic_end) {
    const std::int64_t fstart = last_lf_before(buf, magic_end - 1, pos - 1) + 1;
    const std::string_view head = buf.substr(static_cast<std::size_t>(fstart), 4);
    if (head == "##[ " || head == "==[ ") {
      try {
        schema_footers = validate_tail(pos);
        return pos;
      } catch (const FormatError& e) {
        cache.erase(pos);
        if (tail_error.empty()) {
          tail_error = e.message();
          tail_error_offset = e.offset();
        }
      } catch (const SchemaError& e) {
        cache.erase(pos);
        if (tail_error.empty()) {
          tail_error = e.what();
          tail_error_offset = pos;
        }
      }
    }
    pos = fstart;
  }
  return magic_end;
}

// Checks that the footer ending at `at` is valid by SPEC §11: it parses, its
// type is known, and its VLQ count and walk match its schema.
std::vector<FooterPtr> ReaderState::validate_tail(std::int64_t at) const {
  const FooterPtr f = footer(at);
  auto chain = schema_chain(f);
  if (f->schema) return chain;
  for (const auto& s : chain) {
    if (s->name != f->name) continue;
    const std::string text(buf.substr(static_cast<std::size_t>(s->start), static_cast<std::size_t>(s->fstart - s->start)));
    const Schema schema = build_schema(s->name, static_cast<int>(s->num), s->stamp, text, false);
    if (schema.root) {
      const auto& v = f->vlqs;
      Walk(*schema.root, v.data() + f->values_begin(), v.size() - f->values_begin(), f->start, f->fstart);
    }
    return chain;
  }
  format_error(f->fstart, "event of type '" + f->name + "' has no schema");
}

// Every schema footer visible from f, in index order: walk back to a schema or
// an event with dir type 0, then follow previous-schema pointers (SPEC §7).
std::vector<FooterPtr> ReaderState::schema_chain(const FooterPtr& f) const {
  FooterPtr g = f;
  while (g && !g->schema) {
    if (g->vlqs[4] == 0) {
      g = follow(*g, g->vlqs[3]);
      if (!g->schema) format_error(g->end, "directory pointer for the schema chain misses a schema");
      break;
    }
    g = prev(*g);
  }
  if (!g) format_error(f->fstart, "event with no schema before it");
  std::vector<FooterPtr> chain{g};
  while (chain.back()->vlqs[1] != 0) {
    g = follow(*chain.back(), chain.back()->vlqs[1]);
    if (!g->schema) format_error(g->end, "previous-schema pointer misses a schema");
    chain.push_back(g);
  }
  std::reverse(chain.begin(), chain.end());
  std::unordered_map<std::string, bool> names;
  for (std::size_t i = 0; i < chain.size(); ++i) {
    const Footer& s = *chain[i];
    if (s.num != i + 1) {
      format_error(s.fstart, "schema " + s.name + " has index " + std::to_string(s.num) + ", expected " +
                                 std::to_string(i + 1));
    }
    if (!names.emplace(s.name, true).second) format_error(s.fstart, "duplicate schema name " + s.name);
  }
  return chain;
}

FooterPtr ReaderState::latest_footer(const Schema& s) const {
  if (auto it = latest.find(s.index); it != latest.end()) return it->second;
  FooterPtr f = last();
  while (f) {
    if (f->name == s.name) break;
    if (!f->schema && f->vlqs[4] == static_cast<std::uint64_t>(s.index)) {
      f = follow(*f, f->vlqs[3]);
      if (f->name != s.name) format_error(f->end, "directory pointer leads to the wrong type");
      break;
    }
    f = prev(*f);
  }
  if (!f) format_error(end, "type " + s.name + " not found");
  latest[s.index] = f;
  return f;
}

FooterPtr ReaderState::navigate(FooterPtr f, std::uint64_t m) const {
  const std::string name = f->name;
  while (f->node() > m) {
    const std::uint64_t j = jump(f->node());
    std::uint64_t ptr = f->vlqs[1], want = f->node() - 1;
    if (j >= m) {
      ptr = f->vlqs[2];
      want = j;
    }
    FooterPtr next = follow(*f, ptr);
    if (next->name != name || next->node() != want) {
      format_error(f->fstart, "pointer leads to the wrong node (wanted " + name + " " + std::to_string(want) + ")");
    }
    f = next;
  }
  return f;
}

const Schema* ReaderState::schema_of(const Footer& f) const {
  auto it = by_name.find(f.name);
  if (it == by_name.end()) format_error(f.fstart, "event of type '" + f.name + "' has no schema");
  return &schemas[it->second];
}

}  // namespace detail

// --- Reader ---

Reader::Reader() : s_(std::make_unique<detail::ReaderState>()) {}
Reader::Reader(Reader&&) noexcept = default;
Reader& Reader::operator=(Reader&&) noexcept = default;
Reader::~Reader() = default;

Reader Reader::open(const std::string& path) {
  Reader r;
  r.s_->path = path;
  r.s_->file = detail::File(path, detail::File::Mode::Read);
  r.s_->load();
  return r;
}

const std::string& Reader::version() const { return s_->version; }
std::int64_t Reader::end() const { return s_->end; }
std::int64_t Reader::size() const { return s_->size; }
const std::string& Reader::tail_error() const { return s_->tail_error; }
std::int64_t Reader::tail_error_offset() const { return s_->tail_error_offset; }
std::int64_t Reader::footer_reads() const { return s_->footer_reads; }
void Reader::clear_cache() const { s_->cache.clear(); }
const std::vector<Schema>& Reader::types() const { return s_->schemas; }
std::string_view Reader::bytes() const { return s_->buf; }

const Schema& Reader::schema(std::string_view name) const {
  auto it = s_->by_name.find(std::string(name));
  if (it == s_->by_name.end()) throw std::out_of_range("no type named '" + std::string(name) + "'");
  return s_->schemas[it->second];
}

Table Reader::table(std::string_view name) const { return Table(this, &schema(name)); }

bool Reader::refresh() {
  if (!s_->file.is_open()) throw Error("trak: reader is closed");
  if (s_->file.size() == s_->size) return false;
  const std::int64_t old_end = s_->end;
  for (auto it = s_->cache.begin(); it != s_->cache.end();) {
    it = it->first > old_end ? s_->cache.erase(it) : std::next(it);
  }
  s_->map.reset();
  s_->load();
  return s_->end != old_end;
}

void Reader::close() {
  s_->map.reset();
  s_->buf = {};
  s_->file.close();
}

void Reader::events_backward(const std::function<bool(const Event&)>& fn) const {
  for (auto f = s_->last(); f; f = s_->prev(*f)) {
    if (!f->schema && !fn(Event(this, s_->schema_of(*f), f))) return;
  }
}

void Reader::events(const std::function<bool(const Event&)>& fn) const {
  std::vector<detail::FooterPtr> footers;
  for (auto f = s_->last(); f; f = s_->prev(*f)) footers.push_back(f);
  for (auto it = footers.rbegin(); it != footers.rend(); ++it) {
    if (!(*it)->schema && !fn(Event(this, s_->schema_of(**it), *it))) return;
  }
}

// --- Table ---

std::int64_t Table::count() const {
  return static_cast<std::int64_t>(reader_->s_->latest_footer(*schema_)->node());
}

std::optional<Event> Table::latest() const {
  auto f = reader_->s_->latest_footer(*schema_);
  if (f->schema) return std::nullopt;
  return Event(reader_, schema_, f);
}

std::shared_ptr<const detail::Footer> Table::footer_at(std::int64_t n) const {
  auto top = reader_->s_->latest_footer(*schema_);
  if (n < 1 || static_cast<std::uint64_t>(n) > top->node()) {
    throw std::out_of_range(schema_->name + " has no event " + std::to_string(n));
  }
  return reader_->s_->navigate(top, static_cast<std::uint64_t>(n));
}

Event Table::get(std::int64_t n) const { return Event(reader_, schema_, footer_at(n)); }

void Table::range(std::int64_t a, std::int64_t b, const std::function<bool(const Event&)>& fn) const {
  const std::int64_t total = count();
  for (auto x : {a, b}) {
    if (x < 1 || x > total) throw std::out_of_range(schema_->name + " has no event " + std::to_string(x));
  }
  const auto& st = *reader_->s_;
  if (a > b) {
    for (auto f = footer_at(a);; f = st.follow(*f, f->vlqs[1])) {
      if (!fn(Event(reader_, schema_, f)) || static_cast<std::int64_t>(f->num) == b) return;
    }
  }
  for (std::int64_t lo = a; lo <= b;) {
    const std::int64_t hi = std::min(b, lo + 255);
    std::vector<detail::FooterPtr> chunk{footer_at(hi)};
    while (static_cast<std::int64_t>(chunk.back()->num) > lo) chunk.push_back(st.navigate(chunk.back(), chunk.back()->num - 1));
    for (auto it = chunk.rbegin(); it != chunk.rend(); ++it) {
      if (!fn(Event(reader_, schema_, *it))) return;
    }
    lo = hi + 1;
  }
}

std::optional<Event> Table::at_time(Millis ms) const {
  const std::int64_t total = count();
  std::int64_t lo = 1, hi = total + 1;
  while (lo < hi) {
    const std::int64_t mid = lo + (hi - lo) / 2;
    const auto f = footer_at(mid);
    Millis at;
    try {
      at = parse_timestamp(f->stamp);
    } catch (const ValueError& e) {
      throw FormatError(e.what(), f->fstart);
    }
    if (at >= ms) {
      hi = mid;
    } else {
      lo = mid + 1;
    }
  }
  if (lo > total) return std::nullopt;
  return get(lo);
}

// --- Event ---

Event::Event(const Reader* r, const Schema* s, std::shared_ptr<const detail::Footer> f)
    : reader_(r), schema_(s), footer_(std::move(f)) {}

const std::string& Event::type() const { return footer_->name; }
std::int64_t Event::n() const { return static_cast<std::int64_t>(footer_->num); }
const std::string& Event::timestamp() const { return footer_->stamp; }
Millis Event::millis() const { return parse_timestamp(footer_->stamp); }
std::int64_t Event::offset() const { return footer_->start; }

const detail::Walk& Event::walk() const {
  if (!walk_) {
    if (!schema_->root) throw SchemaError("values of " + schema_->name + " are unavailable: " + schema_->error);
    const auto& v = footer_->vlqs;
    walk_ = std::make_shared<detail::Walk>(*schema_->root, v.data() + footer_->values_begin(),
                                           v.size() - footer_->values_begin(), footer_->start, footer_->fstart);
  }
  return *walk_;
}

namespace {
std::string_view slice(std::string_view buf, std::int64_t off, std::int64_t len) {
  return buf.substr(static_cast<std::size_t>(off), static_cast<std::size_t>(len));
}
}  // namespace

std::vector<Leaf> Event::leaves() const {
  const auto& w = walk();
  const auto buf = reader_->bytes();
  std::vector<Leaf> out;
  out.reserve(w.leaves.size());
  for (const auto& rec : w.leaves) {
    out.push_back(Leaf{slice(buf, rec.path_off(), rec.plen), slice(buf, rec.voff, rec.length), rec.type});
  }
  return out;
}

std::optional<std::string_view> Event::raw(std::string_view path) const {
  const auto& w = walk();
  const auto buf = reader_->bytes();
  for (const auto& rec : w.leaves) {
    if (slice(buf, rec.path_off(), rec.plen) == path) return slice(buf, rec.voff, rec.length);
  }
  return std::nullopt;
}

std::optional<std::string> Event::get(std::string_view path) const {
  auto v = raw(path);
  if (!v) return std::nullopt;
  return std::string(*v);
}

std::optional<std::int64_t> Event::as_int(std::string_view path) const {
  auto v = get(path);
  if (!v) return std::nullopt;
  std::size_t used = 0;
  const long long n = std::stoll(*v, &used);
  if (used != v->size()) throw std::invalid_argument("not an integer: " + *v);
  return n;
}

std::optional<double> Event::as_float(std::string_view path) const {
  auto v = get(path);
  if (!v) return std::nullopt;
  return std::strtod(v->c_str(), nullptr);
}

namespace {
Node to_node(const detail::WNode& n, const detail::Walk& w, std::string_view buf, const Type& t) {
  Node out;
  std::visit(
      [&](const auto& x) {
        using X = std::decay_t<decltype(x)>;
        if constexpr (std::is_same_v<X, detail::LeafRef>) {
          const auto& rec = w.leaves[x.index];
          out.data = slice(buf, rec.voff, rec.length);
        } else if constexpr (std::is_same_v<X, detail::WObject>) {
          Node::Object obj;
          for (std::size_t i = 0; i < x.members.size(); ++i) {
            obj.emplace_back(t.members[i].key, to_node(x.members[i], w, buf, *t.members[i].type));
          }
          out.data = std::move(obj);
        } else if constexpr (std::is_same_v<X, detail::WList>) {
          Node::List list;
          for (const auto& item : x.items) list.push_back(to_node(item, w, buf, *t.elem));
          out.data = std::move(list);
        } else if constexpr (std::is_same_v<X, detail::WMap>) {
          Node::Map map;
          for (const auto& e : x.entries) {
            MapEntry me;
            me.key_length = static_cast<std::size_t>(e.klen);
            if (e.first >= 0) {
              const auto& rec = w.leaves[static_cast<std::size_t>(e.first)];
              me.key = slice(buf, rec.path_off() + x.plen + 1, static_cast<std::int64_t>(e.klen));
            }
            me.value = to_node(e.child, w, buf, *t.elem);
            map.push_back(std::move(me));
          }
          out.data = std::move(map);
        }
      },
      n.data);
  return out;
}
}  // namespace

Node Event::native() const {
  const auto& w = walk();
  return to_node(w.tree, w, reader_->bytes(), *schema_->root);
}

}  // namespace trak
