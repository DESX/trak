// Decoding and replaying conformance vectors (see conformance/README.md).
#pragma once

#include <nlohmann/json.hpp>
#include <string>

#include "trak/trak.hpp"

namespace trak_vectors {

// Reads a file into the expect.json structure, checking that navigation
// agrees with a file-order scan.
nlohmann::json decode(const std::string& path);

// Applies an input.json to a new file at path.
void replay(const std::string& path, const nlohmann::json& ops);

}  // namespace trak_vectors
