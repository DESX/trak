#include <gtest/gtest.h>

#include "trak/trak.hpp"

namespace {
const char* kBuild = R"(// One CI build run.
{
  user: str,                  // who triggered the build
  status: enum(ok, failed),
  duration: num @ms,          // wall-clock time
  tags: [] str,
  stdout: |str @log,          // compiler output
  error: str?,                // set when the build failed
}
)";
}

TEST(Schema, SpecExample) {
  auto [root, desc] = trak::parse_definition(kBuild, true);
  EXPECT_EQ(desc, "One CI build run.");
  ASSERT_EQ(root->members.size(), 6u);
  EXPECT_EQ(root->members[0].description, "who triggered the build");
  EXPECT_EQ(root->members[1].type->words, (std::vector<std::string>{"ok", "failed"}));
  EXPECT_EQ(root->members[2].type->annotations[0].to_string(), "@ms");
  EXPECT_EQ(root->members[3].type->kind, trak::Kind::List);
  EXPECT_TRUE(root->members[4].type->block);
  EXPECT_TRUE(root->members[5].type->optional);
}

TEST(Schema, Binding) {
  auto [root, desc] = trak::parse_definition("{ a: [] str?, b: [] str @x, c: |str @log(x y), d: {} num? @u, e: {}, f: {} {} }", true);
  const auto& m = root->members;
  EXPECT_TRUE(m[0].type->kind == trak::Kind::List && m[0].type->optional && !m[0].type->elem->optional);
  EXPECT_EQ(m[1].type->annotations.size(), 1u);
  EXPECT_EQ(m[2].type->annotations[0].to_string(), "@log(x y)");
  EXPECT_TRUE(m[3].type->kind == trak::Kind::Map && m[3].type->optional);
  EXPECT_TRUE(m[4].type->kind == trak::Kind::Object && m[4].type->members.empty());
  EXPECT_TRUE(m[5].type->kind == trak::Kind::Map && m[5].type->elem->kind == trak::Kind::Object);
}

TEST(Schema, Descriptions) {
  auto [root, desc] = trak::parse_definition(
      "// Line one.\n//\n// Line three.\n{\n  a: str, b: str, // b\n  // ignored\n  c: { // c\n    d: str, // d\n  },\n  e: str, //\n}\n",
      true);
  EXPECT_EQ(desc, "Line one.\n\nLine three.");
  EXPECT_EQ(root->members[0].description, "");
  EXPECT_EQ(root->members[1].description, "b");
  EXPECT_EQ(root->members[2].description, "c");
  EXPECT_EQ(root->members[2].type->members[0].description, "d");
  EXPECT_EQ(root->members[3].description, "");
}

TEST(Schema, UnknownLeaves) {
  auto [root, desc] = trak::parse_definition("{ a: si.meter, b: |vec(3)? }", false);
  EXPECT_EQ(root->members[0].type->unknown, "si.meter");
  EXPECT_EQ(root->members[1].type->unknown, "vec(3)");
  EXPECT_THROW(trak::parse_definition("{ a: foo }", true), trak::SchemaError);
}

TEST(Schema, Errors) {
  struct Case {
    const char* text;
    const char* msg;
    int line, column;
  };
  const Case cases[] = {
      {"", "expected '{'", 1, 1},
      {"{ a: str", "expected ',' or '}'", 1, 9},
      {"{ a str }", "expected ':'", 1, 5},
      {"{ a: str, a: num }", "duplicate key 'a'", 1, 11},
      {"{ 1a: str }", "expected a key", 1, 3},
      {"{\n  a: |[] str }", "'|' applies only to a leaf", 2, 6},
      {"{ a: ||str }", "'|' applies only to a leaf", 1, 6},
      {"{ a: str?? }", "repeated '?'", 1, 10},
      {"{ a: enum() }", "at least one word", 1, 6},
      {"{ a: enum(x, x) }", "duplicate enum word", 1, 14},
      {"{ a: enum(x y) }", "expected ',' or ')'", 1, 13},
      {"{ a: enum(1) }", "expected an enum word", 1, 11},
      {"{ a: enum }", "expected '(' after enum", 1, 10},
      {"{ a: str(x) }", "str takes no parameters", 1, 9},
      {"{ a: num @ }", "expected an annotation name", 1, 11},
      {"{ a: num @x(oops }", "unclosed '('", 1, 12},
      {"{ a: = }", "expected a type", 1, 6},
      {"{ a: [ str }", "expected ']'", 1, 8},
      {"{ a: str }?", "can't be optional", 1, 11},
      {"{ a: str } x", "unexpected text", 1, 12},
      {"{ a: str } // \xc3\xa9", "schemas are ASCII", 1, 15},
      {"{ a: str,\r\n}", "expected a key", 1, 10},
  };
  for (const auto& c : cases) {
    try {
      trak::parse_definition(c.text, true);
      ADD_FAILURE() << c.text;
    } catch (const trak::SchemaError& e) {
      EXPECT_NE(e.message().find(c.msg), std::string::npos) << c.text << ": " << e.what();
      EXPECT_EQ(e.line(), c.line) << c.text;
      EXPECT_EQ(e.column(), c.column) << c.text;
    }
  }
}
