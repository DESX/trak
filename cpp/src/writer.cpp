// Writing (SPEC §10 and the reference writer behavior in docs/api.md).
#include <algorithm>
#include <unordered_map>

#include "internal.hpp"

namespace trak {

namespace {
constexpr const char* kMagic = "trak 1.0\n";
constexpr const char* kVersion = "1.0";
constexpr std::int64_t kUnset = -(std::int64_t{1} << 62);

struct SpineNode {
  std::uint64_t node;
  std::int64_t end;
};

struct TypeState {
  Schema schema;
  std::uint64_t n = 0;
  // Node n, J(n), J(J(n)), ..., 0 with the latest last.
  std::vector<SpineNode> spine;
  Millis last_ms = 0;
  bool has_ts = false;
};
}  // namespace

struct Writer::State {
  detail::File file;
  bool sync = false;
  std::int64_t offset = 0;
  std::vector<std::unique_ptr<TypeState>> order;
  std::unordered_map<std::string, TypeState*> types;
  std::int64_t last_schema_end = 0;
  std::vector<std::int64_t> cov{-1};
  std::int64_t seq = 0;
  std::string buf;

  void write(std::string_view data) {
    if (!file.is_open()) throw Error("trak: writer is closed");
    file.write_at(data, offset);
    offset += static_cast<std::int64_t>(data.size());
    if (sync) file.sync();
  }

