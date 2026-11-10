// Reading: recovery, navigation, tables and events (SPEC §7, §8, §9, §11).
#include <algorithm>
#include <cstdlib>
#include <unordered_map>

#include "internal.hpp"

namespace trak {
namespace detail {

namespace {
constexpr std::size_t kCacheSize = 4096;
constexpr std::int64_t kBelow = -(std::int64_t{1} << 62);

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
  by_name.clear();
  child_from.clear();
  schemas = build_schemas(schema_footers, schema_footers.size());
  for (std::size_t i = 0; i < schemas.size(); ++i) {
    by_name[schemas[i].name] = i;
    if (!schemas[i].parent.empty()) child_from.emplace(schemas[i].parent, schema_footers[i]->end);
  }
}

// Parses the first count schema objects in index order, so that children find
// their parents.
std::vector<Schema> ReaderState::build_schemas(const std::vector<FooterPtr>& footers, std::size_t count) const {
  std::vector<Schema> out;
  out.reserve(count);
  std::unordered_map<std::string, std::size_t> names;
  const SchemaLookup lookup = [&](const std::string& name) -> const Schema* {
    auto it = names.find(name);
    return it == names.end() ? nullptr : &out[it->second];
  };
  for (std::size_t i = 0; i < count; ++i) {
    const Footer& f = *footers[i];
    const std::string text(buf.substr(static_cast<std::size_t>(f.start), static_cast<std::size_t>(f.fstart - f.start)));
    out.push_back(build_schema(f.name, static_cast<int>(f.num), f.stamp, text, false, lookup));
    names[f.name] = i;
  }
  return out;
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
  for (std::size_t i = 0; i < chain.size(); ++i) {
    if (chain[i]->name != f->name) continue;
    const auto built = build_schemas(chain, i + 1);
    const Schema& schema = built.back();
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

void ReaderState::latest_all() const {
  const std::size_t want = schemas.size();
  for (FooterPtr f = last(); f && latest.size() < want; f = prev(*f)) {
    if (auto it = by_name.find(f->name); it != by_name.end()) latest.emplace(schemas[it->second].index, f);
    if (f->schema) continue;
    const std::uint64_t dt = f->vlqs[4];
    if (dt == 0 || dt > want || latest.count(static_cast<int>(dt))) continue;
    FooterPtr g = follow(*f, f->vlqs[3]);
    if (g->name != schemas[dt - 1].name) format_error(g->end, "directory pointer leads to the wrong type");
    latest.emplace(static_cast<int>(dt), std::move(g));
  }
}

FooterPtr ReaderState::hop(const Footer& f, bool jump_) const {
  const std::uint64_t want = jump_ ? jump(f.node()) : f.node() - 1;
  FooterPtr g = follow(f, jump_ ? f.vlqs[2] : f.vlqs[1]);
  if (g->name != f.name || g->node() != want) {
    format_error(f.fstart, "pointer leads to the wrong node (wanted " + f.name + " " + std::to_string(want) + ")");
  }
  return g;
}

FooterPtr ReaderState::navigate(FooterPtr f, std::uint64_t m) const {
  while (f->node() > m) f = hop(*f, jump(f->node()) >= m);
  return f;
}

FooterPtr ReaderState::seek(const Schema& s, const std::function<std::int64_t(const Footer&)>& key,
                            std::int64_t m) const {
  FooterPtr f = latest_footer(s);
  if (f->schema || key(*f) < m) return nullptr;
  while (f->node() > 1) {
    if (jump(f->node()) > 0) {
      FooterPtr g = hop(*f, true);
      if (key(*g) >= m) {
        f = std::move(g);
        continue;
      }
    }
    FooterPtr g = hop(*f, false);
    if (key(*g) < m) break;
    f = std::move(g);
  }
  return f;
}

std::int64_t ReaderState::family_number(const Footer& f, const std::string& family) const {
  const Schema& s = *schema_of(f);
  const auto it = std::find(s.lineage.begin(), s.lineage.end(), family);
  if (it == s.lineage.end()) throw std::out_of_range(f.name + " is not in family '" + family + "'");
  const auto level = static_cast<std::size_t>(it - s.lineage.begin());
  if (level == s.lineage.size() - 1) {
    const auto child = child_from.find(f.name);
    if (child == child_from.end() || child->second > f.start) return static_cast<std::int64_t>(f.num);
  }
  if (level >= f.vlqs[5]) format_error(f.fstart, f.name + " event has no number in family " + family);
  return static_cast<std::int64_t>(f.vlqs[6 + level]);
}

std::int64_t ReaderState::cursor(std::int64_t c) const {
  if (c == magic_end || c == end) return c;
  if (c <= magic_end || c > end) {
    throw ValueError("cursor " + std::to_string(c) + " is outside the file's valid objects");
  }
  try {
    const FooterPtr f = footer(c);
    auto it = by_name.find(f->name);
    if (it == by_name.end()) format_error(f->fstart, "unknown type '" + f->name + "'");
    const Schema& s = schemas[it->second];
    if (!f->schema && s.root) {
      const auto& v = f->vlqs;
      Walk(*s.root, v.data() + f->values_begin(), v.size() - f->values_begin(), f->start, f->fstart);
    }
  } catch (const FormatError&) {
    throw ValueError("cursor " + std::to_string(c) + " is not the start of an object");
  }
  return c;
}

const Schema* ReaderState::schema_of(const Footer& f) const {
  auto it = by_name.find(f.name);
  if (it == by_name.end()) format_error(f.fstart, "event of type '" + f.name + "' has no schema");
  return &schemas[it->second];
}

namespace {
// Keys for seeks; the schema (node 0) is below every key.
std::int64_t start_key(const Footer& f) { return f.schema ? kBelow : f.start; }

std::int64_t stamp_key(const Footer& f) {
  if (f.schema) return kBelow;
  try {
    return parse_timestamp(f.stamp);
  } catch (const ValueError& e) {
    throw FormatError(e.what(), f.fstart);
  }
}
}  // namespace

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
void Reader::clear_cache() const {
  s_->cache.clear();
  s_->latest.clear();
}
const std::vector<Schema>& Reader::types() const { return s_->schemas; }
std::string_view Reader::bytes() const { return s_->buf; }

const Schema& Reader::schema(std::string_view name) const {
  auto it = s_->by_name.find(std::string(name));
  if (it == s_->by_name.end()) throw std::out_of_range("no type named '" + std::string(name) + "'");
  return s_->schemas[it->second];
}

Table Reader::table(std::string_view name) const { return Table(this, &schema(name)); }

Family Reader::family(std::string_view name) const { return Family(this, &schema(name)); }

std::vector<Event> Reader::before(std::optional<std::int64_t> cursor, std::size_t limit) const {
  const auto& st = *s_;
  const std::int64_t c = cursor ? st.cursor(*cursor) : st.end;
  std::vector<Event> out;
  for (auto f = c == st.magic_end ? nullptr : st.footer(c); f && out.size() < limit; f = st.prev(*f)) {
    if (!f->schema) out.push_back(Event(this, st.schema_of(*f), f));
  }
  return out;
}

std::vector<Event> Reader::after(std::optional<std::int64_t> cursor, std::size_t limit) const {
  const auto& st = *s_;
  const std::int64_t c = cursor ? st.cursor(*cursor) : -1;
  if (limit == 0) return {};
  // The objects after c can't be found by scanning forward, since logged
  // content may look like footers. Instead find, per type, its first event
  // after c by seeking on offsets, then walk back from a point that has at
  // least limit events before it.
  std::int64_t bound = st.end;
  st.latest_all();
  for (const auto& s : st.schemas) {
    const auto top = st.latest_footer(s);
    if (top->schema) continue;
    const auto first = st.seek(s, detail::start_key, c + 1);
    if (first && limit - 1 <= top->node() - first->node()) {
      bound = std::min(bound, st.navigate(top, first->node() + limit - 1)->end);
    }
  }
  std::vector<detail::FooterPtr> found;
  for (auto f = bound == st.magic_end ? nullptr : st.footer(bound); f && f->start > c; f = st.prev(*f)) {
    if (!f->schema) found.push_back(f);
  }
  const std::size_t keep = std::min(found.size(), limit);
  std::vector<Event> out;
  out.reserve(keep);
  for (std::size_t i = found.size(); i > found.size() - keep; --i) {
    out.push_back(Event(this, st.schema_of(*found[i - 1]), found[i - 1]));
  }
  return out;
}

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
  auto f = reader_->s_->seek(*schema_, detail::stamp_key, ms);
  if (!f) return std::nullopt;
  return Event(reader_, schema_, std::move(f));
}

// --- Family ---

Family::Family(const Reader* r, const Schema* s) : reader_(r), schema_(s) {
  for (const auto& t : r->s_->schemas) {
    if (std::find(t.lineage.begin(), t.lineage.end(), s->name) != t.lineage.end()) types_.push_back(&t);
  }
}

std::int64_t Family::key(const detail::Footer& f) const {
  return f.schema ? detail::kBelow : reader_->s_->family_number(f, schema_->name);
}

namespace {
// The latest event of each of a family's types that has any.
std::vector<detail::FooterPtr> family_tops(const detail::ReaderState& st, const std::vector<const Schema*>& types) {
  if (types.size() > 1) st.latest_all();
  std::vector<detail::FooterPtr> out;
  for (const auto* s : types) {
    auto f = st.latest_footer(*s);
    if (!f->schema) out.push_back(std::move(f));
  }
  return out;
}
}  // namespace

std::int64_t Family::count() const {
  std::int64_t best = 0;
  for (const auto& f : family_tops(*reader_->s_, types_)) best = std::max(best, key(*f));
  return best;
}

std::optional<Event> Family::latest() const {
  detail::FooterPtr best;
  std::int64_t best_key = 0;
  for (auto& f : family_tops(*reader_->s_, types_)) {
    const std::int64_t k = key(*f);
    if (!best || k > best_key) {
      best_key = k;
      best = std::move(f);
    }
  }
  if (!best) return std::nullopt;
  return Event(reader_, reader_->s_->schema_of(*best), best);
}

Event Family::get(std::int64_t m) const {
  if (m >= 1) {
    const auto k = [this](const detail::Footer& f) { return key(f); };
    for (const auto* s : types_) {
      auto f = reader_->s_->seek(*s, k, m);
      if (f && key(*f) == m) return Event(reader_, s, std::move(f));
    }
  }
  throw std::out_of_range("family " + schema_->name + " has no row " + std::to_string(m));
}

namespace {
// One type's events within a family range, in order, read a chunk at a time
// like Table::range.
struct Run {
  const Schema* schema;
  std::int64_t next;  // the next node to yield
  std::int64_t last;  // the final node, inclusive
  bool down;
  std::vector<detail::FooterPtr> chunk;  // pending footers, the next one last

  detail::FooterPtr peek(const detail::ReaderState& st) {
    if (chunk.empty()) {
      if (down ? next < last : next > last) return nullptr;
      if (down) {
        // Descending steps are one read each.
        chunk.push_back(st.navigate(st.latest_footer(*schema), static_cast<std::uint64_t>(next)));
      } else {
        const std::int64_t hi = std::min(last, next + 255);
        chunk.push_back(st.navigate(st.latest_footer(*schema), static_cast<std::uint64_t>(hi)));
        while (static_cast<std::int64_t>(chunk.back()->num) > next) chunk.push_back(st.hop(*chunk.back(), false));
      }
    }
    return chunk.back();
  }

  void pop(const detail::ReaderState& st) {
    detail::FooterPtr f = chunk.back();
    chunk.pop_back();
    if (down) {
      --next;
      if (next >= last && next >= 1) chunk.push_back(st.hop(*f, false));
    } else {
      ++next;
    }
  }
};
}  // namespace

void Family::range(std::int64_t a, std::int64_t b, const std::function<bool(const Event&)>& fn) const {
  const std::int64_t total = count();
  for (auto x : {a, b}) {
    if (x < 1 || x > total) throw std::out_of_range("family " + schema_->name + " has no row " + std::to_string(x));
  }
  const auto& st = *reader_->s_;
  const std::int64_t lo = std::min(a, b), hi = std::max(a, b);
  const bool down = a > b;
  const auto k = [this](const detail::Footer& f) { return key(f); };
  std::vector<Run> runs;
  for (const auto* s : types_) {
    const auto first = st.seek(*s, k, lo);
    if (!first || key(*first) > hi) continue;
    const auto past = st.seek(*s, k, hi + 1);
    const std::int64_t last =
        (past ? static_cast<std::int64_t>(past->node()) : static_cast<std::int64_t>(st.latest_footer(*s)->node()) + 1) - 1;
    const auto first_n = static_cast<std::int64_t>(first->node());
    runs.push_back(down ? Run{s, last, first_n, true, {}} : Run{s, first_n, last, false, {}});
  }
  // Merge the runs by family number.
  for (;;) {
    Run* pick = nullptr;
    std::int64_t pick_key = 0;
    for (auto& run : runs) {
      const auto f = run.peek(st);
      if (!f) continue;
      const std::int64_t fk = key(*f);
      if (!pick || (down ? fk > pick_key : fk < pick_key)) {
        pick = &run;
        pick_key = fk;
      }
    }
    if (!pick) return;
    const auto f = pick->peek(st);
    pick->pop(st);
    if (!fn(Event(reader_, pick->schema, f))) return;
  }
}

std::optional<Event> Family::at_time(Millis ms) const {
  // Timestamps never decrease within a family, so it's the earliest of each
  // type's first such event.
  detail::FooterPtr best;
  std::int64_t best_key = 0;
  for (const auto* s : types_) {
    auto f = reader_->s_->seek(*s, detail::stamp_key, ms);
    if (!f) continue;
    const std::int64_t fk = key(*f);
    if (!best || fk < best_key) {
      best_key = fk;
      best = std::move(f);
    }
  }
  if (!best) return std::nullopt;
  return Event(reader_, reader_->s_->schema_of(*best), best);
}

// --- Event ---

Event::Event(const Reader* r, const Schema* s, std::shared_ptr<const detail::Footer> f)
    : reader_(r), schema_(s), footer_(std::move(f)) {}

const std::string& Event::type() const { return footer_->name; }
std::int64_t Event::n() const { return static_cast<std::int64_t>(footer_->num); }
const std::string& Event::timestamp() const { return footer_->stamp; }
Millis Event::millis() const { return parse_timestamp(footer_->stamp); }
std::int64_t Event::offset() const { return footer_->start; }

std::int64_t Event::family_number(std::string_view family) const {
  const std::string name = family.empty() ? schema_->lineage.front() : std::string(family);
  if (std::find(schema_->lineage.begin(), schema_->lineage.end(), name) == schema_->lineage.end()) {
    throw std::out_of_range(footer_->name + " is not in family '" + name + "'");
  }
  return reader_->s_->family_number(*footer_, name);
}

std::optional<Event> Event::prev() const {
  if (footer_->num == 1) return std::nullopt;
  return Event(reader_, schema_, reader_->s_->hop(*footer_, false));
}

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
