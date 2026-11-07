// Errors, letter VLQs, jumps, num validation and timestamps.
#include <chrono>
#include <cstdio>
#include <limits>

#include "internal.hpp"

namespace trak {

const char* version() noexcept { return "1.0.0"; }

namespace {
std::string with_offset(const std::string& message, std::int64_t offset) {
  return offset < 0 ? message : message + " (at byte " + std::to_string(offset) + ")";
}
std::string with_position(const std::string& message, int line, int column) {
  if (line == 0) return message;
  return message + " (line " + std::to_string(line) + ", column " + std::to_string(column) + ")";
}
}  // namespace

FormatError::FormatError(const std::string& message, std::int64_t offset)
    : Error(with_offset(message, offset)), message_(message), offset_(offset) {}

UnsupportedVersion::UnsupportedVersion(const std::string& major)
    : FormatError("unsupported major version " + major, 5) {}

SchemaError::SchemaError(const std::string& message, int line, int column)
    : Error(with_position(message, line, column)), message_(message), line_(line), column_(column) {}

namespace detail {
void format_error(std::int64_t offset, const std::string& message) { throw FormatError(message, offset); }
}  // namespace detail

void append_vlq(std::string& out, std::uint64_t v) {
  char buf[14];
  int i = 13;
  buf[i] = static_cast<char>('A' + v % 26);
  v /= 26;
  while (v > 0) {
    buf[--i] = static_cast<char>('a' + v % 26);
    v /= 26;
  }
  out.append(buf + i, buf + 14);
}

std::vector<std::uint64_t> decode_vlqs(std::string_view data, std::int64_t offset) {
  std::vector<std::uint64_t> out;
  out.reserve(data.size());
  std::uint64_t v = 0;
  bool started = false;
  for (std::size_t i = 0; i < data.size(); ++i) {
    const char c = data[i];
    const std::int64_t at = offset + static_cast<std::int64_t>(i);
    std::uint64_t d;
    if (c >= 'A' && c <= 'Z') {
      d = static_cast<std::uint64_t>(c - 'A');
    } else if (c >= 'a' && c <= 'z') {
      d = static_cast<std::uint64_t>(c - 'a');
      if (!started && d == 0) detail::format_error(at, "VLQ has a leading zero digit");
    } else {
      std::string msg = "bad VLQ letter '";
      msg += c;
      detail::format_error(at, msg + "'");
    }
    if (v > (std::numeric_limits<std::uint64_t>::max() - d) / 26) detail::format_error(at, "VLQ overflows 64 bits");
    v = v * 26 + d;
    if (c <= 'Z') {
      out.push_back(v);
      v = 0;
      started = false;
    } else {
      started = true;
    }
  }
  if (started) {
    detail::format_error(offset + static_cast<std::int64_t>(data.size()), "VLQ ends without a final capital letter");
  }
  return out;
}

std::uint64_t jump(std::uint64_t n) noexcept {
  if (n == 0) return 0;
  std::uint64_t r = n;
  for (;;) {
    if (r == std::numeric_limits<std::uint64_t>::max()) return n - r;
    // Largest 2^k - 1 <= r.
    std::uint64_t t = 1;
    while (t <= (r + 1) / 2) t <<= 1;
    t -= 1;
    if (t == r) return n - t;
    r -= t;
  }
}

bool valid_num(std::string_view b) noexcept {
  std::size_t i = 0;
  const std::size_t n = b.size();
  auto digit = [&](std::size_t k) { return k < n && b[k] >= '0' && b[k] <= '9'; };
  if (i < n && b[i] == '-') ++i;
  if (i >= n) return false;
  if (b[i] == '0') {
    ++i;
  } else if (b[i] >= '1' && b[i] <= '9') {
    while (digit(i)) ++i;
  } else {
    return false;
  }
  if (i < n && b[i] == '.') {
    const std::size_t start = ++i;
    while (digit(i)) ++i;
    if (i == start) return false;
  }
  if (i < n && (b[i] == 'e' || b[i] == 'E')) {
    ++i;
    if (i < n && (b[i] == '+' || b[i] == '-')) ++i;
    const std::size_t start = i;
    while (digit(i)) ++i;
    if (i == start) return false;
  }
  return i == n;
}

// --- timestamps ---

namespace {
// Howard Hinnant's days_from_civil and civil_from_days.
std::int64_t days_from_civil(std::int64_t y, unsigned m, unsigned d) {
  y -= m <= 2;
  const std::int64_t era = (y >= 0 ? y : y - 399) / 400;
  const auto yoe = static_cast<unsigned>(y - era * 400);
  const unsigned mm = m > 2 ? m - 3 : m + 9;
  const unsigned doy = (153 * mm + 2) / 5 + d - 1;
  const unsigned doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
  return era * 146097 + static_cast<std::int64_t>(doe) - 719468;
}

void civil_from_days(std::int64_t z, std::int64_t& y, unsigned& m, unsigned& d) {
  z += 719468;
  const std::int64_t era = (z >= 0 ? z : z - 146096) / 146097;
  const auto doe = static_cast<unsigned>(z - era * 146097);
  const unsigned yoe = (doe - doe / 1460 + doe / 36524 - doe / 146096) / 365;
  y = static_cast<std::int64_t>(yoe) + era * 400;
  const unsigned doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
  const unsigned mp = (5 * doy + 2) / 153;
  d = doy - (153 * mp + 2) / 5 + 1;
  m = mp < 10 ? mp + 3 : mp - 9;
  y += m <= 2;
}

std::int64_t floor_div(std::int64_t a, std::int64_t b) { return a / b - ((a % b != 0) && ((a < 0) != (b < 0))); }
}  // namespace

std::string format_timestamp(Millis ms) {
  const std::int64_t days = floor_div(ms, 86400000);
  const std::int64_t rem = ms - days * 86400000;
  std::int64_t y;
  unsigned m, d;
  civil_from_days(days, y, m, d);
  char buf[32];
  std::snprintf(buf, sizeof buf, "%04lld%02u%02uT%02d%02d%02d.%03dZ", static_cast<long long>(y), m, d,
                static_cast<int>(rem / 3600000), static_cast<int>(rem / 60000 % 60), static_cast<int>(rem / 1000 % 60),
                static_cast<int>(rem % 1000));
  return buf;
}

namespace detail {
bool valid_stamp_syntax(std::string_view s) noexcept {
  if (s.size() != 20 || s[8] != 'T' || s[15] != '.' || s[19] != 'Z') return false;
  for (std::size_t i = 0; i < 19; ++i) {
    if (i == 8 || i == 15) continue;
    if (s[i] < '0' || s[i] > '9') return false;
  }
  return true;
}
}  // namespace detail

Millis parse_timestamp(std::string_view s) {
  auto bad = [&]() -> ValueError {
    return ValueError("bad timestamp '" + std::string(s) + "', expected YYYYMMDDTHHMMSS.sssZ");
  };
  if (!detail::valid_stamp_syntax(s)) throw bad();
  auto num = [&](std::size_t at, std::size_t len) {
    unsigned v = 0;
    for (std::size_t i = at; i < at + len; ++i) v = v * 10 + static_cast<unsigned>(s[i] - '0');
    return v;
  };
  const unsigned y = num(0, 4), mo = num(4, 2), d = num(6, 2), h = num(9, 2), mi = num(11, 2), sec = num(13, 2),
                 ms = num(16, 3);
  static const unsigned month_days[] = {31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31};
  const bool leap = (y % 4 == 0 && y % 100 != 0) || y % 400 == 0;
  if (y == 0 || mo < 1 || mo > 12 || d < 1 || d > month_days[mo - 1] + (mo == 2 && leap) || h > 23 || mi > 59 ||
      sec > 59) {
    throw bad();
  }
  return ((days_from_civil(y, mo, d) * 24 + h) * 60 + mi) * 60000 + static_cast<Millis>(sec) * 1000 + ms;
}

Millis now_millis() {
  using namespace std::chrono;
  return duration_cast<milliseconds>(system_clock::now().time_since_epoch()).count();
}

}  // namespace trak
