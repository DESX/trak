// Full validation of a whole file (trak check).
#include <unordered_map>

#include "internal.hpp"

namespace trak {

namespace {

struct CheckType {
  Schema schema;
  std::vector<std::int64_t> ends;  // end offset of node 0, 1, 2, ...
  Millis last_ms = 0;
  bool has_ts = false;
};

class Checker {
 public:
  explicit Checker(const detail::ReaderState& r) : r_(r), buf_(r.buf) {}

  std::size_t run() {
    std::vector<detail::FooterPtr> footers;
    for (auto f = r_.last(); f; f = r_.prev(*f)) footers.push_back(f);
    for (auto it = footers.rbegin(); it != footers.rend(); ++it) {
      if ((*it)->schema) {
        schema(**it);
      } else {
        event(**it);
      }
    }
    return footers.size();
  }

 private:
  void pointer(const detail::Footer& f, std::uint64_t value, std::int64_t target_end, const char* what) {
    const auto want = static_cast<std::uint64_t>(f.start - target_end + 1);
    if (value != want) {
      detail::format_error(f.fstart, std::string(what) + " pointer is " + std::to_string(value) + ", expected " +
                                         std::to_string(want));
    }
  }

  void schema(const detail::Footer& f) {
    if (!detail::valid_type_name(f.name)) detail::format_error(f.fstart, "bad type name '" + f.name + "'");
    if (types_.count(f.name)) detail::format_error(f.fstart, "duplicate schema name '" + f.name + "'");
    if (f.num != order_.size() + 1) {
      detail::format_error(f.fstart, "schema index " + std::to_string(f.num) + ", expected " +
                                         std::to_string(order_.size() + 1));
    }
    if (order_.empty()) {
      if (f.vlqs[1] != 0) detail::format_error(f.fstart, "first schema's previous-schema pointer must be 0");
    } else {
      pointer(f, f.vlqs[1], last_schema_end_, "previous-schema");
    }
    const std::string_view body =
        buf_.substr(static_cast<std::size_t>(f.start), static_cast<std::size_t>(f.fstart - f.start));
    if (body.empty() || body.back() != '\n') detail::format_error(f.fstart, "schema body must end with LF");
    const auto dot = f.stamp.find('.');
    if (f.stamp.substr(0, dot) != "1") detail::format_error(f.fstart, "unsupported schema version " + f.stamp);
    for (std::size_t i = 0; i < body.size(); ++i) {
      if (static_cast<unsigned char>(body[i]) >= 0x80) {
        detail::format_error(f.start + static_cast<std::int64_t>(i), "schema is not ASCII");
      }
    }
    CheckType t;
    try {
      t.schema = detail::build_schema(f.name, static_cast<int>(f.num), f.stamp, std::string(body),
                                      f.stamp.substr(dot + 1) == "0");
    } catch (const SchemaError& e) {
      detail::format_error(f.start, "schema " + f.name + ": " + e.what());
    }
    t.ends.push_back(f.end);
    auto& slot = types_[f.name];
    slot = std::move(t);
    order_.push_back(&slot);
    last_schema_end_ = f.end;
  }

  void event(const detail::Footer& f) {
    auto it = types_.find(f.name);
    if (it == types_.end()) detail::format_error(f.fstart, "event of type '" + f.name + "' before its schema");
    CheckType& t = it->second;
    const std::uint64_t n = t.ends.size();
    if (f.num != n) {
      detail::format_error(f.fstart, f.name + " event number " + std::to_string(f.num) + ", expected " +
                                         std::to_string(n));
    }
    Millis ms;
    try {
      ms = parse_timestamp(f.stamp);
    } catch (const ValueError&) {
      detail::format_error(f.fstart, "invalid timestamp " + f.stamp);
    }
    if (t.has_ts && ms < t.last_ms) detail::format_error(f.fstart, f.name + " timestamp goes backwards");
    pointer(f, f.vlqs[1], t.ends[n - 1], "step");
    pointer(f, f.vlqs[2], t.ends[jump(n)], "jump");
    const std::uint64_t dt = f.vlqs[4];
    if (dt > order_.size()) detail::format_error(f.fstart, "dir type " + std::to_string(dt) + " is not defined yet");
    pointer(f, f.vlqs[3], dt == 0 ? last_schema_end_ : order_[dt - 1]->ends.back(), "dir");
    if (t.schema.root) {
      const auto& v = f.vlqs;
      detail::Walk w(*t.schema.root, v.data() + f.values_begin(), v.size() - f.values_begin(), f.start, f.fstart);
      framing(*t.schema.root, w.tree, "", w);
    }
    t.ends.push_back(f.end);
    t.last_ms = ms;
    t.has_ts = true;
  }

