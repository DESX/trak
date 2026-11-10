// Event families (SPEC §1, §5, §6, §7): schemas, numbering, tables, seeks and
// append. Mirrors python/tests/test_family.py.
#include <gtest/gtest.h>

#include <filesystem>
#include <fstream>
#include <random>
#include <sstream>

#include "trak/trak.hpp"

namespace fs = std::filesystem;
using trak::Value;

namespace {

const trak::Millis kBase = trak::parse_timestamp("20261107T140000.000Z");

trak::Millis stamp(std::int64_t i) { return kBase + i; }

std::string read_file(const fs::path& p) {
  std::ifstream in(p, std::ios::binary);
  std::ostringstream ss;
  ss << in.rdbuf();
  return ss.str();
}

void write_file(const fs::path& p, const std::string& data) {
  std::ofstream out(p, std::ios::binary | std::ios::trunc);
  out << data;
}

// Every event footer line's VLQs, in file order. The tests' values hold no
// footer-like lines.
std::vector<std::vector<std::uint64_t>> event_vlqs(const std::string& data) {
  std::vector<std::vector<std::uint64_t>> out;
  std::istringstream in(data);
  for (std::string line; std::getline(in, line);) {
    if (line.rfind("==[ ", 0) == 0) out.push_back(trak::decode_vlqs(line.substr(line.find("]==") + 3)));
  }
  return out;
}

using Op = std::pair<std::string, Value>;

Value build_value(std::initializer_list<std::pair<std::string, Value>> extra = {}) {
  Value::Dict d{{"user", "ana"}, {"ok", "yes"}};
  for (const auto& kv : extra) {
    auto it = std::find_if(d.begin(), d.end(), [&](const auto& p) { return p.first == kv.first; });
    if (it != d.end()) {
      it->second = kv.second;
    } else {
      d.push_back(kv);
    }
  }
  return Value(std::move(d));
}

void make(const std::string& path, const std::vector<Op>& ops) {
  auto w = trak::Writer::create(path, {true, false});
  w.define("build", "// A build.\n{ user: str, ok: enum(yes, no) }");
  w.define("build.step", "// One step.\n{ step: str }");
  w.define("build.step.cmd", "{ argv: [] str }");
  w.define("build.test", "{ passed: num }");
  w.define("other", "{ x: str }");
  for (std::size_t i = 0; i < ops.size(); ++i) w.write(ops[i].first, ops[i].second, stamp(static_cast<std::int64_t>(i)));
}

std::vector<Op> base_ops() {
  return {
      {"build", build_value()},
      {"build.step", build_value({{"step", "compile"}})},
      {"other", Value::dict({{"x", "1"}})},
      {"build.step.cmd", build_value({{"step", "compile"}, {"argv", Value::list({"cc", "-c"})}})},
      {"build.test", build_value({{"passed", 10}})},
      {"build.step", build_value({{"step", "link"}})},
      {"build", build_value({{"user", "bo"}})},
  };
}

std::vector<trak::Event> all_events(const trak::Reader& r) {
  std::vector<trak::Event> out;
  r.events([&](const trak::Event& e) {
    out.push_back(e);
    return true;
  });
  return out;
}

using Key = std::pair<std::string, std::int64_t>;

std::vector<Key> family_rows(const trak::Family& f, std::int64_t a, std::int64_t b) {
  std::vector<Key> out;
  f.range(a, b, [&](const trak::Event& e) {
    out.emplace_back(e.type(), e.n());
    return true;
  });
  return out;
}

std::vector<Key> family_all(const trak::Family& f) { return f.count() ? family_rows(f, 1, f.count()) : std::vector<Key>{}; }

class Family : public ::testing::Test {
 protected:
  void SetUp() override {
    dir_ = fs::temp_directory_path() /
           ("trak-family-" + std::string(::testing::UnitTest::GetInstance()->current_test_info()->name()));
    fs::remove_all(dir_);
    fs::create_directories(dir_);
    fam_ = path("f.trak");
    make(fam_, base_ops());
  }
  void TearDown() override { fs::remove_all(dir_); }
  std::string path(const char* name) const { return (dir_ / name).string(); }
  fs::path dir_;
  std::string fam_;
};

TEST_F(Family, ChildSchemas) {
  auto r = trak::Reader::open(fam_);
  const auto& step = r.schema("build.step");
  EXPECT_EQ(step.parent, "build");
  EXPECT_EQ(step.lineage, (std::vector<std::string>{"build", "build.step"}));
  EXPECT_EQ(step.version, "1.1");
  EXPECT_EQ(r.schema("build").version, "1.0");
  EXPECT_EQ(r.schema("build").parent, "");
  EXPECT_EQ(r.schema("build").lineage, (std::vector<std::string>{"build"}));
  EXPECT_EQ(step.description, "One step.");
  auto paths = [](const trak::Schema& s) {
    std::vector<std::string> out;
    for (const auto& f : s.fields) out.push_back(f.path);
    return out;
  };
  EXPECT_EQ(paths(step), (std::vector<std::string>{"user", "ok", "step"}));
  EXPECT_EQ(paths(r.schema("build.step.cmd")), (std::vector<std::string>{"user", "ok", "step", "argv.[]"}));
  EXPECT_EQ(r.schema("build.step.cmd").lineage, (std::vector<std::string>{"build", "build.step", "build.step.cmd"}));
  EXPECT_EQ(step.text, "// One step.\n{ step: str }\n");
}

TEST_F(Family, Numbers) {
  auto r = trak::Reader::open(fam_);
  const auto events = all_events(r);
  std::vector<std::tuple<std::string, std::int64_t, std::int64_t>> got;
  for (const auto& e : events) got.emplace_back(e.type(), e.family_number(), e.family_number(e.type()));
  const std::vector<std::tuple<std::string, std::int64_t, std::int64_t>> want{
      {"build", 1, 1},          {"build.step", 2, 1}, {"other", 1, 1}, {"build.step.cmd", 3, 1},
      {"build.test", 4, 1},     {"build.step", 5, 3}, {"build", 6, 6},
  };
  EXPECT_EQ(got, want);
  const auto& cmd = events[3];
  EXPECT_EQ(cmd.family_number("build.step"), 2);
  EXPECT_THROW(cmd.family_number("build.test"), std::out_of_range);
  // Extensions as written: one per ancestor, plus its own once a child exists.
  std::vector<std::uint64_t> counts;
  for (const auto& v : event_vlqs(read_file(fam_))) counts.push_back(v[5]);
  EXPECT_EQ(counts, (std::vector<std::uint64_t>{1, 2, 0, 2, 1, 2, 1}));
}

TEST_F(Family, Table) {
  auto r = trak::Reader::open(fam_);
  auto f = r.family("build");
  EXPECT_EQ(f.types()[0]->name, "build");
  EXPECT_EQ(f.name(), "build");
  EXPECT_EQ(f.count(), 6);
  EXPECT_EQ(family_all(f), (std::vector<Key>{{"build", 1},
                                             {"build.step", 1},
                                             {"build.step.cmd", 1},
                                             {"build.test", 1},
                                             {"build.step", 2},
                                             {"build", 2}}));
  EXPECT_EQ(family_rows(f, 5, 2),
            (std::vector<Key>{{"build.step", 2}, {"build.test", 1}, {"build.step.cmd", 1}, {"build.step", 1}}));
  EXPECT_EQ(f.get(4).type(), "build.test");
  auto latest = f.latest();
  ASSERT_TRUE(latest);
  EXPECT_EQ(Key(latest->type(), latest->n()), Key("build", 2));
  EXPECT_THROW(f.get(0), std::out_of_range);
  EXPECT_THROW(f.get(7), std::out_of_range);
  EXPECT_THROW(family_rows(f, 1, 7), std::out_of_range);
  // The callback can stop a range early.
  int seen = 0;
  f.range(1, 6, [&](const trak::Event&) { return ++seen < 2; });
  EXPECT_EQ(seen, 2);
  EXPECT_EQ(family_all(r.family("build.step")),
            (std::vector<Key>{{"build.step", 1}, {"build.step.cmd", 1}, {"build.step", 2}}));
  EXPECT_EQ(r.family("other").count(), 1);
  EXPECT_EQ(r.family("build.test").count(), 1);
  EXPECT_THROW(r.family("nope"), std::out_of_range);
  auto at = f.at_time(stamp(3));
  ASSERT_TRUE(at);
  EXPECT_EQ(at->type(), "build.step.cmd");
  EXPECT_EQ(at->family_number(), 3);
  EXPECT_FALSE(f.at_time(stamp(99)));
}

TEST_F(Family, Empty) {
  const auto p = path("e.trak");
  {
    auto w = trak::Writer::create(p);
    w.define("a", "{}");
    w.define("a.b", "{}");
  }
  auto r = trak::Reader::open(p);
  auto f = r.family("a");
  EXPECT_EQ(f.count(), 0);
  EXPECT_FALSE(f.latest());
  EXPECT_TRUE(family_all(f).empty());
  EXPECT_FALSE(f.at_time(kBase));
}

TEST_F(Family, WriterRules) {
  const auto p = path("w.trak");
  {
    auto w = trak::Writer::create(p);
    try {
      w.define("a.b", "{}");
      ADD_FAILURE() << "defined a child before its parent";
    } catch (const trak::SchemaError& e) {
      EXPECT_NE(std::string(e.what()).find("before its child"), std::string::npos) << e.what();
    }
    w.define("a", "{ x: str }");
    try {
      w.define("a.b", "{\n  y: str,\n  x: str,\n}");
      ADD_FAILURE() << "repeated a parent's key";
    } catch (const trak::SchemaError& e) {
      EXPECT_NE(std::string(e.what()).find("already defined by 'a'"), std::string::npos) << e.what();
      EXPECT_EQ(e.line(), 3);
      EXPECT_EQ(e.column(), 3);
    }
    w.define("a.b", "{ y: str }");
    w.write("a", Value::dict({{"x", "1"}}), trak::parse_timestamp("20261107T140000.005Z"));
    try {
      w.write("a.b", Value::dict({{"x", "2"}, {"y", "3"}}), trak::parse_timestamp("20261107T140000.004Z"));
      ADD_FAILURE() << "timestamp went backwards";
    } catch (const trak::ValueError& e) {
      EXPECT_NE(std::string(e.what()).find("family a"), std::string::npos) << e.what();
    }
    try {
      w.write("a.b", Value::dict({{"y", "3"}}), trak::parse_timestamp("20261107T140000.006Z"));
      ADD_FAILURE() << "wrote without an inherited field";
    } catch (const trak::ValueError& e) {
      EXPECT_NE(std::string(e.what()).find("required"), std::string::npos) << e.what();
    }
    // The default timestamp never goes back within the family.
    w.write("a.b", Value::dict({{"x", "2"}, {"y", "3"}}), trak::parse_timestamp("20991107T140000.000Z"));
    w.write("a", Value::dict({{"x", "4"}}));
  }
  auto r = trak::Reader::open(p);
  EXPECT_EQ(all_events(r).back().timestamp(), "20991107T140000.000Z");
}

TEST_F(Family, DottedNameIn10IsPlain) {
  // A 1.0 schema's dot is just a character: no parent, no inherited fields.
  const auto p = path("d.trak");
  std::string data = "trak 1.1\n{ x: str }\n##[ a 1 1.0 ]##";
  for (std::uint64_t v : {11u, 0u, 0u}) trak::append_vlq(data, v);
  data += "\n{ y: str }\n##[ a.b 2 1.0 ]##";
  for (std::uint64_t v : {11u, 1u, 0u}) trak::append_vlq(data, v);
  data += "\n";
  write_file(p, data);
  auto r = trak::Reader::open(p);
  const auto& s = r.schema("a.b");
  EXPECT_EQ(s.parent, "");
  EXPECT_EQ(s.lineage, (std::vector<std::string>{"a.b"}));
  ASSERT_EQ(s.fields.size(), 1u);
  EXPECT_EQ(s.fields[0].path, "y");
  EXPECT_EQ(trak::check(p), 2u);
}

TEST_F(Family, LateChildNumbers) {
  const auto p = path("late.trak");
  {
    auto w = trak::Writer::create(p);
    w.define("job", "{ i: num }");
    for (int i = 0; i < 5; ++i) w.write("job", Value::dict({{"i", i}}));
    w.define("job.retry", "{}");
    w.write("job", Value::dict({{"i", 5}}));
    w.write("job.retry", Value::dict({{"i", 6}}));
  }
  std::vector<std::uint64_t> counts;
  for (const auto& v : event_vlqs(read_file(p))) counts.push_back(v[5]);
  EXPECT_EQ(counts, (std::vector<std::uint64_t>{0, 0, 0, 0, 0, 1, 1}));
  {
    auto r = trak::Reader::open(p);
    std::vector<std::int64_t> numbers;
    for (const auto& e : all_events(r)) numbers.push_back(e.family_number());
    EXPECT_EQ(numbers, (std::vector<std::int64_t>{1, 2, 3, 4, 5, 6, 7}));
    EXPECT_EQ(r.family("job").get(7).type(), "job.retry");
  }
  EXPECT_EQ(trak::check(p), 9u);
}

TEST_F(Family, AppendRestoresFamilyState) {
  std::mt19937 rng(3);
  const std::vector<std::string> names{"build", "build.step", "build.step.cmd", "build.test", "other"};
  std::vector<Op> ops;
  for (int i = 0; i < 300; ++i) {
    const std::string& name = names[rng() % names.size()];
    Value v;
    if (name == "other") {
      v = Value::dict({{"x", "1"}});
    } else if (name == "build.step") {
      v = build_value({{"step", "s"}});
    } else if (name == "build.step.cmd") {
      v = build_value({{"step", "s"}, {"argv", Value(Value::List{})}});
    } else if (name == "build.test") {
      v = build_value({{"passed", 1}});
    } else {
      v = build_value();
    }
    ops.emplace_back(name, v);
  }
  const auto whole = path("whole.trak"), parts = path("parts.trak");
  make(whole, ops);
  make(parts, std::vector<Op>(ops.begin(), ops.begin() + 100));
  for (auto [lo, hi] : std::vector<std::pair<std::size_t, std::size_t>>{{100, 101}, {101, 250}, {250, 300}}) {
    auto w = trak::Writer::open_append(parts);
    for (std::size_t i = lo; i < hi; ++i) w.write(ops[i].first, ops[i].second, stamp(static_cast<std::int64_t>(i)));
  }
  EXPECT_EQ(read_file(parts), read_file(whole));
  auto w = trak::Writer::open_append(parts);
  try {
    w.write("build.test", build_value({{"passed", 1}}), stamp(0));
    ADD_FAILURE() << "timestamp went backwards";
  } catch (const trak::ValueError& e) {
    EXPECT_NE(std::string(e.what()).find("family build"), std::string::npos) << e.what();
  }
}

TEST_F(Family, GetMatchesScan) {
  std::mt19937 rng(9);
  const auto p = path("wide.trak");
  {
    auto w = trak::Writer::create(p);
    w.define("r", "{ i: num }");
    std::vector<std::string> names{"r"};
    for (int i = 0; i < 9; ++i) {
      names.push_back("r.k" + std::to_string(i));
      w.define(names.back(), "{}");
    }
    w.define("r.k0.g", "{}");
    names.push_back("r.k0.g");
    for (int i = 0; i < 3000; ++i) w.write(names[rng() % names.size()], Value::dict({{"i", i}}), stamp(i / 3));
  }
  {
    auto r = trak::Reader::open(p);
    const auto events = all_events(r);
    for (const char* name : {"r", "r.k0"}) {
      auto f = r.family(name);
      std::vector<const trak::Event*> members;
      for (const auto& e : events) {
        const auto& lin = e.schema().lineage;
        if (std::find(lin.begin(), lin.end(), name) != lin.end()) members.push_back(&e);
      }
      const auto size = static_cast<std::int64_t>(members.size());
      ASSERT_EQ(f.count(), size) << name;
      std::vector<std::int64_t> ms{1, 2, size};
      for (int i = 0; i < 100; ++i) ms.push_back(1 + static_cast<std::int64_t>(rng() % members.size()));
      for (auto m : ms) {
        const auto e = f.get(m);
        EXPECT_EQ(Key(e.type(), e.n()), Key(members[static_cast<std::size_t>(m - 1)]->type(), members[static_cast<std::size_t>(m - 1)]->n()));
      }
      const std::int64_t a = 17, b = std::min<std::int64_t>(400, size);
      std::vector<std::int64_t> want, up, down;
      for (std::int64_t i = a; i <= b; ++i) want.push_back(members[static_cast<std::size_t>(i - 1)]->offset());
      f.range(a, b, [&](const trak::Event& e) {
        up.push_back(e.offset());
        return true;
      });
      f.range(b, a, [&](const trak::Event& e) {
        down.push_back(e.offset());
        return true;
      });
      EXPECT_EQ(up, want);
      std::reverse(want.begin(), want.end());
      EXPECT_EQ(down, want);
      for (std::int64_t i : {0, 1, 500, 999}) {
        const auto at = f.at_time(stamp(i));
        const trak::Event* expect = nullptr;
        for (const auto* e : members) {
          if (e->millis() >= stamp(i)) {
            expect = e;
            break;
          }
        }
        ASSERT_EQ(at.has_value(), expect != nullptr);
        if (at) {
          EXPECT_EQ(at->offset(), expect->offset());
        }
      }
    }
  }
  EXPECT_EQ(trak::check(p), 3011u);
}

TEST_F(Family, CheckRejectsBadFamilies) {
  std::string data = read_file(fam_);
  const auto at = data.find("==[ build.step 1 ");
  ASSERT_NE(at, std::string::npos);
  const auto marker = data.find("]==", at) + 3;
  const auto eol = data.find('\n', marker);
  auto vlqs = trak::decode_vlqs(data.substr(marker, eol - marker));
  // E = 2: number 2 in build, 1 in build.step.
  ASSERT_EQ(std::vector<std::uint64_t>(vlqs.begin() + 5, vlqs.begin() + 8), (std::vector<std::uint64_t>{2, 2, 1}));
  vlqs[6] = 9;
  std::string letters;
  for (auto v : vlqs) trak::append_vlq(letters, v);
  data.replace(marker, eol - marker, letters);
  write_file(fam_, data);
  try {
    trak::check(fam_);
    ADD_FAILURE() << "check passed";
  } catch (const trak::FormatError& e) {
    EXPECT_NE(std::string(e.what()).find("family numbers"), std::string::npos) << e.what();
  }
}

TEST_F(Family, Prev) {
  auto r = trak::Reader::open(fam_);
  auto t = r.table("build.step");
  auto e = t.latest();
  ASSERT_TRUE(e);
  EXPECT_EQ(e->n(), 2);
  const auto before = r.footer_reads();
  auto p = e->prev();
  EXPECT_EQ(r.footer_reads() - before, 1);
  ASSERT_TRUE(p);
  EXPECT_EQ(p->n(), 1);
  EXPECT_EQ(p->get("step"), "compile");
  EXPECT_FALSE(p->prev());
}

TEST_F(Family, SeekAndAtTime) {
  const auto p = path("s.trak");
  {
    auto w = trak::Writer::create(p);
    w.define("t", "{ i: num }");
    for (int i = 0; i < 1000; ++i) w.write("t", Value::dict({{"i", i}}), stamp(i / 4 * 4));
  }
  auto r = trak::Reader::open(p);
  auto t = r.table("t");
  for (int i = 0; i <= 1000; i += 7) {
    const auto e = t.at_time(stamp(i));
    const int want = (i + 3) / 4 * 4;
    if (want >= 1000) {
      EXPECT_FALSE(e) << i;
    } else {
      ASSERT_TRUE(e) << i;
      EXPECT_EQ(e->n(), want + 1);
      EXPECT_EQ(e->millis(), stamp(want));
    }
  }
}

}  // namespace
