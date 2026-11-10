// Decoding and replaying conformance vectors, shared by the tests and the
// interop tool.
#include "vectors.hpp"

#include <algorithm>
#include <map>
#include <optional>

namespace trak_vectors {

using nlohmann::json;

namespace {

json nullable(const std::string& s) { return s.empty() ? json(nullptr) : json(s); }

json annotations(const std::vector<trak::Annotation>& as) {
  json out = json::array();
  for (const auto& a : as) out.push_back({{"name", a.name}, {"text", a.has_text ? json(a.text) : json(nullptr)}});
  return out;
}

json node_json(const trak::Node& n) {
  return std::visit(
      [](const auto& x) -> json {
        using X = std::decay_t<decltype(x)>;
        if constexpr (std::is_same_v<X, std::monostate>) {
          return nullptr;
        } else if constexpr (std::is_same_v<X, std::string_view>) {
          return std::string(x);
        } else if constexpr (std::is_same_v<X, trak::Node::Object>) {
          json out = json::object();
          for (const auto& [k, v] : x) out[k] = node_json(v);
          return out;
        } else if constexpr (std::is_same_v<X, trak::Node::List>) {
          json out = json::array();
          for (const auto& v : x) out.push_back(node_json(v));
          return out;
        } else {
          json out = json::array();
          for (const auto& e : x) {
            out.push_back(json::array({e.key ? json(std::string(*e.key)) : json(nullptr), node_json(e.value)}));
          }
          return out;
        }
      },
      n.data);
}

}  // namespace

json decode(const std::string& path) {
  auto r = trak::Reader::open(path);
  json types = json::array();
  for (const auto& s : r.types()) {
    json fields = json::array();
    for (const auto& f : s.fields) {
      fields.push_back({{"path", f.path},
                        {"type", f.type},
                        {"enum", f.enum_words},
                        {"unknown", nullable(f.unknown)},
                        {"block", f.block},
                        {"optional", f.optional},
                        {"annotations", annotations(f.annotations)},
                        {"description", nullable(f.description)}});
    }
    types.push_back({{"name", s.name},
                     {"index", s.index},
                     {"version", s.version},
                     {"parent", nullable(s.parent)},
                     {"description", nullable(s.description)},
                     {"parsed", s.parsed()},
                     {"annotations", annotations(s.annotations())},
                     {"fields", fields}});
  }
  json events = json::array();
  std::map<std::string, std::int64_t> counts;
  struct Row {
    std::string type;
    std::int64_t n;
    std::map<std::string, std::int64_t> families;
  };
  std::vector<Row> rows;
  r.events([&](const trak::Event& e) {
    Row row{e.type(), e.n(), {}};
    json families = json::object();
    for (const auto& a : e.schema().lineage) {
      row.families[a] = e.family_number(a);
      families[a] = row.families[a];
    }
    events.push_back({{"type", e.type()},
                      {"n", e.n()},
                      {"timestamp", e.timestamp()},
                      {"families", families},
                      {"value", e.schema().parsed() ? node_json(e.native()) : json(nullptr)}});
    ++counts[e.type()];
    rows.push_back(std::move(row));
    return true;
  });
  // Navigation must agree with the file-order scan.
  for (const auto& s : r.types()) {
    auto t = r.table(s.name);
    if (t.count() != counts[s.name]) throw std::runtime_error(s.name + ": count disagrees with a scan");
    for (std::int64_t n = 1; n <= t.count(); n += 1 + t.count() / 50) {
      if (t.get(n).n() != n) throw std::runtime_error(s.name + ": get returned the wrong event");
    }
    // So must family tables, cursors and prev().
    auto family = r.family(s.name);
    std::vector<const Row*> members;
    for (const auto& row : rows) {
      if (row.families.count(s.name)) members.push_back(&row);
    }
    if (family.count() != static_cast<std::int64_t>(members.size())) {
      throw std::runtime_error(s.name + ": family count disagrees with a scan");
    }
    for (std::size_t m = 1; m <= members.size(); ++m) {
      const Row& row = *members[m - 1];
      if (row.families.at(s.name) != static_cast<std::int64_t>(m)) {
        throw std::runtime_error(s.name + ": family number disagrees with a scan");
      }
      const auto e = family.get(static_cast<std::int64_t>(m));
      if (e.type() != row.type || e.n() != row.n) throw std::runtime_error(s.name + ": family get returned the wrong event");
    }
    if (!members.empty()) {
      std::size_t i = 0;
      family.range(1, family.count(), [&](const trak::Event& e) {
        if (i >= members.size() || e.type() != members[i]->type || e.n() != members[i]->n) {
          throw std::runtime_error(s.name + ": family iteration disagrees with a scan");
        }
        ++i;
        return true;
      });
      if (i != members.size()) throw std::runtime_error(s.name + ": family iteration is short");
    }
    if (auto latest = t.latest()) {
      auto p = latest->prev();
      if (latest->n() == 1 ? p.has_value() : (!p || p->n() != latest->n() - 1)) {
        throw std::runtime_error(s.name + ": prev returned the wrong event");
      }
    }
  }
  std::vector<std::pair<std::string, std::int64_t>> everything, pages;
  for (const auto& row : rows) everything.emplace_back(row.type, row.n);
  std::optional<std::int64_t> cursor;
  for (auto page = r.after(cursor, 3); !page.empty(); page = r.after(cursor, 3)) {
    for (const auto& e : page) pages.emplace_back(e.type(), e.n());
    cursor = page.back().offset();
  }
  if (pages != everything) throw std::runtime_error("after() pages disagree with a scan");
  pages.clear();
  cursor.reset();
  for (auto page = r.before(cursor, 4); !page.empty(); page = r.before(cursor, 4)) {
    for (const auto& e : page) pages.emplace_back(e.type(), e.n());
    cursor = page.back().offset();
  }
  std::reverse(everything.begin(), everything.end());
  if (pages != everything) throw std::runtime_error("before() pages disagree with a scan");
  return {{"types", types}, {"events", events}};
}


namespace {
// Converts input.json values using the schema: maps arrive as [key, value] pairs.
trak::Value to_value(const json& j, const trak::Type& t) {
  if (j.is_null()) return {};
  switch (t.kind) {
    case trak::Kind::Object: {
      trak::Value::Dict d;
      for (const auto& m : t.members) {
        if (j.contains(m.key)) d.emplace_back(m.key, to_value(j.at(m.key), *m.type));
      }
      return trak::Value(std::move(d));
    }
    case trak::Kind::List: {
      trak::Value::List l;
      for (const auto& x : j) l.push_back(to_value(x, *t.elem));
      return trak::Value(std::move(l));
    }
    case trak::Kind::Map: {
      trak::Value::Dict d;
      for (const auto& pair : j) d.emplace_back(pair.at(0).get<std::string>(), to_value(pair.at(1), *t.elem));
      return trak::Value(std::move(d));
    }
    default:
      if (j.is_number_unsigned()) return trak::Value(j.get<std::uint64_t>());
      if (j.is_number_integer()) return trak::Value(j.get<std::int64_t>());
      return trak::Value(j.get<std::string>());
  }
}

}  // namespace

void replay(const std::string& path, const json& ops) {
  auto w = trak::Writer::create(path, {true, false});
  for (const auto& op : ops) {
    if (op.at("op") == "define") {
      w.define(op.at("name").get<std::string>(), op.at("schema").get<std::string>());
      continue;
    }
    const std::string type = op.at("type").get<std::string>();
    const trak::Schema* schema = nullptr;
    for (const auto* s : w.types()) {
      if (s->name == type) schema = s;
    }
    if (!schema) throw trak::ValueError("type '" + type + "' is not defined");
    w.write(type, to_value(op.at("value"), *schema->root), trak::parse_timestamp(op.at("timestamp").get<std::string>()));
  }
}

}  // namespace trak_vectors
