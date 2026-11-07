#include <gtest/gtest.h>

#include <filesystem>
#include <fstream>
#include <nlohmann/json.hpp>
#include <sstream>

#include "trak/trak.hpp"
#include "vectors.hpp"

namespace fs = std::filesystem;
using nlohmann::json;

namespace {

const fs::path kVectors = fs::path(TRAK_REPO_DIR) / "conformance";

std::string read_file(const fs::path& p) {
  std::ifstream in(p, std::ios::binary);
  std::ostringstream ss;
  ss << in.rdbuf();
  return ss.str();
}

std::vector<std::string> names(const char* sub) {
  std::vector<std::string> out;
  for (const auto& e : fs::directory_iterator(kVectors / sub)) {
    if (e.path().extension() == ".trak") out.push_back(e.path().stem().string());
  }
  std::sort(out.begin(), out.end());
  return out;
}

class Valid : public ::testing::TestWithParam<std::string> {};
TEST_P(Valid, Decodes) {
  const auto path = kVectors / "valid" / (GetParam() + ".trak");
  const json want = json::parse(read_file(kVectors / "valid" / (GetParam() + ".expect.json")));
  EXPECT_EQ(trak_vectors::decode(path.string()), want);
  EXPECT_NO_THROW(trak::check(path.string()));
}
INSTANTIATE_TEST_SUITE_P(Conformance, Valid, ::testing::ValuesIn(names("valid")),
                         [](const auto& p) { return p.param.substr(0, 3); });

class Invalid : public ::testing::TestWithParam<std::string> {};
TEST_P(Invalid, FailsCheck) {
  const auto path = kVectors / "invalid" / (GetParam() + ".trak");
  const std::string reason = read_file(kVectors / "invalid" / (GetParam() + ".reason.txt"));
  try {
    trak::check(path.string());
    ADD_FAILURE() << "check passed";
  } catch (const trak::FormatError& e) {
    const auto at = reason.find("offset: ");
    if (at != std::string::npos) {
      EXPECT_EQ(e.offset(), std::stoll(reason.substr(at + 8))) << e.what();
    }
  }
}
INSTANTIATE_TEST_SUITE_P(Conformance, Invalid, ::testing::ValuesIn(names("invalid")),
                         [](const auto& p) { return p.param.substr(0, 3); });

class Write : public ::testing::TestWithParam<std::string> {};
TEST_P(Write, ReproducesBytes) {
  const auto ops = json::parse(read_file(kVectors / "write" / (GetParam() + ".input.json")));
  const auto out = fs::temp_directory_path() / ("trak-write-" + GetParam() + ".trak");
  trak_vectors::replay(out.string(), ops);
  const std::string got = read_file(out), want = read_file(kVectors / "write" / (GetParam() + ".trak"));
  fs::remove(out);
  if (got != want) {
    std::size_t i = 0;
    while (i < got.size() && i < want.size() && got[i] == want[i]) ++i;
    FAIL() << "bytes differ at offset " << i;
  }
}
INSTANTIATE_TEST_SUITE_P(Conformance, Write, ::testing::ValuesIn(names("write")),
                         [](const auto& p) { return p.param.substr(0, 3); });

}  // namespace
