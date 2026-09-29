// skill.go — skill agent di-embed ke binary saat build (portabel, bisa
// dibaca tanpa daemon jalan) — pola sama dengan pen-cli --skills.
package main

import (
	_ "embed"
	"fmt"
)

//go:embed skills/figma-cli.md
var skillMD string

func cmdSkill() error {
	fmt.Print(skillMD)
	return nil
}
