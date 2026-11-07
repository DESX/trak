// libFuzzer target: open arbitrary bytes, check them and walk every event.
// It must never crash, hang or trip a sanitizer.
#include <cstdint>
#include <cstdio>
#include <exception>
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
