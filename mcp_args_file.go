package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"unicode/utf8"
)

const maxArgsFileBytes = 512 * 1024

// readArgsFile loads MCP call arguments from a file so long payloads (plugin code with quotes and newlines)
// never need shell quoting or an inline interpreter.
func readArgsFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("tidak bisa membaca --args-file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("--args-file harus file biasa")
	}
	if info.Size() > maxArgsFileBytes {
		return "", fmt.Errorf("--args-file terlalu besar (%d byte; maksimal %d)", info.Size(), maxArgsFileBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("tidak bisa membaca --args-file: %w", err)
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return "", errors.New("--args-file harus teks UTF-8 tanpa byte NUL")
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return "", errors.New("--args-file harus berisi objek JSON")
	}
	return string(trimmed), nil
}
