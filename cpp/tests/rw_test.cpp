#include <gtest/gtest.h>

#include <cmath>
#include <filesystem>
#include <fstream>
#include <sstream>

#include "trak/trak.hpp"

namespace fs = std::filesystem;

namespace {

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

std::string spec_example() {
  std::string s = read_file(fs::path(TRAK_REPO_DIR) / "SPEC.md");
  s = s.substr(s.find("## 13. Example"));
  const auto start = s.find("```\n") + 4;
  return s.substr(start, s.find("```", start) - start);
}

const char* kBuild =
    "// One CI build run.\n{\n  user: str,                  // who triggered the build\n"
    "  status: enum(ok, failed),\n  duration: num @ms,          // wall-clock time\n  tags: [] str,\n"
    "  stdout: |str @log,          // compiler output\n  error: str?,                // set when the build failed\n}";
const char* kDeploy = "// A release pushed to an environment.\n{\n  target: str,\n  version: str @semver,\n}\n";

class TempDir : public ::testing::Test {
 protected:
  void SetUp() override {
    dir_ = fs::temp_directory_path() /
           ("trak-test-" + std::string(::testing::UnitTest::GetInstance()->current_test_info()->name()));
    fs::remove_all(dir_);
    fs::create_directories(dir_);
  }
  void TearDown() override { fs::remove_all(dir_); }
  std::string path(const char* name) const { return (dir_ / name).string(); }
  fs::path dir_;
};

// A typed convenience: an aggregate converted through ValueTraits.
struct Build {
  std::string user;
  std::string status;
  int duration;
  std::vector<std::string> tags;
  std::string stdout_text;
  std::optional<std::string> error;
};

}  // namespace

template <>
struct trak::ValueTraits<Build> {
  static Value to_value(const Build& b) {
    return Value::dict({{"user", b.user},
                        {"status", b.status},
                        {"duration", b.duration},
                        {"tags", trak::to_value(b.tags)},
                        {"stdout", b.stdout_text},
                        {"error", trak::to_value(b.error)}});
  }
};

