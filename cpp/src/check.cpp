// Full validation of a whole file (trak check).
#include <algorithm>
#include <unordered_map>

#include "internal.hpp"

namespace trak {

namespace {

struct CheckType {
  Schema schema;
  std::vector<std::int64_t> ends;               // end offset of node 0, 1, 2, ...
  Millis last_ms = -(std::int64_t{1} << 62);  // the family's latest timestamp, kept on its root
  std::int64_t family_count = 0;
  bool has_child = false;
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
    // 1.0 and 1.1 are parsed strictly; newer minors leniently (DECISIONS I5).
    const std::string minor = f.stamp.substr(dot + 1);
    bool strict = minor == "0" || minor == "1";
    const std::string parent = detail::parent_name(f.name, f.stamp);
    if (!parent.empty()) {
      auto p = types_.find(parent);
      if (p == types_.end()) detail::format_error(f.fstart, "schema " + f.name + " comes before its parent " + parent);
      p->second.has_child = true;
      if (!p->second.schema.root) strict = false;  // the child's object can't be known either
    }
    for (std::size_t i = 0; i < body.size(); ++i) {
      if (static_cast<unsigned char>(body[i]) >= 0x80) {
        detail::format_error(f.start + static_cast<std::int64_t>(i), "schema is not ASCII");
      }
    }
    const detail::SchemaLookup lookup = [this](const std::string& name) -> const Schema* {
      auto it = types_.find(name);
      return it == types_.end() ? nullptr : &it->second.schema;
    };
    CheckType t;
    try {
      t.schema = detail::build_schema(f.name, static_cast<int>(f.num), f.stamp, std::string(body), strict, lookup);
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
    std::vector<CheckType*> lineage;
    lineage.reserve(t.schema.lineage.size());
    for (const auto& a : t.schema.lineage) lineage.push_back(&types_.at(a));
    CheckType& family = *lineage.front();
    if (ms < family.last_ms) {
      const std::string what = family.has_child ? "family " + family.schema.name : f.name;
      detail::format_error(f.fstart, what + " timestamp goes backwards");
    }
    const std::size_t levels = t.has_child ? lineage.size() : lineage.size() - 1;
    std::vector<std::uint64_t> want, got;
    want.reserve(levels);
    for (std::size_t i = 0; i < levels; ++i) want.push_back(static_cast<std::uint64_t>(lineage[i]->family_count + 1));
    const std::size_t given = static_cast<std::size_t>(std::min<std::uint64_t>(f.vlqs[5], levels));
    got.assign(f.vlqs.begin() + 6, f.vlqs.begin() + 6 + static_cast<std::ptrdiff_t>(given));
    if (got != want) {
      detail::format_error(f.fstart, f.name + " family numbers are " + list(got) + ", expected " + list(want));
    }
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
    family.last_ms = ms;
    for (CheckType* a : lineage) ++a->family_count;
  }

  static std::string list(const std::vector<std::uint64_t>& v) {
    std::string out = "[";
    for (std::size_t i = 0; i < v.size(); ++i) out += (i ? ", " : "") + std::to_string(v[i]);
    return out + "]";
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
