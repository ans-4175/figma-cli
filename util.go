// util.go — helper kecil yang dipakai lintas file.
package main

import (
	"bufio"
	"encoding/base64"
	"net/url"
	"os"
)

// toURLValues mengubah map sederhana jadi url.Values.
func toURLValues(m map[string][]string) url.Values {
	v := url.Values{}
	for k, vs := range m {
		for _, s := range vs {
			if s != "" {
				v.Add(k, s)
			}
		}
	}
	return v
}

// bufioReadLine membaca satu baris dari stdin (untuk input token interaktif).
func bufioReadLine() (string, error) {
	r := bufio.NewReader(os.Stdin)
	return r.ReadString('\n')
}

// mustDecodeB64 decode base64 (payload gambar MCP); gagal → slice kosong.
func mustDecodeB64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return []byte{}
	}
	return b
}