  static std::string join(const std::string& path, std::string_view key) {
    return path.empty() ? std::string(key) : path + "." + std::string(key);
  }

  // Checks the bytes around each value: paths, separators and block labels.
  void framing(const Type& t, const detail::WNode& node, const std::string& path, const detail::Walk& w) {
    if (const auto* leaf = std::get_if<detail::LeafRef>(&node.data)) {
      check_leaf(w.leaves[leaf->index], path);
    } else if (const auto* obj = std::get_if<detail::WObject>(&node.data)) {
      for (std::size_t i = 0; i < obj->members.size(); ++i) {
        framing(*t.members[i].type, obj->members[i], join(path, t.members[i].key), w);
      }
    } else if (const auto* list = std::get_if<detail::WList>(&node.data)) {
      for (std::size_t i = 0; i < list->items.size(); ++i) framing(*t.elem, list->items[i], join(path, std::to_string(i)), w);
    } else if (const auto* map = std::get_if<detail::WMap>(&node.data)) {
      for (const auto& e : map->entries) {
        if (e.first < 0) continue;
        const auto off = w.leaves[static_cast<std::size_t>(e.first)].path_off() + static_cast<std::int64_t>(path.size()) + 1;
        const auto key = buf_.substr(static_cast<std::size_t>(off), static_cast<std::size_t>(e.klen));
        framing(*t.elem, e.child, join(path, key), w);
      }
    }
  }

  void check_leaf(const detail::LeafRec& rec, const std::string& path) {
    const std::int64_t end = rec.voff + rec.length;
    auto at = [&](std::int64_t off, std::size_t len) { return buf_.substr(static_cast<std::size_t>(off), len); };
    if (rec.type->block) {
      const std::string label = "\n--[ " + path + " ]--\n";
      if (at(end, label.size()) != label) detail::format_error(end, "block label for '" + path + "' doesn't match");
    } else {
      const std::string head = path + ": ";
      if (at(rec.start, head.size()) != head) detail::format_error(rec.start, "inline leaf doesn't start with '" + head + "'");
      if (buf_[static_cast<std::size_t>(end)] != '\n') detail::format_error(end, "inline leaf doesn't end with LF");
    }
    const auto value = at(rec.voff, static_cast<std::size_t>(rec.length));
    if (rec.type->kind == Kind::Num && rec.type->unknown.empty() && !valid_num(value)) {
      detail::format_error(rec.voff, "bad num value '" + std::string(value) + "'");
    }
    if (rec.type->kind == Kind::Enum) {
      for (const auto& w : rec.type->words) {
        if (value == w) return;
      }
      detail::format_error(rec.voff, "bad enum value '" + std::string(value) + "'");
    }
  }

  const detail::ReaderState& r_;
  std::string_view buf_;
  std::unordered_map<std::string, CheckType> types_;
  std::vector<CheckType*> order_;
  std::int64_t last_schema_end_ = 0;
};

}  // namespace

std::size_t check(const std::string& path) {
  Reader r = Reader::open(path);
  if (!r.tail_error().empty()) {
    throw FormatError("ignored tail has an invalid footer: " + r.tail_error(),
                      r.tail_error_offset() >= 0 ? r.tail_error_offset() : r.end());
  }
  return Checker(*r.s_).run();
}

}  // namespace trak
