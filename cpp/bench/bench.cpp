// Measures the C++ library on the shared 1M-event workload (see
// bench/README.md at the repository root).
//
//   trak_bench [FILE] [N]
#include <chrono>
#include <cstdio>
#include <random>
#include <string>

#include "trak/trak.hpp"

namespace {

constexpr trak::Millis kBase = 1'794'000'000'000;  // 2026-11-06

std::pair<const char*, trak::Value> event(int i) {
  switch (i % 4) {
    case 0:
      return {"build", trak::Value::dict({{"user", "user" + std::to_string(i % 100)},
                                          {"status", i % 3 == 0 ? "failed" : "ok"},
                                          {"duration", i % 5000},
                                          {"tags", i % 2 ? trak::Value::list({"ci", "nightly"}) : trak::Value::list({"ci"})}})};
    case 1:
      return {"deploy", trak::Value::dict({{"target", i % 2 ? "staging" : "prod"},
                                           {"version", "1." + std::to_string(i % 50) + ".0"}})};
    case 2:
      return {"log", trak::Value::dict({{"msg", "request " + std::to_string(i) + " served"}})};
    default:
      return {"metric", trak::Value::dict({{"name", "latency"}, {"value", i % 1000}})};
  }
}

double since(std::chrono::steady_clock::time_point start) {
  return std::chrono::duration<double>(std::chrono::steady_clock::now() - start).count();
}

}  // namespace

int main(int argc, char** argv) {
  const std::string path = argc > 1 ? argv[1] : "bench.trak";
  const int n = argc > 2 ? std::stoi(argv[2]) : 1'000'000;

  auto start = std::chrono::steady_clock::now();
  {
    auto w = trak::Writer::create(path, {true, false});
    w.define("build", "{ user: str, status: enum(ok, failed), duration: num, tags: [] str }");
    w.define("deploy", "{ target: str, version: str }");
    w.define("log", "{ msg: str }");
    w.define("metric", "{ name: str, value: num }");
    for (int i = 0; i < n; ++i) {
      auto [type, value] = event(i);
      w.write(type, value, kBase + i);
    }
  }
  std::printf("write              %8.0f events/s\n", n / since(start));

  const int opens = 1000;
  start = std::chrono::steady_clock::now();
  for (int i = 0; i < opens; ++i) trak::Reader::open(path).table("build").count();
  std::printf("open + count()     %8.1f us\n", since(start) / opens * 1e6);

  auto r = trak::Reader::open(path);
  auto t = r.table("build");
  const std::int64_t count = t.count();
  std::mt19937_64 rng(1);
  const int gets = 20000;
  start = std::chrono::steady_clock::now();
  for (int i = 0; i < gets; ++i) t.get(1 + static_cast<std::int64_t>(rng() % static_cast<std::uint64_t>(count))).get("user");
  std::printf("get(random n)      %8.1f us\n", since(start) / gets * 1e6);

  const int scans = 2000;
  start = std::chrono::steady_clock::now();
  for (int i = 0; i < scans; ++i) {
    const auto a = 1 + static_cast<std::int64_t>(rng() % static_cast<std::uint64_t>(count - 40));
    t.range(a, a + 39, [](const trak::Event& e) {
      e.leaves();
      return true;
    });
  }
  std::printf("sequential 40 rows %8.1f us\n", since(start) / scans * 1e6);
}
