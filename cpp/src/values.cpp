// Event values: encoding a Value into a body and VLQs, and walking them back
// (SPEC §6, §8).
#include <charconv>
#include <cmath>
#include <set>

#include "internal.hpp"

namespace trak::detail {

namespace {

std::string show(const std::string& path) { return path.empty() ? "<root>" : path; }

std::string join(const std::string& path, std::string_view key) {
  std::string out;
  out.reserve(path.size() + 1 + key.size());
  if (!path.empty()) {
    out += path;
    out += '.';
  }
  out += key;
  return out;
}

std::string format_double(double d) {
  // Shortest representation that round-trips.
  char buf[32];
  for (int precision = 1; precision <= 17; ++precision) {
    std::snprintf(buf, sizeof buf, "%.*g", precision, d);
    if (std::strtod(buf, nullptr) == d) break;
  }
  std::string s = buf;
  // %g writes exponents like e+16 or e-05; both match the num grammar.
  return s;
}

std::string leaf_bytes(const Type& t, const Value& v, const std::string& path) {
  const auto& d = v.data();
  if (const auto* s = std::get_if<std::string>(&d)) {
    if (t.kind == Kind::Num && !valid_num(*s)) throw ValueError(show(path) + ": '" + *s + "' is not a valid num");
    if (t.kind == Kind::Enum) {
      bool ok = false;
      for (const auto& w : t.words) ok = ok || w == *s;
      if (!ok) {
        std::string words;
        for (const auto& w : t.words) words += (words.empty() ? "" : ", ") + w;
        throw ValueError(show(path) + ": '" + *s + "' is not one of " + words);
      }
    }
    return *s;
  }
  if (t.kind != Kind::Num) throw ValueError(show(path) + ": needs a string");
  if (const auto* i = std::get_if<std::int64_t>(&d)) return std::to_string(*i);
  if (const auto* u = std::get_if<std::uint64_t>(&d)) return std::to_string(*u);
  if (const auto* f = std::get_if<double>(&d)) {
    if (!std::isfinite(*f)) throw ValueError(show(path) + ": num must be finite");
    return format_double(*f);
  }
  throw ValueError(show(path) + ": num needs a number or a decimal string");
}

struct Encoder {
  std::string& body;
  std::vector<std::uint64_t>& vals;

  void object(const Type& t, const Value& v, const std::string& path) {
    const auto* dict = std::get_if<Value::Dict>(&v.data());
    if (!dict) throw ValueError(show(path) + ": expected an object");
    for (const auto& [k, _] : *dict) {
      bool known = false;
      for (const auto& m : t.members) known = known || m.key == k;
      if (!known) throw ValueError(show(path) + ": unknown field '" + k + "'");
    }
    static const Value absent;
    for (const auto& m : t.members) {
      const Value* found = &absent;
      for (const auto& [k, x] : *dict) {
        if (k == m.key) {
          found = &x;
          break;
        }
      }
      value(*m.type, *found, join(path, m.key));
    }
  }

