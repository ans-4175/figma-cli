package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadArgsFile(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "a.json")
	os.WriteFile(good, []byte("  {\"code\": \"figma.currentPage.name = 'x \\\"y\\\"'\"}\n"), 0o600)
	got, err := readArgsFile(good)
	if err != nil || !strings.HasPrefix(got, "{") || strings.HasSuffix(got, "\n") {
		t.Fatalf("good file: %q %v", got, err)
	}
	cases := map[string][]byte{"empty.json": []byte("  "), "array.json": []byte("[1]"), "nul.json": []byte("{\"a\":\"\x00\"}")}
	for name, data := range cases {
		p := filepath.Join(dir, name)
		os.WriteFile(p, data, 0o600)
		if _, err := readArgsFile(p); err == nil {
			t.Fatalf("%s should fail", name)
		}
	}
	if _, err := readArgsFile(dir); err == nil {
		t.Fatal("directory should fail")
	}
	big := filepath.Join(dir, "big.json")
	os.WriteFile(big, append([]byte("{\"a\":\""), make([]byte, maxArgsFileBytes)...), 0o600)
	if _, err := readArgsFile(big); err == nil {
		t.Fatal("oversized should fail")
	}
	if _, err := readArgsFile(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("missing should fail")
	}
}
