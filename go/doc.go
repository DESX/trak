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
//	w.Define("build.step", "{ step: str }") // a child: user, duration, step
//	pos, err := w.Write("build", map[string]any{"user": "ana", "duration": 910})
//
// Reading:
//
//	r, err := trak.Open("app.trak")
//	if err != nil { ... }
//	defer r.Close()
//	builds, _ := r.Table("build")
//	n, _ := builds.Count()
//
// A type named P.x is a child of P: it has P's fields, then its own. P's
// family holds the events of P and all its descendants in file order, and is
// a table of its own:
//
//	fam, _ := r.Family("build")
//	rows, _ := fam.Count()
//	row, _ := fam.Get(rows) // O(k log n) for k types
//
// Before and After page through all events in file order from a cursor, an
// Event.Offset or a Pos.Offset from Write:
//
//	page, _ := r.After(pos.Offset, 100)
//
// Readers and writers are not safe for concurrent use.
package trak