namespace {

void write_example(const std::string& p) {
  auto w = trak::Writer::create(p);
  w.define("build", kBuild);
  w.define("deploy", kDeploy);
  w.write("build",
          trak::Value::dict({{"user", "ana"},
                             {"status", "ok"},
                             {"duration", 910},
                             {"tags", trak::Value::list({"ci"})},
                             {"stdout", "Compiling core...\nDone."}}),
          trak::parse_timestamp("20261107T140210.120Z"));
  w.write("build",
          trak::to_value(Build{"joel", "failed", 1520, {"ci", "nightly"}, "Compiling core...\nld: undefined symbol main",
                               "link failed"}),
          trak::parse_timestamp("20261107T140322.415Z"));
}

using RW = TempDir;

TEST_F(RW, WriterReproducesSpecExample) {
  write_example(path("x.trak"));
  EXPECT_EQ(read_file(path("x.trak")), spec_example());
}

TEST_F(RW, ReaderOnSpecExample) {
  write_file(path("x.trak"), spec_example());
  auto r = trak::Reader::open(path("x.trak"));
  EXPECT_EQ(r.version(), "1.1");
  ASSERT_EQ(r.types().size(), 2u);
  EXPECT_EQ(r.schema("deploy").description, "A release pushed to an environment.");
  auto builds = r.table("build");
  EXPECT_EQ(builds.count(), 2);
  auto e = builds.get(1);
  EXPECT_EQ(e.type(), "build");
  EXPECT_EQ(e.offset(), 423);
  EXPECT_EQ(e.timestamp(), "20261107T140210.120Z");
  EXPECT_EQ(e.get("user"), "ana");
  EXPECT_EQ(e.as_int("duration"), 910);
  EXPECT_EQ(e.as_float("duration"), 910.0);
  EXPECT_FALSE(e.get("error"));
  EXPECT_FALSE(e.as_int("error"));
  EXPECT_EQ(e.raw("stdout"), "Compiling core...\nDone.");
  auto latest = builds.latest();
  ASSERT_TRUE(latest);
  const auto leaves = latest->leaves();
  ASSERT_EQ(leaves.size(), 7u);
  EXPECT_EQ(leaves[4].path, "tags.1");
  EXPECT_EQ(leaves[4].value, "nightly");
  EXPECT_EQ(r.table("deploy").count(), 0);
  EXPECT_FALSE(r.table("deploy").latest());
  EXPECT_THROW(r.table("nope"), std::out_of_range);
  EXPECT_THROW(builds.get(3), std::out_of_range);
  EXPECT_EQ(trak::check(path("x.trak")), 4u);
}

TEST_F(RW, Navigation) {
  const auto p = path("many.trak");
  {
    auto w = trak::Writer::create(p);
    w.define("a", "{ i: num }");
    w.define("b", "{ s: str }");
    w.define("c", "{ }");
    for (int i = 1; i <= 2000; ++i) {
      const trak::Millis ts = 1'000'000 + std::int64_t{i} * 10;
      w.write("a", trak::Value::dict({{"i", i}}), ts);
      if (i % 3 == 0) w.write("b", trak::Value::dict({{"s", "s" + std::to_string(i)}}), ts);
    }
  }
  auto r = trak::Reader::open(p);
  auto a = r.table("a");
  for (std::int64_t n = 1; n <= 2000; ++n) ASSERT_EQ(a.get(n).as_int("i"), n);
  for (std::int64_t n : {1, 2, 777, 1999, 2000}) {
    r.clear_cache();
    const auto before = r.footer_reads();
    a.get(n);
    EXPECT_LE(r.footer_reads() - before, 3 * 11 + 4) << n;
  }
  std::vector<std::int64_t> got;
  a.range(5, 600, [&](const trak::Event& e) {
    got.push_back(e.n());
    return true;
  });
  ASSERT_EQ(got.size(), 596u);
  EXPECT_EQ(got.front(), 5);
  EXPECT_EQ(got.back(), 600);
  got.clear();
  a.range(600, 590, [&](const trak::Event& e) {
    got.push_back(e.n());
    return got.size() < 5;
  });
  EXPECT_EQ(got, (std::vector<std::int64_t>{600, 599, 598, 597, 596}));
  EXPECT_THROW(a.range(0, 3, [](const trak::Event&) { return true; }), std::out_of_range);
  EXPECT_EQ(a.at_time(1'000'000 + 105)->n(), 11);
  EXPECT_FALSE(a.at_time(1'000'000 + 20'011));
  EXPECT_EQ(r.table("b").count(), 666);
  EXPECT_EQ(trak::check(p), 3u + 2000u + 666u);
}

TEST_F(RW, NumFormats) {
  const std::pair<trak::Value, const char*> cases[] = {
      {trak::Value(0), "0"},        {trak::Value(-12), "-12"},
      {trak::Value(std::uint64_t{18446744073709551615u}), "18446744073709551615"},
      {trak::Value(0.1), "0.1"},    {trak::Value(0.5), "0.5"},
      {trak::Value("1.5e3"), "1.5e3"}};
  for (const auto& [v, want] : cases) {
    const auto p = path("n.trak");
    {
      auto w = trak::Writer::create(p, {true, false});
      w.define("t", "{ x: num }");
      w.write("t", trak::Value::dict({{"x", v}}));
    }
    auto r = trak::Reader::open(p);
    EXPECT_EQ(r.table("t").get(1).get("x"), want);
  }
}

TEST_F(RW, ValueErrors) {
  auto w = trak::Writer::create(path("e.trak"));
  w.define("t", "{ x: num, e: enum(a, b)?, s: str?, l: [] str?, m: {} str?, o: {y: str}? }");
  using D = trak::Value::Dict;
  const std::pair<trak::Value, const char*> cases[] = {
      {trak::Value(D{{"x", "01"}}), "not a valid num"},
      {trak::Value(D{{"x", std::nan("")}}), "finite"},
      {trak::Value(D{{"x", trak::Value::list({1})}}), "decimal string"},
      {trak::Value(D{{"x", 1}, {"e", "z"}}), "not one of a, b"},
      {trak::Value(D{{"x", 1}, {"e", 3}}), "needs a string"},
      {trak::Value(D{{"x", 1}, {"l", "ab"}}), "expected a list"},
      {trak::Value(D{{"x", 1}, {"m", 3}}), "expected a map"},
      {trak::Value(D{{"x", 1}, {"m", trak::Value::dict({{"a", "1"}, {"a", "2"}})}}), "repeated map key"},
      {trak::Value(D{{"x", 1}, {"o", 5}}), "expected an object"},
      {trak::Value(D{{"x", 1}, {"zzz", 1}}), "unknown field 'zzz'"},
      {trak::Value(D{}), "x: value is required"},
      {trak::Value(D{{"x", 1}, {"o", trak::Value::dict({})}}), "o.y: value is required"},
  };
  for (const auto& [v, msg] : cases) {
    try {
      w.write("t", v);
      ADD_FAILURE() << msg;
    } catch (const trak::ValueError& e) {
      EXPECT_NE(std::string(e.what()).find(msg), std::string::npos) << e.what();
    }
  }
  EXPECT_EQ(w.write("t", trak::Value::dict({{"x", 1}})).n, 1);
  EXPECT_THROW(w.write("nope", trak::Value()), trak::ValueError);
}

TEST_F(RW, Timestamps) {
  const auto p = path("t.trak");
  {
    auto w = trak::Writer::create(p);
    w.define("t", "{}");
    w.write("t", trak::Value::dict({}), trak::parse_timestamp("20991231T235959.999Z"));
    w.write("t", trak::Value::dict({}));
    EXPECT_THROW(w.write("t", trak::Value::dict({}), trak::parse_timestamp("20991231T235959.998Z")), trak::ValueError);
  }
  auto r = trak::Reader::open(p);
  EXPECT_EQ(r.table("t").get(2).timestamp(), "20991231T235959.999Z");
}

TEST_F(RW, DefineErrors) {
  auto w = trak::Writer::create(path("s.trak"));
  w.define("a", "{}");
  for (const auto& [name, def] : std::vector<std::pair<std::string, std::string>>{
           {"a", "{}"}, {"trak.x", "{}"}, {"1x", "{}"}, {"b", "{ x: foo }"}}) {
    EXPECT_THROW(w.define(name, def), trak::SchemaError) << name;
  }
  EXPECT_EQ(w.types().size(), 1u);
}

TEST_F(RW, CreateAndLock) {
  const auto p = path("x.trak");
  write_file(p, "precious");
  EXPECT_THROW(trak::Writer::create(p), std::system_error);
  EXPECT_EQ(read_file(p), "precious");
  auto w = trak::Writer::create(p, {true, true});
  EXPECT_THROW(trak::Writer::open_append(p), trak::LockedError);
  trak::Reader::open(p);  // readers take no lock
  w.define("a", "{}");
  w.flush();
  w.close();
  EXPECT_THROW(w.define("b", "{}"), trak::Error);
#ifndef _WIN32
  EXPECT_EQ(fs::status(p).permissions() & fs::perms::owner_exec, fs::perms::none);
#endif
}

TEST_F(RW, AppendContinuesIdentically) {
  write_example(path("whole.trak"));
  const std::string data = read_file(path("whole.trak"));
  write_file(path("split.trak"), data.substr(0, data.find("user: joel")));
  {
    auto w = trak::Writer::open_append(path("split.trak"));
    w.write("build",
            trak::to_value(Build{"joel", "failed", 1520, {"ci", "nightly"},
                                 "Compiling core...\nld: undefined symbol main", "link failed"}),
            trak::parse_timestamp("20261107T140322.415Z"));
  }
  EXPECT_EQ(read_file(path("split.trak")), data);
}

TEST_F(RW, AppendMany) {
  const auto p = path("x.trak");
  {
    auto w = trak::Writer::create(p);
    for (const char* n : {"a", "b", "c", "d", "e"}) w.define(n, "{ i: num }");
  }
  for (int round = 0; round < 20; ++round) {
    {
      auto w = trak::Writer::open_append(p);
      for (int i = 0; i < 37; ++i) w.write(std::string(1, "abcde"[(i * 7 + round) % 5]), trak::Value::dict({{"i", i}}));
      if (round == 10) {
        w.define("late", "{}");
        w.write("late", trak::Value::dict({}));
      }
    }
    EXPECT_NO_THROW(trak::check(p));
  }
}

TEST_F(RW, TornTail) {
  const auto p = path("x.trak");
  const std::string good = spec_example();
  const std::string complete = "==[ build 3 20261107T140322.415Z ]==fQBB\n";
  for (const std::string& tail : {std::string("user: x\nstat"), std::string("==[ build 3 2026"), complete}) {
    write_file(p, good + tail);
    {
      auto r = trak::Reader::open(p);
      EXPECT_EQ(r.end(), static_cast<std::int64_t>(good.size()));
      EXPECT_EQ(r.table("build").count(), 2);
    }
    if (tail == complete) {
      EXPECT_THROW(trak::check(p), trak::FormatError);
    } else {
      EXPECT_EQ(trak::check(p), 4u);
    }
    trak::Writer::open_append(p);
    EXPECT_EQ(read_file(p), good);
  }
}

TEST_F(RW, EmptyAndMagicOnly) {
  trak::Writer::create(path("x.trak"));
  write_file(path("empty.trak"), "");
  for (const char* name : {"x.trak", "empty.trak"}) trak::Writer::open_append(path(name)).define("a", "{}");
  EXPECT_EQ(read_file(path("x.trak")), read_file(path("empty.trak")));
}

TEST_F(RW, Refresh) {
  const auto p = path("x.trak");
  auto w = trak::Writer::create(p);
  auto r = trak::Reader::open(p);
  EXPECT_FALSE(r.refresh());
  w.define("a", "{ i: num }");
  w.write("a", trak::Value::dict({{"i", 1}}));
  EXPECT_TRUE(r.refresh());
  EXPECT_EQ(r.table("a").count(), 1);
  w.write("a", trak::Value::dict({{"i", 2}}));
  r.refresh();
  EXPECT_EQ(r.table("a").get(2).get("i"), "2");
}

TEST_F(RW, BadMagic) {
  const auto p = path("x.trak");
  for (const char* data : {"", "trac 1.0\n", "trak 1.0", "trak 01.0\n"}) {
    write_file(p, data);
    EXPECT_THROW(trak::Reader::open(p), trak::FormatError) << data;
  }
  write_file(p, "trak 2.0\n");
  EXPECT_THROW(trak::Reader::open(p), trak::UnsupportedVersion);
}

TEST_F(RW, UnparseableSchema) {
  const auto p = path("x.trak");
  {
    auto w = trak::Writer::create(p);
    w.define("t", "{ a: str }");
    w.write("t", trak::Value::dict({{"a", "x"}}));
  }
  std::string data = read_file(p);
  data.replace(data.find("{ a: str }"), 10, "{ a: s?? }");
  write_file(p, data);
  {
    auto r = trak::Reader::open(p);
    auto t = r.table("t");
    EXPECT_EQ(t.count(), 1);
    EXPECT_FALSE(t.schema().parsed());
    EXPECT_THROW(t.get(1).get("a"), trak::SchemaError);
  }
  EXPECT_THROW(trak::check(p), trak::FormatError);
  auto w = trak::Writer::open_append(p);
  EXPECT_THROW(w.write("t", trak::Value::dict({{"a", "y"}})), trak::SchemaError);
}

TEST_F(RW, NativeNested) {
  const auto p = path("n.trak");
  {
    auto w = trak::Writer::create(p);
    w.define("t", "{ jobs: [] { env: {} str, log: |str? }, labels: {} [] str, empty: [] {} }");
    w.write("t", trak::Value::dict(
                     {{"jobs", trak::Value::list({trak::Value::dict({{"env", trak::Value::dict({{"z", "1"}, {"a.b", "2"}})},
                                                                     {"log", "x\n--[ log ]--"}})})},
                      {"labels", trak::Value::dict({{"none", trak::Value::list({})}, {"some", trak::Value::list({"1"})}})},
                      {"empty", trak::Value::list({trak::Value::dict({}), trak::Value::dict({})})}}));
  }
  EXPECT_NO_THROW(trak::check(p));
  auto r = trak::Reader::open(p);
  const auto e = r.table("t").get(1);
  EXPECT_EQ(e.get("jobs.0.env.a.b"), "2");
  const auto root = e.native();
  const auto& obj = std::get<trak::Node::Object>(root.data);
  const auto& labels = std::get<trak::Node::Map>(obj[1].second.data);
  ASSERT_EQ(labels.size(), 2u);
  EXPECT_FALSE(labels[0].key);
  EXPECT_EQ(labels[0].key_length, 4u);
  EXPECT_EQ(*labels[1].key, "some");
  EXPECT_EQ(std::get<trak::Node::List>(obj[2].second.data).size(), 2u);
}

}  // namespace
