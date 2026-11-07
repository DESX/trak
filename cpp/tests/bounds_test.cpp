// Navigation read-count bounds (handover §9) on a 1M-event file. n in
// 3·log2(n) + 4 is the number of events in the table.
#include <gtest/gtest.h>

#include <cmath>
#include <filesystem>
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
  r.close();
  std::filesystem::remove(p);
}
