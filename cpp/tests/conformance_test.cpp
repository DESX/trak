#include <gtest/gtest.h>

#include <filesystem>
#include <fstream>
#include <nlohmann/json.hpp>
#include <sstream>

#include "trak/trak.hpp"

namespace fs = std::filesystem;
using nlohmann::json;

namespace {

const fs::path kVectors = fs::path(TRAK_REPO_DIR) / "conformance";

std::string read_file(const fs::path& p) {
  std::ifstream in(p, std::ios::binary);
  std::ostringstream ss;
  ss << in.rdbuf();
  return ss.str();
}

std::vector<std::string> names(const char* sub) {
  std::vector<std::string> out;
  for (const auto& e : fs::directory_iterator(kVectors / sub)) {
    if (e.path().extension() == ".trak") out.push_back(e.path().stem().string());
  }
  std::sort(out.begin(), out.end());
  return out;
}

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

json decode(const fs::path& path) {
  auto r = trak::Reader::open(path.string());
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
                     {"description", nullable(s.description)},
                     {"parsed", s.parsed()},
                     {"annotations", annotations(s.annotations())},
                     {"fields", fields}});
  }
  json events = json::array();
  std::map<std::string, std::int64_t> counts;
  r.events([&](const trak::Event& e) {
    events.push_back({{"type", e.type()},
                      {"n", e.n()},
                      {"timestamp", e.timestamp()},
                      {"value", e.schema().parsed() ? node_json(e.native()) : json(nullptr)}});
    ++counts[e.type()];
    return true;
  });
  // Navigation must agree with the file-order scan.
  for (const auto& s : r.types()) {
    auto t = r.table(s.name);
    EXPECT_EQ(t.count(), counts[s.name]) << s.name;
    for (std::int64_t n = 1; n <= t.count(); n += 1 + t.count() / 50) EXPECT_EQ(t.get(n).n(), n);
  }
  return {{"types", types}, {"events", events}};
}

class Valid : public ::testing::TestWithParam<std::string> {};
TEST_P(Valid, Decodes) {
  const auto path = kVectors / "valid" / (GetParam() + ".trak");
  const json want = json::parse(read_file(kVectors / "valid" / (GetParam() + ".expect.json")));
  EXPECT_EQ(decode(path), want);
  EXPECT_NO_THROW(trak::check(path.string()));
}
INSTANTIATE_TEST_SUITE_P(Conformance, Valid, ::testing::ValuesIn(names("valid")),
                         [](const auto& p) { return p.param.substr(0, 3); });

class Invalid : public ::testing::TestWithParam<std::string> {};
TEST_P(Invalid, FailsCheck) {
  const auto path = kVectors / "invalid" / (GetParam() + ".trak");
  const std::string reason = read_file(kVectors / "invalid" / (GetParam() + ".reason.txt"));
  try {
    trak::check(path.string());
    ADD_FAILURE() << "check passed";
  } catch (const trak::FormatError& e) {
    const auto at = reason.find("offset: ");
    if (at != std::string::npos) {
      EXPECT_EQ(e.offset(), std::stoll(reason.substr(at + 8))) << e.what();
    }
  }
}
INSTANTIATE_TEST_SUITE_P(Conformance, Invalid, ::testing::ValuesIn(names("invalid")),
                         [](const auto& p) { return p.param.substr(0, 3); });

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

class Write : public ::testing::TestWithParam<std::string> {};
TEST_P(Write, ReproducesBytes) {
  const auto ops = json::parse(read_file(kVectors / "write" / (GetParam() + ".input.json")));
  const auto out = fs::temp_directory_path() / ("trak-write-" + GetParam() + ".trak");
  {
    auto w = trak::Writer::create(out.string(), {true, false});
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
      ASSERT_NE(schema, nullptr);
      w.write(type, to_value(op.at("value"), *schema->root), trak::parse_timestamp(op.at("timestamp").get<std::string>()));
    }
  }
  const std::string got = read_file(out), want = read_file(kVectors / "write" / (GetParam() + ".trak"));
  fs::remove(out);
  if (got != want) {
    std::size_t i = 0;
    while (i < got.size() && i < want.size() && got[i] == want[i]) ++i;
    FAIL() << "bytes differ at offset " << i;
  }
}
INSTANTIATE_TEST_SUITE_P(Conformance, Write, ::testing::ValuesIn(names("write")),
                         [](const auto& p) { return p.param.substr(0, 3); });

}  // namespace
