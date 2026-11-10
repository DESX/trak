// File-order paging with cursors, write positions and directory sync.
// Mirrors python/tests/test_cursor.py.
#include <gtest/gtest.h>

#include <filesystem>
#include <fstream>
#include <random>
#include <sstream>

#include "trak/trak.hpp"

namespace fs = std::filesystem;
using trak::Value;

namespace {

const trak::Millis kStamp = trak::parse_timestamp("20261107T140000.000Z");

using Key = std::pair<std::string, std::int64_t>;

std::string read_file(const fs::path& p) {
  std::ifstream in(p, std::ios::binary);
  std::ostringstream ss;
  ss << in.rdbuf();
  return ss.str();
}

std::vector<Key> page_all(const trak::Reader& r, bool forward, std::size_t limit) {
  std::vector<Key> out;
  std::optional<std::int64_t> cursor;
  for (;;) {
    const auto page = forward ? r.after(cursor, limit) : r.before(cursor, limit);
    if (page.empty()) return out;
    EXPECT_LE(page.size(), limit);
    for (const auto& e : page) out.emplace_back(e.type(), e.n());
    cursor = page.back().offset();
  }
}

std::vector<Key> scan(const trak::Reader& r) {
  std::vector<Key> out;
  r.events([&](const trak::Event& e) {
    out.emplace_back(e.type(), e.n());
    return true;
  });
  return out;
}

std::vector<std::int64_t> offsets(const std::vector<trak::Event>& events) {
  std::vector<std::int64_t> out;
  for (const auto& e : events) out.push_back(e.offset());
  return out;
}

std::vector<trak::Pos> mixed(const std::string& path, int n, unsigned seed = 5, int types = 4) {
  std::mt19937 rng(seed);
  std::uniform_real_distribution<double> unit(0, 1);
  std::vector<trak::Pos> pos;
  auto w = trak::Writer::create(path, {true, false});
  for (int k = 0; k < types; ++k) w.define("t" + std::to_string(k), "{ i: num }");
  for (int i = 0; i < n; ++i) {
    const int k = unit(rng) < 0.6 ? 0 : static_cast<int>(rng() % static_cast<unsigned>(types));
    pos.push_back(w.write("t" + std::to_string(k), Value::dict({{"i", i}}), kStamp));
    if (unit(rng) < 0.01) w.define("late" + std::to_string(i), "{}");
  }
  return pos;
}

class Cursor : public ::testing::Test {
 protected:
  void SetUp() override {
    dir_ = fs::temp_directory_path() /
           ("trak-cursor-" + std::string(::testing::UnitTest::GetInstance()->current_test_info()->name()));
    fs::remove_all(dir_);
    fs::create_directories(dir_);
  }
  void TearDown() override { fs::remove_all(dir_); }
  std::string path(const char* name) const { return (dir_ / name).string(); }
  fs::path dir_;
};

TEST_F(Cursor, PagesCoverEveryEventOnce) {
  const auto p = path("m.trak");
  for (int n : {0, 1, 2, 57}) {
    mixed(p, n);
    auto r = trak::Reader::open(p);
    auto everything = scan(r);
    ASSERT_EQ(everything.size(), static_cast<std::size_t>(n));
    for (std::size_t limit : {1u, 2u, 5u, 100u}) {
      EXPECT_EQ(page_all(r, true, limit), everything) << n << " " << limit;
      auto backward = everything;
      std::reverse(backward.begin(), backward.end());
      EXPECT_EQ(page_all(r, false, limit), backward) << n << " " << limit;
    }
  }
}

TEST_F(Cursor, WriteReturnsPosition) {
  const auto p = path("m.trak");
  const auto pos = mixed(p, 40);
  auto r = trak::Reader::open(p);
  std::vector<std::pair<std::int64_t, std::int64_t>> got, want;
  r.events([&](const trak::Event& e) {
    got.emplace_back(e.n(), e.offset());
    return true;
  });
  for (const auto& x : pos) want.emplace_back(x.n, x.offset);
  EXPECT_EQ(got, want);
  // A write position is a cursor.
  EXPECT_EQ(r.after(pos[9].offset, 1).at(0).offset(), pos[10].offset);
  EXPECT_EQ(r.before(pos[10].offset, 1).at(0).offset(), pos[9].offset);
}

TEST_F(Cursor, Edges) {
  const auto p = path("m.trak");
  const auto pos = mixed(p, 10);
  auto r = trak::Reader::open(p);
  EXPECT_TRUE(r.after(pos.back().offset).empty());
  EXPECT_TRUE(r.before(pos.front().offset).empty());
  EXPECT_TRUE(r.after(std::nullopt, 0).empty());
  EXPECT_EQ(offsets(r.before(r.end(), 2)), offsets(r.before(std::nullopt, 2)));
  EXPECT_EQ(r.after(std::int64_t{9}, 100).size(), 10u);  // the magic line's end
  for (std::int64_t bad : {std::int64_t{-5}, std::int64_t{3}, pos[2].offset + 1, r.end() + 1, r.size() + 100}) {
    for (bool forward : {true, false}) {
      try {
        forward ? r.after(bad) : r.before(bad);
        ADD_FAILURE() << "cursor " << bad << " accepted";
      } catch (const trak::ValueError& e) {
        EXPECT_NE(std::string(e.what()).find("cursor"), std::string::npos) << e.what();
      }
    }
  }
}

TEST_F(Cursor, ForgedFooterIsNotACursor) {
  const auto p = path("f.trak");
  const std::string forged = "x\n==[ t 1 20261107T140000.000Z ]==BBBBBAB\n";
  {
    auto w = trak::Writer::create(p);
    w.define("t", "{ out: |str }");
    w.write("t", Value::dict({{"out", forged}}), kStamp);
    w.write("t", Value::dict({{"out", "y"}}), kStamp);
  }
  const std::string data = read_file(p);
  const std::string tail = "]==BBBBBAB\n";
  const auto inside = static_cast<std::int64_t>(data.find(tail) + tail.size());
  auto r = trak::Reader::open(p);
  EXPECT_THROW(r.after(inside), trak::ValueError);
  std::vector<std::int64_t> ns;
  for (const auto& e : r.after()) ns.push_back(e.n());
  EXPECT_EQ(ns, (std::vector<std::int64_t>{1, 2}));
}

TEST_F(Cursor, SurvivesRefreshAndTornTail) {
  const auto p = path("m.trak");
  const auto pos = mixed(p, 20);
  const std::int64_t cursor = pos.back().offset;
  std::vector<trak::Pos> more;
  {
    auto r = trak::Reader::open(p);
    EXPECT_TRUE(r.after(cursor).empty());
    {
      auto w = trak::Writer::open_append(p);
      for (int i = 0; i < 3; ++i) more.push_back(w.write("t1", Value::dict({{"i", i}}), kStamp));
    }
    std::vector<std::int64_t> want;
    for (const auto& x : more) want.push_back(x.offset);
    EXPECT_TRUE(r.refresh());
    EXPECT_EQ(offsets(r.after(cursor)), want);
    {
      std::ofstream out(p, std::ios::binary | std::ios::app);
      out << "i: 9\n==[ t1 9 2026";
    }
    EXPECT_FALSE(r.refresh());
    EXPECT_EQ(offsets(r.after(cursor)), want);
  }
  // After truncation, a cursor past the new end is stale.
  fs::resize_file(p, static_cast<std::uintmax_t>(more[1].offset));
  auto r = trak::Reader::open(p);
  EXPECT_THROW(r.after(more[2].offset), trak::ValueError);
  EXPECT_EQ(offsets(r.after(cursor)), (std::vector<std::int64_t>{more[0].offset}));
}

TEST_F(Cursor, ForwardPageReadsAreBounded) {
  const auto p = path("big.trak");
  const auto pos = mixed(p, 20000, 5, 6);
  auto r = trak::Reader::open(p);
  const auto k = static_cast<std::int64_t>(r.types().size());
  for (std::size_t start : {0u, 5000u, 19990u}) {
    r.clear_cache();
    const auto before = r.footer_reads();
    const auto page = r.after(pos[start].offset, 50);
    std::vector<std::int64_t> want;
    for (std::size_t i = start + 1; i < std::min(pos.size(), start + 51); ++i) want.push_back(pos[i].offset);
    EXPECT_EQ(offsets(page), want);
    // Every type's latest node in one walk (~3K reads), a seek and a
    // navigation in each of the 6 types that have events (~6 log2 n each),
    // then the walk back over at most 6 * 50 objects, plus the schemas among
    // them.
    EXPECT_LE(r.footer_reads() - before, 3 * k + 6 * 2 * 6 * 15 + 6 * 50 + 10) << start;
  }
  r.clear_cache();
  const auto before = r.footer_reads();
  r.before(pos[10000].offset, 50);
  EXPECT_LE(r.footer_reads() - before, 50 + 5);  // plus any schemas in between
}

TEST_F(Cursor, SyncCreate) {
  // The directory fsync itself isn't observable here; this checks that a
  // synced writer works end to end.
  const auto p = path("s.trak");
  {
    auto w = trak::Writer::create(p, {false, true});
    w.define("t", "{}");
    w.write("t", Value::dict({}), kStamp);
  }
  EXPECT_EQ(trak::check(p), 2u);
  // Appending to an empty file writes the magic line and syncs too.
  std::ofstream(path("e.trak"), std::ios::binary).close();
  trak::Writer::open_append(path("e.trak"), {false, true}).close();
  EXPECT_EQ(read_file(path("e.trak")), "trak 1.1\n");
}

}  // namespace
