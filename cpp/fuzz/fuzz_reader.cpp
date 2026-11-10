// libFuzzer target: open arbitrary bytes, check them and walk every event.
// It must never crash, hang or trip a sanitizer.
#include <cstdint>
#include <cstdio>
#include <exception>
#include <optional>
#include <string>

#include "trak/trak.hpp"

extern "C" int LLVMFuzzerTestOneInput(const std::uint8_t* data, std::size_t size) {
  static const std::string path = [] {
    char name[64];
    std::snprintf(name, sizeof name, "/tmp/trak-fuzz-%ld.trak", static_cast<long>(std::rand()));
    return std::string(name);
  }();
  if (std::FILE* f = std::fopen(path.c_str(), "wb")) {
    std::fwrite(data, 1, size, f);
    std::fclose(f);
  } else {
    return 0;
  }
  try {
    trak::check(path);
  } catch (const std::exception&) {
  }
  try {
    auto r = trak::Reader::open(path);
    for (const auto& s : r.types()) {
      auto t = r.table(s.name);
      const auto count = t.count();
      for (std::int64_t n = 1; n <= count && n < 50; ++n) {
        try {
          auto e = t.get(n);
          e.native();
          e.leaves();
        } catch (const std::exception&) {
        }
      }
      try {
        auto f = r.family(s.name);
        const auto rows = f.count();
        if (rows > 0) {
          f.get(1);
          f.latest();
          f.at_time(0);
          int seen = 0;
          f.range(rows, 1, [&](const trak::Event& e) {
            e.family_number();
            if (auto p = e.prev()) p->n();
            return ++seen < 50;
          });
        }
      } catch (const std::exception&) {
      }
    }
    try {
      std::optional<std::int64_t> cursor;
      for (int i = 0; i < 20; ++i) {
        auto page = r.after(cursor, 7);
        if (page.empty()) break;
        cursor = page.back().offset();
      }
      r.before(cursor, 7);
    } catch (const std::exception&) {
    }
    r.events([](const trak::Event& e) {
      try {
        e.native();
      } catch (const std::exception&) {
      }
      return true;
    });
  } catch (const std::exception&) {
  }
  return 0;
}
