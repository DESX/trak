// Package trak reads and writes trak files: a log format that programs write
// and read, and people can read too. The format is specified in SPEC.md at
// the root of this repository.
//
// Writing:
//
//	w, err := trak.Create("app.trak", trak.Options{})
//	if err != nil { ... }
//	defer w.Close()
//	w.Define("build", "{ user: str, duration: num }")
//	w.Write("build", map[string]any{"user": "ana", "duration": 910})
//
// Reading:
//
//	r, err := trak.Open("app.trak")
//	if err != nil { ... }
//	defer r.Close()
//	builds, _ := r.Table("build")
//	n, _ := builds.Count()
//
// Readers and writers are not safe for concurrent use.
package trak
