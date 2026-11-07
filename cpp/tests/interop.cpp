// The C++ side of the cross-implementation tests (conformance/crosscheck.py).
//
//   trak_interop write OPS.json OUT.trak   replay operations into a new file
//   trak_interop read FILE.trak            print the decoded file and check result as JSON
#include <fstream>
#include <iostream>
#include <sstream>

#include "vectors.hpp"

int main(int argc, char** argv) {
  using nlohmann::json;
  if (argc < 3) {
    std::cerr << "usage: trak_interop write OPS OUT | trak_interop read FILE\n";
    return 2;
  }
  const std::string cmd = argv[1];
  try {
    if (cmd == "write" && argc == 4) {
      std::ifstream in(argv[2], std::ios::binary);
      std::ostringstream ss;
      ss << in.rdbuf();
      trak_vectors::replay(argv[3], json::parse(ss.str()));
      return 0;
    }
    if (cmd == "read") {
      json out;
      try {
        out = trak_vectors::decode(argv[2]);
      } catch (const std::exception& e) {
        out = {{"error", e.what()}};
      }
      try {
        trak::check(argv[2]);
        out["check"] = nullptr;
      } catch (const std::exception& e) {
        out["check"] = e.what();
      }
      std::cout << out.dump() << "\n";
      return 0;
    }
  } catch (const std::exception& e) {
    std::cerr << e.what() << "\n";
    return 1;
  }
  return 2;
}
