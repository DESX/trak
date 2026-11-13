package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func vectorPath(dir, name string) string {
	return filepath.Join("..", "..", "..", "conformance", dir, name+".trak")
}

func runCLI(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestCLI(t *testing.T) {
	ok := vectorPath("valid", "004-nested")
	cases := []struct {
		args []string
		code int
		out  string
	}{
		{[]string{"check", ok}, 0, "ok, 9 versions"},
		{[]string{"check", vectorPath("invalid", "010-not-a-path-copy")}, 1, ""},
		{[]string{"stat", ok}, 0, "footer bytes"},
		{[]string{"schema", ok}, 0, "root nest {"},
		{[]string{"get", ok, "items.1.extra.why"}, 0, "because\nof this\n"},
		{[]string{"get", ok, "items.1"}, 0, "items.1.meta.owner: bo"},
		{[]string{"get", ok, "config.limits.soft"}, 0, "2 items"},
		{[]string{"get", ok, "config.limits.soft.0", "--at", "3"}, 0, "10\n"},
		{[]string{"get", ok, "."}, 0, "items: 2 items"},
		{[]string{"get", ok, "items.0.extra.why"}, 1, ""},
		{[]string{"get", ok, "items", "--at", "x"}, 2, ""},
		{[]string{"get", ok}, 2, ""},
		{[]string{"nope"}, 2, ""},
		{nil, 2, ""},
	}
	for _, c := range cases {
		code, out, errOut := runCLI(c.args...)
		if code != c.code || !strings.Contains(out, c.out) {
			t.Errorf("trak %v: exit %d, out %q, err %q", c.args, code, out, errOut)
		}
	}
	code, _, errOut := runCLI("check", vectorPath("invalid", "004-body-framing"))
	if code != 1 || !strings.Contains(errOut, "body: expected user:") {
		t.Errorf("check invalid: %d %q", code, errOut)
	}
}

func TestServeArgs(t *testing.T) {
	if code, _, _ := runCLI("serve"); code != 2 {
		t.Errorf("no dir: exit %d", code)
	}
	if code, _, _ := runCLI("serve", t.TempDir(), "--prefix", "x"); code != 2 {
		t.Errorf("bad prefix: exit %d", code)
	}
	f := filepath.Join(t.TempDir(), "f")
	_ = os.WriteFile(f, nil, 0o644)
	if code, _, _ := runCLI("serve", f); code != 1 {
		t.Errorf("not a dir: exit %d", code)
	}
}
