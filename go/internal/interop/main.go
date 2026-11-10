// Command interop is the Go side of the cross-implementation tests
// (conformance/crosscheck.py).
//
//	interop write OPS.json OUT.trak   replay operations into a new file
//	interop read FILE.trak            print the decoded file and check result as JSON
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/desx/trak/go"
	"github.com/desx/trak/go/internal/vectors"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: interop write OPS OUT | interop read FILE")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "write":
		input, err := os.ReadFile(os.Args[2])
		if err == nil {
			err = vectors.Replay(os.Args[3], input)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "read":
		out, err := vectors.Decode(os.Args[2])
		if err != nil {
			out = map[string]any{"error": err.Error()}
		}
		var checkErr any
		if _, err := trak.Check(os.Args[2]); err != nil {
			checkErr = err.Error()
		}
		out["check"] = checkErr
		json.NewEncoder(os.Stdout).Encode(out)
	default:
		os.Exit(2)
	}
}