  void value(const Type& t, const Value& v, const std::string& path) {
    if (v.is_null()) {
      if (!t.optional) throw ValueError(show(path) + ": value is required");
      vals.push_back(0);
      return;
    }
    if (t.optional) vals.push_back(1);
    switch (t.kind) {
      case Kind::Str:
      case Kind::Num:
      case Kind::Enum: {
        const std::string data = leaf_bytes(t, v, path);
        vals.push_back(data.size());
        if (t.block) {
          body += data;
          body += "\n--[ ";
          body += path;
          body += " ]--\n";
        } else {
          body += path;
          body += ": ";
          body += data;
          body += '\n';
        }
        return;
      }
      case Kind::Object:
        object(t, v, path);
        return;
      case Kind::List: {
        const auto* list = std::get_if<Value::List>(&v.data());
        if (!list) throw ValueError(show(path) + ": expected a list");
        vals.push_back(list->size());
        for (std::size_t i = 0; i < list->size(); ++i) value(*t.elem, (*list)[i], join(path, std::to_string(i)));
        return;
      }
      case Kind::Map: {
        const auto* dict = std::get_if<Value::Dict>(&v.data());
        if (!dict) throw ValueError(show(path) + ": expected a map");
        vals.push_back(dict->size());
        std::set<std::string_view> seen;
        for (const auto& [k, x] : *dict) {
          if (!seen.insert(k).second) throw ValueError(show(path) + ": repeated map key '" + k + "'");
          vals.push_back(k.size());
          value(*t.elem, x, join(path, k));
        }
        return;
      }
    }
  }
};

// Lists of empty objects cost no bytes in the file; bound them so a tiny file
// can't decode into a huge tree (DECISIONS I11).
constexpr std::uint64_t kMaxEmptyElements = 1 << 16;

bool zero_size(const Type& t) {
  if (t.kind != Kind::Object || t.optional) return false;
  for (const auto& m : t.members) {
    if (!zero_size(*m.type)) return false;
  }
  return true;
}

std::int64_t digits(std::size_t i) { return static_cast<std::int64_t>(std::to_string(i).size()); }

}  // namespace

void encode_value(const Type& root, const Value& value, std::string& body, std::vector<std::uint64_t>& vals) {
  Encoder{body, vals}.object(root, value, "");
}

Walk::Walk(const Type& root, const std::uint64_t* vals, std::size_t count, std::int64_t start, std::int64_t end)
    : vals_(vals), count_(count), off_(start), end_(end) {
  tree = object(root, -1);
  if (vi_ != count_) format_error(end_, "event has more value VLQs than its schema uses");
  if (off_ != end_) format_error(off_, "value walk doesn't end at the footer");
}

std::uint64_t Walk::next() {
  if (vi_ >= count_) format_error(end_, "event has fewer value VLQs than its schema needs");
  return vals_[vi_++];
}

WNode Walk::object(const Type& t, std::int64_t plen) {
  WObject obj;
  obj.members.reserve(t.members.size());
  for (const auto& m : t.members) obj.members.push_back(node(*m.type, plen + 1 + static_cast<std::int64_t>(m.key.size())));
  return WNode{std::move(obj)};
}

WNode Walk::node(const Type& t, std::int64_t plen) {
  if (t.optional) {
    const auto flag = next();
    if (flag == 0) return WNode{};
    if (flag != 1) format_error(end_, "optional flag must be 0 or 1");
  }
  switch (t.kind) {
    case Kind::Str:
    case Kind::Num:
    case Kind::Enum: {
      const std::uint64_t length = next();
      const std::int64_t start = off_;
      const auto room = static_cast<std::uint64_t>(end_ - start);
      const auto p = static_cast<std::uint64_t>(plen);
      std::int64_t voff;
      if (t.block) {
        if (length > room || p + 10 > room - length) format_error(start, "value walk runs past the footer");
        voff = start;
        off_ = start + static_cast<std::int64_t>(length) + plen + 10;
      } else {
        if (p + 3 > room || length > room - p - 3) format_error(start, "value walk runs past the footer");
        voff = start + plen + 2;
        off_ = voff + static_cast<std::int64_t>(length) + 1;
      }
      leaves.push_back(LeafRec{&t, start, voff, static_cast<std::int64_t>(length), plen});
      return WNode{LeafRef{leaves.size() - 1}};
    }
    case Kind::Object:
      return object(t, plen);
    case Kind::List: {
      const std::uint64_t n = next();
      WList list;
      if (zero_size(*t.elem)) {
        if (n > kMaxEmptyElements) format_error(end_, "list of empty objects is too long to decode");
        for (std::uint64_t i = 0; i < n; ++i) list.items.push_back(object(*t.elem, 0));
        return WNode{std::move(list)};
      }
      if (n > count_) format_error(end_, "list count exceeds the footer's VLQs");
      list.items.reserve(static_cast<std::size_t>(n));
      for (std::size_t i = 0; i < n; ++i) list.items.push_back(node(*t.elem, plen + 1 + digits(i)));
      return WNode{std::move(list)};
    }
    case Kind::Map: {
      const std::uint64_t n = next();
      if (n > count_) format_error(end_, "map count exceeds the footer's VLQs");
      WMap map;
      map.plen = plen;
      map.entries.reserve(static_cast<std::size_t>(n));
      for (std::uint64_t i = 0; i < n; ++i) {
        const std::uint64_t klen = next();
        if (klen > static_cast<std::uint64_t>(end_)) format_error(end_, "map key length runs past the footer");
        const std::size_t first = leaves.size();
        WNode child = node(*t.elem, plen + 1 + static_cast<std::int64_t>(klen));
        map.entries.push_back(
            WMapEntry{klen, leaves.size() == first ? -1 : static_cast<std::int64_t>(first), std::move(child)});
      }
      return WNode{std::move(map)};
    }
  }
  return WNode{};
}

}  // namespace trak::detail
