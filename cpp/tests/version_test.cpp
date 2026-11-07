#include <gtest/gtest.h>

#include <string>

#include "trak/trak.hpp"

TEST(Version, Is100) { EXPECT_EQ(std::string(trak::version()), "1.0.0"); }
