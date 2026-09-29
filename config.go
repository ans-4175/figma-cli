// config.go — konfigurasi ringan (~/.figma-cli/config.json) + dukungan
// file kredensial eksternal.
//
// Sumber token REST, urutannya:
//  1. flag --token
//  2. flag --token-file ATAU config token_file (file berisi token mentah
//     ATAU baris "FIGMA_TOKEN=figd_…")
//  3. alias --account / akun default (accounts.json)
//  4. env FIGMA_TOKEN
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// tokenFileOverride diisi flag --token-file (dipakai lintas command).
var tokenFileOverride string

type figmaConfig struct {
	TokenFile string `json:"token_file,omitempty"`
}

func configPath() string { return filepath.Join(cliDir(), "config.json") }

func loadConfig() *figmaConfig {
	raw, err := os.ReadFile(configPath())
	if err != nil {
		return &figmaConfig{}
	}
	var c figmaConfig
	if json.Unmarshal(raw, &c) != nil {
		return &figmaConfig{}
	}
	return &c
}

func saveConfig(c *figmaConfig) error {
	if err := os.MkdirAll(cliDir(), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(), data, 0o600)
}

// parseTokenFile membaca file kredensial: token mentah ATAU baris
// KEY=VALUE (mis. FIGMA_TOKEN=figd_…). Baris # komentar diabaikan.
func parseTokenFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return "", fmt.Errorf("file kosong")
	}
	if strings.Contains(s, "=") {
		for _, line := range strings.Split(s, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if i := strings.Index(line, "="); i > 0 {
				key := strings.TrimSpace(line[:i])
				val := strings.Trim(strings.TrimSpace(line[i+1:]), `"'`)
				if strings.EqualFold(key, "FIGMA_TOKEN") || strings.Contains(strings.ToUpper(key), "TOKEN") {
					if val != "" {
						return val, nil
					}
				}
			}
		}
	}
	return s, nil
}

// currentTokenFile mengembalikan path file token aktif (flag > config).
func currentTokenFile() string {
	if p := strings.TrimSpace(tokenFileOverride); p != "" {
		return p
	}
	return loadConfig().TokenFile
}

func cmdConfig(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: figma-cli config get | set token-file <path> | clear token-file")
	}
	sub := args[0]
	switch sub {
	case "get":
		c := loadConfig()
		if c.TokenFile == "" {
			fmt.Println("token-file: (tidak diset)")
		} else {
			fmt.Printf("token-file: %s\n", c.TokenFile)
		}
		return nil
	case "set":
		if len(args) < 3 || args[1] != "token-file" {
			return fmt.Errorf("usage: figma-cli config set token-file <path>")
		}
		path := args[2]
		if _, err := parseTokenFile(path); err != nil {
			return fmt.Errorf("file token tidak bisa dibaca: %w", err)
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			abs = path
		}
		c := loadConfig()
		c.TokenFile = abs
		if err := saveConfig(c); err != nil {
			return err
		}
		fmt.Printf("✅ token-file → %s\n", abs)
		return nil
	case "clear":
		if len(args) < 2 || args[1] != "token-file" {
			return fmt.Errorf("usage: figma-cli config clear token-file")
		}
		c := loadConfig()
		c.TokenFile = ""
		if err := saveConfig(c); err != nil {
			return err
		}
		fmt.Println("✅ token-file dikosongkan")
		return nil
	default:
		return fmt.Errorf("subperintah config tidak dikenal: %s", sub)
	}
}
