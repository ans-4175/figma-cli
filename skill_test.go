package main

import (
	"os"
	"strings"
	"testing"
)

func TestEmbeddedSkillMatchesSource(t *testing.T) {
	source, err := os.ReadFile("skills/figma-cli.md")
	if err != nil {
		t.Fatal(err)
	}
	if skillMD != string(source) {
		t.Fatal("embedded skill differs from skills/figma-cli.md")
	}
}

func TestEmbeddedSkillReleaseGuidance(t *testing.T) {
	for _, required := range []string{
		"comments --json '<url>'",
		"client_meta.node_id",
		"does NOT filter them",
		"figma/mcp-server-guide",
		"plugin-api-standalone.index.md",
		"figma-generate-design",
		"`skillNames`",
		"verify the clone's parent",
		"create a NEW instance from the matching main",
		"stop at\n   `STRUCTURE_ONLY`",
		"v1.1.0 or later",
		"comments reply --comment-id",
		"file_comments:write",
		"never retries",
		"Edit where the comment lives",
		"A pin is approximate",
		"ONLY when the requester asks (opt-in)",
		"the API has no resolve",
	} {
		if !strings.Contains(skillMD, required) {
			t.Errorf("embedded skill missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"comments -json -url",
		"patched source build",
		"figma-review",
		"Goers",
		"goher",
		"goers",
	} {
		if strings.Contains(skillMD, forbidden) {
			t.Errorf("embedded skill contains %q", forbidden)
		}
	}
}
