#include <gtest/gtest.h>

#include <limits>
#include <random>

#include "trak/trak.hpp"

TEST(Vlq, SpecTable) {
  const std::pair<std::uint64_t, const char*> table[] = {{0, "A"},     {25, "Z"},     {26, "bA"},
                                                         {148, "fS"}, {1520, "cgM"}, {52310, "czjY"}};
  for (const auto& [v, s] : table) {
    std::string out;
    trak::append_vlq(out, v);
    EXPECT_EQ(out, s);
    EXPECT_EQ(trak::decode_vlqs(s), std::vector<std::uint64_t>{v});
  }
}

TEST(Vlq, RoundTripRange) {
  std::string buf;
  for (std::uint64_t v = 0; v < 10'000'000; ++v) {
    buf.clear();
    trak::append_vlq(buf, v);
    const auto got = trak::decode_vlqs(buf);
    ASSERT_EQ(got.size(), 1u);
    ASSERT_EQ(got[0], v);
  }
}

TEST(Vlq, RoundTripRandom64) {
  std::mt19937_64 rng(1);
  std::vector<std::uint64_t> values;
  std::string buf;
  for (int i = 0; i < 20000; ++i) {
    values.push_back(rng() >> (rng() % 64));
    trak::append_vlq(buf, values.back());
  }
  values.push_back(std::numeric_limits<std::uint64_t>::max());
  trak::append_vlq(buf, values.back());
  EXPECT_EQ(trak::decode_vlqs(buf), values);
}

TEST(Vlq, Errors) {
  const std::pair<std::string, std::string> cases[] = {
      {"aB", "leading zero"}, {"bAaB", "leading zero"}, {"b", "final capital"},
      {"A1", "bad VLQ letter"}, {std::string(14, 'p') + "A", "overflows"}};
  for (const auto& [s, msg] : cases) {
    try {
      trak::decode_vlqs(s, 100);
      ADD_FAILURE() << s;
    } catch (const trak::FormatError& e) {
      EXPECT_NE(e.message().find(msg), std::string::npos) << e.what();
      EXPECT_GE(e.offset(), 100);
    }
  }
}

TEST(Jump, MatchesRecurrence) {
  const std::size_t limit = 1'000'000;
  std::vector<std::uint64_t> j(limit + 1, 0);
  for (std::size_t n = 1; n <= limit; ++n) {
    const std::uint64_t p = n - 1, jp = j[p];
    j[n] = (p > jp && p - jp == jp - j[jp]) ? j[jp] : p;
    ASSERT_EQ(trak::jump(n), j[n]) << n;
  }
  EXPECT_EQ(trak::jump(std::numeric_limits<std::uint64_t>::max()), 0u);
  EXPECT_EQ(trak::jump(std::uint64_t{1} << 40), (std::uint64_t{1} << 40) - 1);
}

TEST(Num, Grammar) {
  for (const char* ok : {"0", "-0", "12", "1.5", "-1.5E-3", "2e+0", "10.000", "1e10"}) EXPECT_TRUE(trak::valid_num(ok)) << ok;
  for (const char* bad : {"", "-", "01", "1.", ".5", "1e", "1e+", "+1", "1.5.5", "0x1", " 1"}) {
    EXPECT_FALSE(trak::valid_num(bad)) << bad;
  }
}

TEST(Timestamp, RoundTrip) {
  EXPECT_EQ(trak::format_timestamp(0), "19700101T000000.000Z");
  EXPECT_EQ(trak::parse_timestamp("19700101T000001.002Z"), 1002);
  EXPECT_EQ(trak::format_timestamp(trak::parse_timestamp("20261107T140322.415Z")), "20261107T140322.415Z");
  EXPECT_EQ(trak::format_timestamp(-1), "19691231T235959.999Z");
  EXPECT_EQ(trak::format_timestamp(trak::parse_timestamp("20000229T235959.999Z")), "20000229T235959.999Z");
  for (const char* bad : {"20261332T000000.000Z", "20230229T000000.000Z", "00000101T000000.000Z",
                          "20260101T240000.000Z", "20260101T000060.000Z", "2026-01-01", "20260101T000000.000"}) {
    EXPECT_THROW(trak::parse_timestamp(bad), trak::ValueError) << bad;
  }
}
