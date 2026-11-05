// folders.go — current folder catalog endpoints; no legacy projects fallback.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var catalogID = regexp.MustCompile(`^[0-9]+$`)

// Folder URLs may still use "project"; both identify a folder in the v2 API.
func parseCatalogID(raw, kind string) (string, error) {
	raw = strings.TrimSpace(raw)
	if catalogID.MatchString(raw) {
		return raw, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil ||
		(u.Host != "figma.com" && u.Host != "www.figma.com") {
		return "", fmt.Errorf("butuh ID numerik atau URL https://www.figma.com untuk %s", kind)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == kind || (kind == "folder" && parts[i] == "project") {
			if catalogID.MatchString(parts[i+1]) {
				return parts[i+1], nil
			}
		}
	}
	return "", fmt.Errorf("URL tidak memuat %s ID yang valid; folder/team URL bukan fileKey", kind)
}

func cmdCatalog(group string, args []string) error {
	usage := "figma-cli " + group
	if group == "folder" {
		usage += " files|folders|meta [flags] <folder-url|id>"
	} else {
		usage += " folders [flags] <team-url|id>"
	}
	if len(args) == 0 {
		return fmt.Errorf("usage: %s", usage)
	}
	if args[0] == "--help" || args[0] == "-h" {
		fmt.Println(usage)
		return nil
	}
	sub := args[0]
	if (group == "folder" && sub != "files" && sub != "folders" && sub != "meta") ||
		(group == "team" && sub != "folders") {
		return fmt.Errorf("usage: %s", usage)
	}
	fs := flag.NewFlagSet(group+" "+sub, flag.ContinueOnError)
	rf := &restFlags{}
	fs.StringVar(&rf.account, "account", "", "alias akun PAT")
	fs.StringVar(&rf.token, "token", "", "token PAT langsung (override)")
	fs.StringVar(&tokenFileOverride, "token-file", "", "path file kredensial; jangan taruh token di argv")
	fs.BoolVar(&rf.jsonOut, "json", false, "output JSON mentah")
	branchData := false
	if sub == "files" {
		fs.BoolVar(&branchData, "branch-data", false, "sertakan metadata branch")
	}
	if err := fs.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: %s — flags harus sebelum URL/ID", usage)
	}
	id, err := parseCatalogID(fs.Arg(0), group)
	if err != nil {
		return err
	}
	token, _, err := resolveToken(rf.account, rf.token)
	if err != nil {
		return err
	}
	path := "/v2/folders/" + id + "/" + sub
	if group == "team" {
		path = "/v2/teams/" + id + "/folders"
	}
	query := url.Values{}
	if branchData {
		query.Set("branch_data", "true")
	}
	raw, err := figmaAPI(path, token, query)
	if err != nil {
		return err
	}
	if rf.jsonOut {
		fmt.Println(string(raw))
		return nil
	}
	var out struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		FileCount int    `json:"file_count"`
		UpdatedAt string `json:"updated_at"`
		Files     []struct {
			Key          string `json:"key"`
			Name         string `json:"name"`
			LastModified string `json:"last_modified"`
		} `json:"files"`
		Folders []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"folders"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("response %s bukan JSON catalog yang valid: %w", path, err)
	}
	switch sub {
	case "meta":
		fmt.Printf("Folder: %s (id=%s, files=%d, updated=%s)\n", out.Name, out.ID, out.FileCount, out.UpdatedAt)
	case "files":
		fmt.Printf("Folder: %s (id=%s)\nFiles langsung di folder (%d; bukan recursive):\n", out.Name, id, len(out.Files))
		for _, f := range out.Files {
			fmt.Printf("  • %q key=%s modified=%s\n    https://www.figma.com/design/%s\n", f.Name, f.Key, f.LastModified, f.Key)
		}
	case "folders":
		fmt.Printf("Folders langsung (%d; bukan recursive):\n", len(out.Folders))
		for _, f := range out.Folders {
			fmt.Printf("  • %q id=%s\n", f.Name, f.ID)
		}
	}
	return nil
}