  void restore(Reader& r);
};

Writer::Writer() : s_(std::make_unique<State>()) {}
Writer::Writer(Writer&&) noexcept = default;
Writer& Writer::operator=(Writer&&) noexcept = default;
Writer::~Writer() = default;

Writer Writer::create(const std::string& path, Options opts) {
  Writer w;
  w.s_->file = detail::File(path, opts.overwrite ? detail::File::Mode::Create : detail::File::Mode::CreateNew);
  w.s_->sync = opts.sync;
  w.s_->file.lock();
  w.s_->file.truncate(0);
  w.s_->write(kMagic);
  return w;
}

Writer Writer::open_append(const std::string& path, Options opts) {
  Writer w;
  w.s_->file = detail::File(path, detail::File::Mode::Append);
  w.s_->sync = opts.sync;
  w.s_->file.lock();
  const std::int64_t size = w.s_->file.size();
  if (size == 0) {
    w.s_->write(kMagic);
    return w;
  }
  std::int64_t end;
  {
    // Close the reader before truncating: Windows can't truncate a mapped file.
    Reader r = Reader::open(path);
    end = r.end();
    w.s_->restore(r);
  }
  if (end < size) w.s_->file.truncate(end);
  w.s_->offset = end;
  return w;
}

void Writer::State::restore(Reader& r) {
  const auto& st = *r.s_;
  for (const auto& sf : st.schema_footers) {
    auto t = std::make_unique<TypeState>();
    t->schema = st.schemas[st.by_name.at(sf->name)];
    t->spine.push_back({0, sf->end});
    types[sf->name] = t.get();
    order.push_back(std::move(t));
    last_schema_end = sf->end;
  }
  for (auto& t : order) {
    auto top = st.latest_footer(t->schema);
    std::vector<SpineNode> spine{{top->node(), top->end}};
    for (auto f = top; f->node() > 0;) {
      f = st.navigate(f, jump(f->node()));
      spine.push_back({f->node(), f->end});
    }
    std::reverse(spine.begin(), spine.end());
    t->spine = std::move(spine);
    t->n = top->node();
    if (!top->schema) {
      try {
        t->last_ms = parse_timestamp(top->stamp);
      } catch (const ValueError& e) {
        throw FormatError(e.what(), top->fstart);
      }
      t->has_ts = true;
    }
  }
  // Rebuild coverage approximately from the last ~2K events.
  cov.assign(order.size() + 1, kUnset);
  std::int64_t seq_back = -1;
  std::size_t budget = 2 * order.size() + 2;
  for (auto f = st.last(); f && budget > 0; f = st.prev(*f)) {
    if (f->schema) continue;
    for (std::uint64_t i : {static_cast<std::uint64_t>(types.at(f->name)->schema.index), f->vlqs[4]}) {
      if (i < cov.size() && cov[i] == kUnset) cov[i] = seq_back;
    }
    --seq_back;
    --budget;
  }
}

std::vector<const Schema*> Writer::types() const {
  std::vector<const Schema*> out;
  for (const auto& t : s_->order) out.push_back(&t->schema);
  return out;
}

const Schema& Writer::define(const std::string& name, std::string definition) {
  auto& s = *s_;
  if (!detail::valid_type_name(name)) throw SchemaError("bad type name '" + name + "'");
  if (name.rfind("trak.", 0) == 0) throw SchemaError("type names starting with 'trak.' are reserved");
  if (s.types.count(name)) throw SchemaError("type '" + name + "' is already defined");
  if (definition.empty() || definition.back() != '\n') definition += '\n';
  const int index = static_cast<int>(s.order.size()) + 1;
  Schema schema = detail::build_schema(name, index, kVersion, definition, true);
  const std::int64_t start = s.offset;
  const std::uint64_t prev = s.order.empty() ? 0 : static_cast<std::uint64_t>(start - s.last_schema_end + 1);
  s.buf.assign(definition);
  detail::append_schema_footer(s.buf, name, index, kVersion, {definition.size(), prev, 0});
  s.write(s.buf);
  s.last_schema_end = s.offset;
  auto t = std::make_unique<TypeState>();
  t->schema = std::move(schema);
  t->spine.push_back({0, s.offset});
  s.types[name] = t.get();
  s.order.push_back(std::move(t));
  s.cov.push_back(-1);
  return s.order.back()->schema;
}

std::int64_t Writer::write(const std::string& type, const Value& value) { return write_impl(type, value, std::nullopt); }

std::int64_t Writer::write(const std::string& type, const Value& value, Millis timestamp) {
  return write_impl(type, value, timestamp);
}

std::int64_t Writer::write_impl(const std::string& type, const Value& value, std::optional<Millis> ts) {
  auto& s = *s_;
  auto it = s.types.find(type);
  if (it == s.types.end()) throw ValueError("type '" + type + "' is not defined");
  TypeState& t = *it->second;
  if (!t.schema.root) throw SchemaError("can't write " + type + ": " + t.schema.error);
  Millis ms;
  if (ts) {
    ms = *ts;
    if (t.has_ts && ms < t.last_ms) {
      throw ValueError("timestamp " + format_timestamp(ms) + " is earlier than " + type + "'s last event");
    }
  } else {
    ms = now_millis();
    if (t.has_ts && ms < t.last_ms) ms = t.last_ms;
  }
  std::string body;
  std::vector<std::uint64_t> values;
  detail::encode_value(*t.schema.root, value, body, values);
  const std::uint64_t n = t.n + 1;
  const std::uint64_t j = jump(n);
  const std::int64_t start = s.offset;
  const std::int64_t step_end = t.spine.back().end;
  std::int64_t jump_end = 0;
  for (auto r = t.spine.rbegin(); r != t.spine.rend(); ++r) {
    if (r->node == j) {
      jump_end = r->end;
      break;
    }
  }
  const auto index = static_cast<std::size_t>(t.schema.index);
  std::size_t best = index == 0 ? 1 : 0;
  for (std::size_t i = 0; i < s.cov.size(); ++i) {
    if (i != index && s.cov[i] < s.cov[best]) best = i;
  }
  const std::int64_t dir_end = best == 0 ? s.last_schema_end : s.order[best - 1]->spine.back().end;
  std::vector<std::uint64_t> vlqs{body.size(),
                                  static_cast<std::uint64_t>(start - step_end + 1),
                                  static_cast<std::uint64_t>(start - jump_end + 1),
                                  static_cast<std::uint64_t>(start - dir_end + 1),
                                  best,
                                  0};
  vlqs.insert(vlqs.end(), values.begin(), values.end());
  s.buf.assign(body);
  detail::append_event_footer(s.buf, type, n, format_timestamp(ms), vlqs);
  s.write(s.buf);
  while (t.spine.back().node > j) t.spine.pop_back();
  t.spine.push_back({n, s.offset});
  t.n = n;
  t.last_ms = ms;
  t.has_ts = true;
  s.cov[index] = s.cov[best] = s.seq++;
  return static_cast<std::int64_t>(n);
}

void Writer::flush() {
  if (!s_->file.is_open()) throw Error("trak: writer is closed");
  s_->file.sync();
}

void Writer::close() { s_->file.close(); }

}  // namespace trak
