// figma-cli — REST API Figma + remote MCP daemon-client, pola pen-cli.
//
// Daemon (`serve`) internal: memegang sesi MCP (OAuth + HTTP) per-alias,
// melayani client lewat unix socket, idle-timeout 15 menit. Client tipis:
// auto-start daemon pada command pertama.
package main

import (
	"fmt"
	"os"
)

// version di-override saat release via -ldflags "-X main.version=vX.Y.Z" (lihat .github/workflows/release.yml).
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve": // internal — dipanggil auto-start client, jangan manual
		err = runDaemon()
	case "daemon":
		err = cmdDaemon(os.Args[2:])
	case "doctor":
		err = cmdDoctor()
	case "understand":
		err = cmdUnderstand(os.Args[2:])
	case "file":
		err = cmdFile(os.Args[2:])
	case "node":
		err = cmdNode(os.Args[2:])
	case "components":
		err = cmdComponents(os.Args[2:])
	case "comments":
		err = cmdComments(os.Args[2:])
	case "versions":
		err = cmdVersions(os.Args[2:])
	case "dev-resources":
		err = cmdDevResources(os.Args[2:])
	case "accounts":
		err = cmdAccounts(os.Args[2:])
	case "mcp":
		err = cmdMCP(os.Args[2:])
	case "config":
		err = cmdConfig(os.Args[2:])
	case "skill", "--skill", "--skills":
		err = cmdSkill()
	case "version", "--version", "-v":
		fmt.Println("figma-cli " + version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "figma-cli: perintah tidak dikenal %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "figma-cli: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`figma-cli ` + version + ` — bedah design Figma: REST API + MCP daemon

Usage:
  figma-cli understand <url> [nodeID]   ⭐ Outline node (layout, warna, teks)
        [--depth N] [--json]
  figma-cli file <url>                  Metadata file + halaman
  figma-cli node <url> [nodeID]         Dump JSON mentah node [--depth N]
  figma-cli components <url>            Components, sets, styles [--json]
  figma-cli comments <url>              Daftar komentar
  figma-cli versions <url>              Riwayat versi
  figma-cli dev-resources <url>         Link dev (code, docs, Storybook)
  figma-cli accounts add|list|use|remove   Multi-akun PAT
  figma-cli config get | set token-file <path> | clear token-file
                                        Arahkan ke file kredensial
                                        (token mentah atau FIGMA_TOKEN=…)
  figma-cli mcp tools                   Daftar tool MCP (OAuth; buka browser
                                        saat pertama kali)
  figma-cli mcp call --tool T [--args '{…}'] [url] [nodeID]
                                        Panggil tool MCP (hasil gambar → PNG)
  figma-cli mcp status|reset [alias]    Cek / hapus OAuth per-alias
  figma-cli daemon status|stop          Kontrol daemon manual
  figma-cli doctor                      Cek akun, token, daemon, REST API
  figma-cli skill | --skill             Baca skill agent (embedded)

Flag umum: --account ALIAS  --token FIGD  --token-file PATH  --json
URL Figma lengkap diterima langsung: node-id di URL dipakai otomatis.
Daemon start otomatis pada perintah MCP pertama; idle-timeout 15 menit.
Runtime: ~/.figma-cli/ (accounts.json, config.json, mcp-auth/, daemon.*)
`)
}
