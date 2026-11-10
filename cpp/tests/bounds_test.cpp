// Navigation read-count bounds (handover §9) on a 1M-event file. n in
// 3·log2(n) + 4 is the number of events in the table.
#include <gtest/gtest.h>

#include <cmath>
#include <filesystem>
#include <optional>
#include <random>

#include "trak/trak.hpp"

TEST(Bounds, Navigation) {
  constexpr int k = 8;
  constexpr int n = 1'000'000;
  const auto p = (std::filesystem::temp_directory_path() / "trak-bounds.trak").string();
  std::mt19937 rng(7);
  std::uniform_real_distribution<double> unit(0, 1);
  {
    auto w = trak::Writer::create(p, {true, false});
    for (int i = 0; i < k; ++i) w.define("t" + std::to_string(i), "{ i: num }");
    for (int i = 0; i < n; ++i) {
      int t = 0;
      if (unit(rng) >= 0.5) t = unit(rng) < 0.001 ? k - 1 : 1 + static_cast<int>(rng() % (k - 2));
      w.write("t" + std::to_string(t), trak::Value::dict({{"i", i}}));
    }
  }
  auto r = trak::Reader::open(p);
  EXPECT_LE(r.footer_reads(), 2 * k + 4);
  auto reads = [&](auto fn) {
    const auto before = r.footer_reads();
    fn();
    return r.footer_reads() - before;
  };
  std::int64_t total = 0;
  for (const auto& s : r.types()) {
    auto t = r.table(s.name);
    r.clear_cache();
    EXPECT_LE(reads([&] { t.count(); }), 2 * k + 4) << s.name;
    r.clear_cache();
    EXPECT_LE(reads([&] { t.latest(); }), 2 * k + 4) << s.name;
    const std::int64_t count = t.count();
    total += count;
    const auto bound = static_cast<std::int64_t>(3 * std::log2(static_cast<double>(count)) + 4);
    std::vector<std::int64_t> ms{1, 2, count / 2, count - 1, count};
    for (int i = 0; i < 200; ++i) ms.push_back(1 + static_cast<std::int64_t>(rng() % static_cast<std::uint64_t>(count)));
    for (auto m : ms) {
      if (m < 1 || m > count) continue;
      r.clear_cache();
      t.count();
      EXPECT_LE(reads([&] { t.get(m); }), bound) << s.name << " " << m;
    }
  }
  EXPECT_EQ(total, n);
  // Seeks read at most two footers per step of the walk to their node.
  {
    auto t = r.table("t0");
    const std::int64_t count = t.count();
    const auto bound = static_cast<std::int64_t>(2 * (3 * std::log2(static_cast<double>(count)) + 4));
    for (const char* ts : {"20000101T000000.000Z", "20991231T000000.000Z"}) {
      r.clear_cache();
      t.count();
      EXPECT_LE(reads([&] { t.at_time(trak::parse_timestamp(ts)); }), bound) << ts;
    }
    // A childless type's family number is n, so a family row is a seek too.
    auto f = r.family("t0");
    for (int i = 0; i < 50; ++i) {
      const auto m = 1 + static_cast<std::int64_t>(rng() % static_cast<std::uint64_t>(count));
      const auto target = t.get(m);
      r.clear_cache();
      t.count();
      std::optional<trak::Event> found;
      EXPECT_LE(reads([&] { found = t.at_time(target.millis()); }), bound) << m;
      ASSERT_TRUE(found);
      EXPECT_EQ(found->millis(), target.millis());
      EXPECT_LE(found->n(), m);
      r.clear_cache();
      t.count();
      EXPECT_LE(reads([&] { found = f.get(m); }), bound) << m;
      EXPECT_EQ(found->offset(), target.offset());
    }
  }
  r.close();
  std::filesystem::remove(p);
}

// Family row m is a seek in each of the family's k types: O(k log n).
TEST(Bounds, FamilyRows) {
  constexpr int n = 250'000;
  constexpr int kids = 5;
  const auto p = (std::filesystem::temp_directory_path() / "trak-bounds-family.trak").string();
  std::mt19937 rng(8);
  {
    auto w = trak::Writer::create(p, {true, false});
    w.define("root", "{ i: num }");
    for (int k = 0; k < kids; ++k) w.define("root.k" + std::to_string(k), "{}");
    w.define("other", "{ i: num }");
    const auto ts = trak::parse_timestamp("20261107T000000.000Z");
    for (int i = 0; i < n; ++i) {
      const auto pick = static_cast<int>(rng() % (kids + 2));
      const std::string name = pick == kids ? "root" : pick == kids + 1 ? "other" : "root.k" + std::to_string(pick);
      w.write(name, trak::Value::dict({{"i", i}}), ts);
    }
  }
  auto r = trak::Reader::open(p);
  auto f = r.family("root");
  const std::int64_t rows = f.count();
  const auto k = static_cast<double>(f.types().size());
  ASSERT_EQ(f.types().size(), static_cast<std::size_t>(kids + 1));
  // A seek reads at most two footers per step of the walk to that node.
  const auto bound = static_cast<std::int64_t>(k * (2 * (3 * std::log2(static_cast<double>(rows)) + 4)));
  std::vector<std::int64_t> ms{1, rows};
  for (int i = 0; i < 100; ++i) ms.push_back(1 + static_cast<std::int64_t>(rng() % static_cast<std::uint64_t>(rows)));
  for (auto m : ms) {
    r.clear_cache();
    f.count();
    const auto before = r.footer_reads();
    const auto e = f.get(m);
    EXPECT_LE(r.footer_reads() - before, bound) << m;
    EXPECT_EQ(e.family_number(), m);
  }
  r.close();
  std::filesystem::remove(p);
}
