// accounts.go — penyimpanan multi-account PAT (personal access token).
//
// File: ~/.figma-cli/accounts.json (mode 0600). Urutan resolusi token
// sama dengan extension pi: flag --token > alias --account > akun default
// > env FIGMA_TOKEN.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Account struct {
	Token string `json:"token"`
	Label string `json:"label,omitempty"`
}

type AccountsFile struct {
	Accounts map[string]Account `json:"accounts"`
	Default  string             `json:"default,omitempty"`
}

func cliDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		h = "."
	}
	return filepath.Join(h, ".figma-cli")
}

func accountsPath() string          { return filepath.Join(cliDir(), "accounts.json") }
func mcpAuthDir() string            { return filepath.Join(cliDir(), "mcp-auth") }
func runtimeDir() string            { return cliDir() }
func sockPath() string              { return filepath.Join(runtimeDir(), "daemon.sock") }
func pidPath() string               { return filepath.Join(runtimeDir(), "daemon.pid") }
func logPath() string               { return filepath.Join(runtimeDir(), "daemon.log") }
func tokenFile(alias string) string { return filepath.Join(mcpAuthDir(), alias+".json") }

func loadAccounts() (*AccountsFile, error) {
	raw, err := os.ReadFile(accountsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &AccountsFile{Accounts: map[string]Account{}}, nil
		}
		return nil, err
	}
	var af AccountsFile
	if err := json.Unmarshal(raw, &af); err != nil {
		return nil, fmt.Errorf("accounts.json rusak: %w", err)
	}
	if af.Accounts == nil {
		af.Accounts = map[string]Account{}
	}
	return &af, nil
}

func saveAccounts(af *AccountsFile) error {
	if err := os.MkdirAll(cliDir(), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(af, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(accountsPath(), data, 0o600); err != nil {
		return err
	}
	return nil
}

var aliasRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

func validAlias(alias string) bool { return aliasRe.MatchString(alias) }

func maskToken(t string) string {
	if len(t) <= 12 {
		if len(t) > 4 {
			return t[:4] + "…"
		}
		return "…"
	}
	return t[:8] + "…" + t[len(t)-4:]
}

// resolveToken mengembalikan (token, sumber, error).
func resolveToken(account, tokenFlag string) (string, string, error) {
	if strings.TrimSpace(tokenFlag) != "" {
		return strings.TrimSpace(tokenFlag), "flag --token", nil
	}
	// token-file: flag --token-file > config token_file
	if path := currentTokenFile(); path != "" {
		tok, err := parseTokenFile(path)
		if err != nil {
			return "", "", fmt.Errorf("token-file %s: %w", path, err)
		}
		return tok, "token-file:" + path, nil
	}
	af, err := loadAccounts()
	if err != nil {
		return "", "", err
	}
	alias := strings.TrimSpace(account)
	if alias == "" {
		alias = af.Default
	}
	if alias != "" {
		if a, ok := af.Accounts[alias]; ok && a.Token != "" {
			return a.Token, "account:" + alias, nil
		}
		if alias != "default" {
			return "", "", fmt.Errorf("akun %q tidak ditemukan — /figma-cli accounts add %s <token>", alias, alias)
		}
	}
	if env := strings.TrimSpace(os.Getenv("FIGMA_TOKEN")); env != "" {
		return env, "env:FIGMA_TOKEN", nil
	}
	return "", "", fmt.Errorf("token Figma tidak ditemukan.\n" +
		"  1. figma-cli accounts add <alias> figd_...\n" +
		"  2. export FIGMA_TOKEN=figd_...\n" +
		"  3. flag --token figd_...")
}
