// Schema parsing (SPEC §5) and footer lines (SPEC §5, §6, §8).
#include <algorithm>
#include <map>
#include <set>

#include "internal.hpp"

namespace trak {

std::string Annotation::to_string() const { return has_text ? "@" + name + "(" + text + ")" : "@" + name; }

const std::vector<Annotation>& Schema::annotations() const {
  static const std::vector<Annotation> none;
  return root ? root->annotations : none;
}

const Field* Schema::field(std::string_view path) const {
  for (const auto& f : fields) {
    if (f.path == path) return &f;
  }
  return nullptr;
}

namespace {

bool is_alpha(char c) { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'); }
bool is_digit(char c) { return c >= '0' && c <= '9'; }

// Length of an identifier [A-Za-z_][A-Za-z0-9_<extra>]* at the start of s.
std::size_t match_word(std::string_view s, std::string_view extra) {
  if (s.empty() || !(is_alpha(s[0]) || s[0] == '_')) return 0;
  std::size_t i = 1;
  while (i < s.size() && (is_alpha(s[i]) || is_digit(s[i]) || s[i] == '_' || extra.find(s[i]) != std::string_view::npos))
    ++i;
  return i;
}

std::string trim(std::string_view s) {
  const auto b = s.find_first_not_of(" \t");
  if (b == std::string_view::npos) return "";
  const auto e = s.find_last_not_of(" \t");
  return std::string(s.substr(b, e - b + 1));
}

struct Comment {
  int line;
  bool alone;
  std::string text;
};

class Parser {
  struct MemberLine {
    Type* obj;
    std::size_t index;
    int line;
    std::size_t pos;
  };

 public:
  Parser(std::string_view s, bool strict) : s_(s), strict_(strict) {}

  std::pair<std::shared_ptr<Type>, std::string> parse() {
    for (std::size_t i = 0; i < s_.size(); ++i) {
      if (static_cast<unsigned char>(s_[i]) >= 0x80) fail(i, "schemas are ASCII");
    }
    ws();
    if (peek() != '{') fail(i_, "expected '{' after the type's comment lines");
    const std::size_t brace = i_;
    auto root = parse_object();
    for (;;) {
      ws();
      if (peek() == '@') {
        root->annotations.push_back(parse_annotation());
      } else if (peek() == '?') {
        fail(i_, "an event's top-level type can't be optional");
      } else {
        break;
      }
    }
    if (i_ != s_.size()) fail(i_, "unexpected text after the definition");
    std::string desc;
    bool first = true;
    for (const auto& [pos, c] : comments_) {
      if (pos >= brace) break;
      if (!first) desc += '\n';
      desc += c.text;
      first = false;
    }
    // A trailing comment describes the last key that starts on its line.
    std::map<int, MemberLine> by_line;
    for (const auto& m : member_lines_) {
      auto it = by_line.find(m.line);
      if (it == by_line.end() || m.pos > it->second.pos) by_line[m.line] = m;
    }
    for (const auto& [pos, c] : comments_) {
      if (pos <= brace || c.alone) continue;
      auto it = by_line.find(c.line);
      if (it != by_line.end()) it->second.obj->members[it->second.index].description = c.text;
    }
    return {root, desc};
  }

 private:
  [[noreturn]] void fail(std::size_t pos, const std::string& message) {
    const auto head = s_.substr(0, pos);
    const int line = static_cast<int>(std::count(head.begin(), head.end(), '\n')) + 1;
    const auto nl = head.rfind('\n');
    const int column = static_cast<int>(pos - (nl == std::string_view::npos ? 0 : nl + 1)) + 1;
    throw SchemaError(message, line, column);
  }

  char peek() const { return i_ < s_.size() ? s_[i_] : '\0'; }

  int line_of(std::size_t pos) const {
    return static_cast<int>(std::count(s_.begin(), s_.begin() + static_cast<std::ptrdiff_t>(pos), '\n')) + 1;
  }

  void ws() {
    while (i_ < s_.size()) {
      const char c = s_[i_];
      if (c == ' ' || c == '\t' || c == '\n') {
        ++i_;
      } else if (s_.substr(i_, 2) == "//") {
        auto end = s_.find('\n', i_);
        if (end == std::string_view::npos) end = s_.size();
        if (comments_.find(i_) == comments_.end()) {
          const auto nl = s_.rfind('\n', i_ == 0 ? 0 : i_ - 1);
          const std::size_t line_start = (nl == std::string_view::npos || (i_ == 0)) ? 0 : nl + 1;
          const bool alone = trim(s_.substr(line_start, i_ - line_start)).empty();
          comments_[i_] = Comment{line_of(i_), alone, trim(s_.substr(i_ + 2, end - i_ - 2))};
        }
        i_ = end;
      } else {
        return;
      }
    }
  }

  void expect(char c) {
    ws();
    if (peek() != c) fail(i_, std::string("expected '") + c + "'");
    ++i_;
  }

  std::shared_ptr<Type> parse_object() {
    expect('{');
    auto obj = std::make_shared<Type>();
    obj->kind = Kind::Object;
    std::set<std::string> keys;
    for (;;) {
      ws();
      if (peek() == '}') {
        ++i_;
        return obj;
      }
      const std::size_t n = match_word(s_.substr(i_), "");
      if (n == 0) fail(i_, "expected a key");
      const std::size_t pos = i_;
      std::string key(s_.substr(pos, n));
      if (!keys.insert(key).second) fail(pos, "duplicate key '" + key + "'");
      i_ += n;
      expect(':');
      auto t = parse_type();
      obj->members.push_back(Member{key, t, ""});
      const std::size_t idx = obj->members.size() - 1;
      member_lines_.push_back({obj.get(), idx, line_of(pos), pos});
      ws();
      if (peek() == ',') {
        ++i_;
      } else if (peek() != '}') {
        fail(i_, "expected ',' or '}'");
      }
    }
  }

  std::shared_ptr<Type> parse_type() {
    std::vector<std::pair<char, std::size_t>> prefixes;
    std::shared_ptr<Type> node;
    while (!node) {
      ws();
      const char c = peek();
      const std::size_t pos = i_;
      if (c == '|') {
        prefixes.emplace_back('|', pos);
        ++i_;
      } else if (c == '[') {
        ++i_;
        expect(']');
        prefixes.emplace_back('[', pos);
      } else if (c == '{') {
        ++i_;
        ws();
        if (peek() != '}') {
          i_ = pos;
          break;
        }
        ++i_;
        const std::size_t after = i_;
        ws();
        const char d = peek();
        if (d == '|' || d == '{' || d == '[' || d == '_' || is_alpha(d)) {
          prefixes.emplace_back('{', pos);
        } else {
          i_ = after;
          node = std::make_shared<Type>();
          node->kind = Kind::Object;
        }
      } else {
        break;
      }
    }
    if (!node) node = parse_base();
    for (auto it = prefixes.rbegin(); it != prefixes.rend(); ++it) {
      if (it->first == '|') {
        if (!node->is_leaf() || node->block) fail(it->second, "'|' applies only to a leaf");
        node->block = true;
      } else {
        auto outer = std::make_shared<Type>();
        outer->kind = it->first == '[' ? Kind::List : Kind::Map;
        outer->elem = node;
        node = outer;
      }
    }
    for (;;) {
      ws();
      if (peek() == '?') {
        if (node->optional) fail(i_, "repeated '?'");
        node->optional = true;
        ++i_;
      } else if (peek() == '@') {
        node->annotations.push_back(parse_annotation());
      } else {
        return node;
      }
    }
  }

  Annotation parse_annotation() {
    ++i_;
    const std::size_t n = match_word(s_.substr(i_), ".-");
    if (n == 0) fail(i_, "expected an annotation name");
    Annotation a;
    a.name = std::string(s_.substr(i_, n));
    i_ += n;
    if (peek() == '(') {
      a.text = parse_parens();
      a.has_text = true;
    }
    return a;
  }

  std::string parse_parens() {
    const std::size_t start = i_ + 1;
    const auto end = s_.find(')', start);
    if (end == std::string_view::npos || s_.substr(start, end - start).find('\n') != std::string_view::npos) {
      fail(i_, "unclosed '(' (text can't contain ')' or a line break)");
    }
    i_ = end + 1;
    return std::string(s_.substr(start, end - start));
  }

  std::shared_ptr<Type> parse_base() {
    if (peek() == '{') return parse_object();
    const std::size_t start = i_;
    const std::size_t n = match_word(s_.substr(i_), ".");
    if (n == 0) fail(i_, "expected a type");
    const std::string word(s_.substr(i_, n));
    i_ += n;
    auto t = std::make_shared<Type>();
    if (word == "str" || word == "num") {
      if (peek() == '(') fail(i_, word + " takes no parameters");
      t->kind = word == "str" ? Kind::Str : Kind::Num;
      return t;
    }
    if (word == "enum") {
      if (peek() != '(') fail(i_, "expected '(' after enum");
      ++i_;
      t->kind = Kind::Enum;
      for (;;) {
        ws();
        if (peek() == ')') {
          ++i_;
          break;
        }
        const std::size_t m = match_word(s_.substr(i_), "");
        if (m == 0) fail(i_, "expected an enum word");
        std::string w(s_.substr(i_, m));
        if (std::find(t->words.begin(), t->words.end(), w) != t->words.end()) {
          fail(i_, "duplicate enum word '" + w + "'");
        }
        t->words.push_back(std::move(w));
        i_ += m;
        ws();
        if (peek() == ',') {
          ++i_;
        } else if (peek() != ')') {
          fail(i_, "expected ',' or ')'");
        }
      }
      if (t->words.empty()) fail(start, "enum needs at least one word");
      return t;
    }
    if (peek() == '(') parse_parens();
    if (strict_) fail(start, "unknown type '" + word + "'");
    t->kind = Kind::Str;
    t->unknown = std::string(s_.substr(start, i_ - start));
    return t;
  }

  std::string_view s_;
  std::size_t i_ = 0;
  bool strict_;
  std::map<std::size_t, Comment> comments_;
  std::vector<MemberLine> member_lines_;
};

void flatten_type(const Type& t, const std::string& path, bool optional, std::vector<Annotation> anns,
                  const std::string& desc, std::vector<Field>& out);

void flatten_object(const Type& obj, const std::string& prefix, bool optional, std::vector<Field>& out) {
  for (const auto& m : obj.members) {
    flatten_type(*m.type, prefix.empty() ? m.key : prefix + "." + m.key, optional, {}, m.description, out);
  }
}

void flatten_type(const Type& t, const std::string& path, bool optional, std::vector<Annotation> anns,
                  const std::string& desc, std::vector<Field>& out) {
  optional = optional || t.optional;
  anns.insert(anns.end(), t.annotations.begin(), t.annotations.end());
  switch (t.kind) {
    case Kind::Str:
    case Kind::Num:
    case Kind::Enum: {
      Field f;
      f.path = path;
      f.type = t.kind == Kind::Str ? "str" : t.kind == Kind::Num ? "num" : "enum";
      f.enum_words = t.words;
      f.unknown = t.unknown;
      f.block = t.block;
      f.optional = optional;
      f.annotations = std::move(anns);
      f.description = desc;
      out.push_back(std::move(f));
      break;
    }
    case Kind::Object:
      flatten_object(t, path, optional, out);
      break;
    case Kind::List:
      flatten_type(*t.elem, path + ".[]", optional, std::move(anns), desc, out);
      break;
    case Kind::Map:
      flatten_type(*t.elem, path + ".{}", optional, std::move(anns), desc, out);
      break;
  }
}

}  // namespace

std::pair<std::shared_ptr<Type>, std::string> parse_definition(std::string_view text, bool strict) {
  return Parser(text, strict).parse();
}

namespace detail {

bool is_version(std::string_view s) noexcept {
  const auto dot = s.find('.');
  if (dot == std::string_view::npos || dot == 0 || dot == s.size() - 1) return false;
  for (std::size_t i = 0; i < s.size(); ++i) {
    if (i != dot && !is_digit(s[i])) return false;
  }
  return true;
}

bool canonical_version(std::string_view s) noexcept {
  if (!is_version(s)) return false;
  const auto dot = s.find('.');
  return (dot == 1 || s[0] != '0') && (s.size() - dot - 1 == 1 || s[dot + 1] != '0');
}

bool valid_type_name(std::string_view s) noexcept { return !s.empty() && match_word(s, ".") == s.size(); }

Schema build_schema(const std::string& name, int index, const std::string& version, const std::string& text,
                    bool strict) {
  Schema s;
  s.name = name;
  s.index = index;
  s.version = version;
  s.text = text;
  try {
    if (!canonical_version(version) || version.substr(0, version.find('.')) != "1") {
      throw SchemaError("unsupported schema version " + version);
    }
    auto [root, desc] = parse_definition(text, strict);
    s.root = root;
    s.description = desc;
  } catch (const SchemaError& e) {
    if (strict) throw;
    s.root = nullptr;
    s.error = e.what();
    return s;
  }
  flatten_object(*s.root, "", false, s.fields);
  return s;
}

// --- footers ---

std::int64_t Footer::target(std::uint64_t pointer) const {
  if (pointer == 0) format_error(fstart, "required pointer is 0");
  if (pointer - 1 > static_cast<std::uint64_t>(start)) format_error(fstart, "pointer reaches before the start of the file");
  return start - static_cast<std::int64_t>(pointer - 1);
}

namespace {
bool is_decimal(std::string_view b) {
  if (b.empty() || b[0] < '1' || b[0] > '9') return false;
  return std::all_of(b.begin(), b.end(), is_digit);
}
}  // namespace

Footer parse_footer(std::string_view buf, std::int64_t fstart, std::int64_t end, std::int64_t min_start) {
  const std::string_view line = buf.substr(static_cast<std::size_t>(fstart), static_cast<std::size_t>(end - 1 - fstart));
  Footer f;
  f.fstart = fstart;
  f.end = end;
  std::string_view close;
  if (line.substr(0, 4) == "##[ ") {
    f.schema = true;
    close = " ]##";
  } else if (line.substr(0, 4) == "==[ ") {
    close = " ]==";
  } else {
    format_error(fstart, "line is not a footer");
  }
  const auto k = line.find(close, 4);
  if (k == std::string_view::npos) format_error(fstart, "footer has no closing marker");
  const std::string_view fields = line.substr(4, k - 4);
  std::string_view parts[3];
  std::size_t count = 0, from = 0;
  for (;;) {
    const auto sp = fields.find(' ', from);
    const auto part = fields.substr(from, sp == std::string_view::npos ? std::string_view::npos : sp - from);
    if (count == 3 || part.empty()) format_error(fstart + 4, "footer needs exactly three fields");
    parts[count++] = part;
    if (sp == std::string_view::npos) break;
    from = sp + 1;
  }
  if (count != 3) format_error(fstart + 4, "footer needs exactly three fields");
  const auto letters_at = fstart + static_cast<std::int64_t>(k) + 4;
  const std::string_view letters = line.substr(k + 4);
  if (letters.empty()) format_error(letters_at, "footer has no VLQs");
  f.vlqs = decode_vlqs(letters, letters_at);
  for (char c : parts[0]) {
    if (static_cast<unsigned char>(c) >= 0x80) format_error(fstart + 4, "type name is not ASCII");
  }
  f.name = std::string(parts[0]);
  const auto num_at = fstart + 5 + static_cast<std::int64_t>(parts[0].size());
  if (!is_decimal(parts[1])) format_error(num_at, "bad decimal number in footer");
  if (parts[1].size() > 20 || (parts[1].size() == 20 && parts[1] > "18446744073709551615")) {
    format_error(num_at, "footer number overflows");
  }
  f.num = std::stoull(std::string(parts[1]));
  const auto stamp_at = num_at + 1 + static_cast<std::int64_t>(parts[1].size());
  f.stamp = std::string(parts[2]);
  const auto& v = f.vlqs;
  if (f.schema) {
    if (!is_version(parts[2])) format_error(stamp_at, "bad version in schema footer");
    if (v.size() < 3 || v.size() - 3 != v[2]) format_error(letters_at, "schema footer has the wrong number of VLQs");
  } else {
    if (!valid_stamp_syntax(parts[2])) format_error(stamp_at, "bad timestamp in event footer");
    if (v.size() < 6 || v.size() - 6 < v[5]) format_error(letters_at, "event footer has too few VLQs");
  }
  if (v[0] > static_cast<std::uint64_t>(fstart - min_start)) {
    format_error(letters_at, "body length reaches past the first object");
  }
  f.start = fstart - static_cast<std::int64_t>(v[0]);
  return f;
}

void append_schema_footer(std::string& out, const std::string& name, int index, const std::string& version,
                          const std::vector<std::uint64_t>& vlqs) {
  out += "##[ ";
  out += name;
  out += ' ';
  out += std::to_string(index);
  out += ' ';
  out += version;
  out += " ]##";
  for (auto v : vlqs) append_vlq(out, v);
  out += '\n';
}

void append_event_footer(std::string& out, const std::string& name, std::uint64_t n, const std::string& stamp,
                         const std::vector<std::uint64_t>& vlqs) {
  out += "==[ ";
  out += name;
  out += ' ';
  out += std::to_string(n);
  out += ' ';
  out += stamp;
  out += " ]==";
  for (auto v : vlqs) append_vlq(out, v);
  out += '\n';
}

}  // namespace detail
}  // namespace trak
