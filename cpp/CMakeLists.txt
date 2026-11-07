cmake_minimum_required(VERSION 3.20)
project(trak VERSION 1.0.0 LANGUAGES CXX)

if(NOT CMAKE_CXX_STANDARD)
  set(CMAKE_CXX_STANDARD 17)
endif()
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

option(TRAK_BUILD_TESTS "Build trak tests" ${PROJECT_IS_TOP_LEVEL})
option(TRAK_BUILD_FUZZERS "Build libFuzzer targets (Clang only)" OFF)
option(TRAK_WERROR "Treat warnings as errors" OFF)
option(TRAK_SANITIZE "Build with ASan and UBSan" OFF)

function(trak_warnings target)
  if(MSVC)
    target_compile_options(${target} PRIVATE /W4 /permissive- /utf-8)
    if(TRAK_WERROR)
      target_compile_options(${target} PRIVATE /WX)
    endif()
  else()
    target_compile_options(${target} PRIVATE -Wall -Wextra -Wpedantic -Wshadow -Wconversion -Wsign-conversion)
    if(TRAK_WERROR)
      target_compile_options(${target} PRIVATE -Werror)
    endif()
  endif()
endfunction()

if(TRAK_SANITIZE AND NOT MSVC)
  add_compile_options(-fsanitize=address,undefined -fno-omit-frame-pointer -fno-sanitize-recover=all)
  add_link_options(-fsanitize=address,undefined)
endif()

add_library(trak
  src/core.cpp
  src/schema.cpp
  src/values.cpp
  src/reader.cpp
  src/writer.cpp
  src/check.cpp
  src/platform.cpp)
add_library(trak::trak ALIAS trak)
target_include_directories(trak PUBLIC
  $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
  $<INSTALL_INTERFACE:include>)
target_compile_features(trak PUBLIC cxx_std_17)
trak_warnings(trak)

include(GNUInstallDirs)
install(TARGETS trak EXPORT trakTargets
  ARCHIVE DESTINATION ${CMAKE_INSTALL_LIBDIR}
  LIBRARY DESTINATION ${CMAKE_INSTALL_LIBDIR})
install(DIRECTORY include/ DESTINATION ${CMAKE_INSTALL_INCLUDEDIR})
install(EXPORT trakTargets NAMESPACE trak:: DESTINATION ${CMAKE_INSTALL_LIBDIR}/cmake/trak)

if(TRAK_BUILD_TESTS)
  include(FetchContent)
  FetchContent_Declare(googletest
    URL https://github.com/google/googletest/archive/refs/tags/v1.15.2.tar.gz
    DOWNLOAD_EXTRACT_TIMESTAMP TRUE)
  FetchContent_Declare(json
    URL https://github.com/nlohmann/json/releases/download/v3.11.3/json.tar.xz
    DOWNLOAD_EXTRACT_TIMESTAMP TRUE)
  set(gtest_force_shared_crt ON CACHE BOOL "" FORCE)
  set(INSTALL_GTEST OFF CACHE BOOL "" FORCE)
  FetchContent_MakeAvailable(googletest json)
  enable_testing()
  add_executable(trak_tests
    tests/core_test.cpp
    tests/schema_test.cpp
    tests/rw_test.cpp
    tests/conformance_test.cpp
    tests/vectors.cpp)
  target_link_libraries(trak_tests PRIVATE trak::trak GTest::gtest_main nlohmann_json::nlohmann_json)
  target_compile_definitions(trak_tests PRIVATE
    TRAK_REPO_DIR="${CMAKE_CURRENT_SOURCE_DIR}/..")
  trak_warnings(trak_tests)
  add_executable(trak_interop tests/interop.cpp tests/vectors.cpp)
  target_link_libraries(trak_interop PRIVATE trak::trak nlohmann_json::nlohmann_json)
  trak_warnings(trak_interop)
  include(GoogleTest)
  gtest_discover_tests(trak_tests DISCOVERY_TIMEOUT 60)
endif()

if(TRAK_BUILD_FUZZERS)
  add_executable(trak_fuzz_reader fuzz/fuzz_reader.cpp)
  target_link_libraries(trak_fuzz_reader PRIVATE trak::trak)
  target_compile_options(trak_fuzz_reader PRIVATE -fsanitize=fuzzer,address,undefined)
  target_link_options(trak_fuzz_reader PRIVATE -fsanitize=fuzzer,address,undefined)
endif()
