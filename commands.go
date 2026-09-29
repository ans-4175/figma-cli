// commands.go — implementasi perintah-perintah figma-cli.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---- helper flag per perintah ----

type restFlags struct {
	url     string
	fileKey string
	nodeID  string
	account string
	token   string
	jsonOut bool
	depth   int
}

func newRestFlags(fs *flag.FlagSet) *restFlags {
	rf := &restFlags{}
	fs.StringVar(&rf.url, "url", "", "URL Figma penuh (design/file/proto)")
	fs.StringVar(&rf.fileKey, "k", "", "fileKey (alternatif --url)")
	fs.StringVar(&rf.nodeID, "n", "", "node id, mis. 451:36249 atau 451-36249")
	fs.StringVar(&rf.account, "account", "", "alias akun PAT (default akun default/env)")
	fs.StringVar(&rf.token, "token", "", "token PAT langsung (override)")
	fs.StringVar(&tokenFileOverride, "token-file", "", "file kredensial (token mentah atau FIGMA_TOKEN=…)")
	fs.BoolVar(&rf.jsonOut, "json", false, "output JSON mentah")
	return rf
}

// resolveRef: dari flag --url/--k/--n (posisi argumen boleh jadi URL/fileKey).
func (rf *restFlags) resolveRef(args []string) (figmaRef, error) {
	raw := rf.url
	fk := rf.fileKey
	nid := rf.nodeID
	if len(args) > 0 {
		if raw == "" && strings.Contains(args[0], "figma.com") {
			raw = args[0]
		} else if fk == "" {
			fk = args[0]
		}
		if len(args) > 1 && nid == "" {
			nid = args[1]
		}
	}
	return parseFigmaRef(raw, fk, nid)
}

// ---- figma-cli understand ----

func cmdUnderstand(args []string) error {
	fs := flag.NewFlagSet("understand", flag.ExitOnError)
	rf := newRestFlags(fs)
	fs.IntVar(&rf.depth, "depth", 14, "kedalaman outline")
	fs.Parse(args)

	ref, err := rf.resolveRef(fs.Args())
	if err != nil {
		return err
	}
	token, source, err := resolveToken(rf.account, rf.token)
	if err != nil {
		return err
	}

	if ref.NodeID == "" {
		// overview: halaman + frame top-level
		raw, err := figmaAPI("/files/"+ref.FileKey, token, toURLValues(map[string][]string{"depth": {"2"}}))
		if err != nil {
			return err
		}
		if rf.jsonOut {
			fmt.Println(string(raw))
			return nil
		}
		var f struct {
			Name         string `json:"name"`
			LastModified string `json:"lastModified"`
			Role         string `json:"role"`
			Document     struct {
				Children []struct {
					Name     string `json:"name"`
					ID       string `json:"id"`
					Children []struct {
						Type string `json:"type"`
						Name string `json:"name"`
						ID   string `json:"id"`
					} `json:"children"`
				} `json:"children"`
			} `json:"document"`
		}
		json.Unmarshal(raw, &f)
		fmt.Printf("File: %s (lastModified %s, role %s)\n", f.Name, f.LastModified, f.Role)
		fmt.Println("Tidak ada nodeID — daftar halaman & frame top-level:")
		for _, p := range f.Document.Children {
			fmt.Printf("  PAGE %q id=%s\n", p.Name, p.ID)
			for _, c := range p.Children {
				fmt.Printf("    %s %q id=%s\n", c.Type, c.Name, c.ID)
			}
		}
		fmt.Println("→ panggil `figma-cli understand <url> --n <id>` untuk zoom ke frame.")
		return nil
	}

	raw, err := figmaAPI("/files/"+ref.FileKey+"/nodes", token, toURLValues(map[string][]string{"ids": {ref.NodeID}}))
	if err != nil {
		return err
	}
	var nodes struct {
		Nodes map[string]struct {
			Document map[string]any `json:"document"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &nodes); err != nil {
		return err
	}
	wrap, ok := nodes.Nodes[ref.NodeID]
	if !ok || wrap.Document == nil {
		return fmt.Errorf("node %s tidak ditemukan di file %s", ref.NodeID, ref.FileKey)
	}

	if rf.jsonOut {
		fmt.Println(string(raw))
		return nil
	}

	name := strOr(wrap.Document, "name")
	typ := strOr(wrap.Document, "type")
	fmt.Printf("# %s (%s) — file %s, node %s\n", name, typ, ref.FileKey, ref.NodeID)
	fmt.Println(buildOutline(wrap.Document, rf.depth))

	_ = source
	return nil
}

func strOr(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return "?"
}

// ---- figma-cli file ----

func cmdFile(args []string) error {
	fs := flag.NewFlagSet("file", flag.ExitOnError)
	rf := newRestFlags(fs)
	fs.Parse(args)
	ref, err := rf.resolveRef(fs.Args())
	if err != nil {
		return err
	}
	token, _, err := resolveToken(rf.account, rf.token)
	if err != nil {
		return err
	}
	raw, err := figmaAPI("/files/"+ref.FileKey, token, toURLValues(map[string][]string{"depth": {"1"}}))
	if err != nil {
		return err
	}
	if rf.jsonOut {
		fmt.Println(string(raw))
		return nil
	}
	var f struct {
		Name         string `json:"name"`
		LastModified string `json:"lastModified"`
		Role         string `json:"role"`
		EditorType   string `json:"editorType"`
		Document     struct {
			Children []struct {
				Name     string                            `json:"name"`
				ID       string                            `json:"id"`
				Children []struct{ Type, Name, ID string } `json:"-"`
			} `json:"children"`
		} `json:"document"`
	}
	json.Unmarshal(raw, &f)
	fmt.Printf("File        : %s\n", f.Name)
	fmt.Printf("Last modifik: %s\n", f.LastModified)
	fmt.Printf("Role/Tipe   : %s / %s\n", f.Role, f.EditorType)
	fmt.Printf("Halaman     :\n")
	for _, p := range f.Document.Children {
		fmt.Printf("  • %q id=%s\n", p.Name, p.ID)
	}
	return nil
}

// ---- figma-cli node (dump JSON mentah) ----

func cmdNode(args []string) error {
	fs := flag.NewFlagSet("node", flag.ExitOnError)
	rf := newRestFlags(fs)
	fs.IntVar(&rf.depth, "depth", 4, "kedalaman dump (0=penuh via -depth besar)")
	fs.Parse(args)
	ref, err := rf.resolveRef(fs.Args())
	if err != nil {
		return err
	}
	token, _, err := resolveToken(rf.account, rf.token)
	if err != nil {
		return err
	}
	q := map[string][]string{"ids": {ref.NodeID}}
	if rf.depth > 0 {
		q["depth"] = []string{strconv.Itoa(rf.depth)}
	}
	raw, err := figmaAPI("/files/"+ref.FileKey+"/nodes", token, toURLValues(q))
	if err != nil {
		return err
	}
	// pretty print
	var v any
	json.Unmarshal(raw, &v)
	pretty, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(pretty))
	return nil
}

// ---- figma-cli components ----

func cmdComponents(args []string) error {
	fs := flag.NewFlagSet("components", flag.ExitOnError)
	rf := newRestFlags(fs)
	fs.Parse(args)
	ref, err := rf.resolveRef(fs.Args())
	if err != nil {
		return err
	}
	token, _, err := resolveToken(rf.account, rf.token)
	if err != nil {
		return err
	}
	if rf.jsonOut {
		raw, err := figmaAPI("/files/"+ref.FileKey+"/components", token, nil)
		if err != nil {
			return err
		}
		fmt.Println(string(raw))
		return nil
	}
	for _, ep := range []string{"components", "component_sets", "styles"} {
		raw, err := figmaAPI("/files/"+ref.FileKey+"/"+ep, token, nil)
		if err != nil {
			fmt.Printf("(%s gagal: %v)\n", ep, err)
			continue
		}
		var meta struct {
			Meta json.RawMessage `json:"meta"`
		}
		json.Unmarshal(raw, &meta)
		fmt.Printf("== %s ==\n%s\n", ep, prettyMeta(ep, meta.Meta))
	}
	return nil
}

func prettyMeta(ep string, meta json.RawMessage) string {
	var obj map[string]json.RawMessage
	if json.Unmarshal(meta, &obj) != nil {
		return truncate(string(meta), 2000)
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		switch strings.HasPrefix(k, "component") || k == "components" {
		case true:
			var m map[string]json.RawMessage
			if json.Unmarshal(obj[k], &m) == nil {
				fmt.Fprintf(&b, "%s (%d):\n", k, len(m))
				ids := make([]string, 0, len(m))
				for id := range m {
					ids = append(ids, id)
				}
				sort.Strings(ids)
				for _, id := range ids {
					var c struct {
						Name        string `json:"name"`
						Description string `json:"description"`
					}
					json.Unmarshal(m[id], &c)
					fmt.Fprintf(&b, "  • %q id=%s %s\n", c.Name, id, truncate(c.Description, 60))
				}
				continue
			}
		}
		// styles: array
		var arr []struct {
			Name      string `json:"name"`
			StyleType string `json:"style_type"`
			Key       string `json:"key"`
		}
		if json.Unmarshal(obj[k], &arr) == nil && len(arr) > 0 {
			fmt.Fprintf(&b, "%s (%d):\n", k, len(arr))
			for _, s := range arr {
				fmt.Fprintf(&b, "  • [%s] %q key=%s\n", s.StyleType, s.Name, s.Key)
			}
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", k, truncate(string(obj[k]), 400))
	}
	return b.String()
}

// ---- figma-cli comments ----

func cmdComments(args []string) error {
	fs := flag.NewFlagSet("comments", flag.ExitOnError)
	rf := newRestFlags(fs)
	fs.Parse(args)
	ref, err := rf.resolveRef(fs.Args())
	if err != nil {
		return err
	}
	token, _, err := resolveToken(rf.account, rf.token)
	if err != nil {
		return err
	}
	raw, err := figmaAPI("/files/"+ref.FileKey+"/comments", token, nil)
	if err != nil {
		return err
	}
	if rf.jsonOut {
		fmt.Println(string(raw))
		return nil
	}
	var out struct {
		Comments []struct {
			Message   string `json:"message"`
			CreatedAt string `json:"created_at"`
			User      struct {
				Handle string `json:"handle"`
			} `json:"user"`
			Replies []json.RawMessage `json:"replies"`
		} `json:"comments"`
	}
	json.Unmarshal(raw, &out)
	fmt.Printf("Comments (%d):\n", len(out.Comments))
	for _, c := range out.Comments {
		msg := strings.ReplaceAll(c.Message, "\n", " ")
		if len(msg) > 120 {
			msg = msg[:120] + "…"
		}
		extra := ""
		if len(c.Replies) > 0 {
			extra = fmt.Sprintf(" [%d balasan]", len(c.Replies))
		}
		fmt.Printf("  • [%s] @%s: %s%s\n", strings.Split(c.CreatedAt, "T")[0], c.User.Handle, msg, extra)
	}
	return nil
}

// ---- figma-cli versions ----

func cmdVersions(args []string) error {
	fs := flag.NewFlagSet("versions", flag.ExitOnError)
	rf := newRestFlags(fs)
	fs.Parse(args)
	ref, err := rf.resolveRef(fs.Args())
	if err != nil {
		return err
	}
	token, _, err := resolveToken(rf.account, rf.token)
	if err != nil {
		return err
	}
	raw, err := figmaAPI("/files/"+ref.FileKey+"/versions", token, nil)
	if err != nil {
		return err
	}
	if rf.jsonOut {
		fmt.Println(string(raw))
		return nil
	}
	var out struct {
		Versions []struct {
			CreatedAt   string `json:"created_at"`
			Label       string `json:"label"`
			Description string `json:"description"`
			User        struct {
				Handle string `json:"handle"`
			} `json:"user"`
		} `json:"versions"`
	}
	json.Unmarshal(raw, &out)
	fmt.Printf("Versions (%d, terbaru dulu):\n", len(out.Versions))
	for i, v := range out.Versions {
		if i >= 30 {
			fmt.Printf("  … dan %d lagi\n", len(out.Versions)-30)
			break
		}
		desc := strings.ReplaceAll(v.Description, "\n", " ")
		if len(desc) > 100 {
			desc = desc[:100] + "…"
		}
		label := v.Label
		if label == "" {
			label = "(tanpa label)"
		}
		fmt.Printf("  • [%s] @%s: %s — %s\n", strings.Split(v.CreatedAt, "T")[0], v.User.Handle, label, desc)
	}
	return nil
}

// ---- figma-cli dev-resources ----

func cmdDevResources(args []string) error {
	fs := flag.NewFlagSet("dev-resources", flag.ExitOnError)
	rf := newRestFlags(fs)
	fs.Parse(args)
	ref, err := rf.resolveRef(fs.Args())
	if err != nil {
		return err
	}
	token, _, err := resolveToken(rf.account, rf.token)
	if err != nil {
		return err
	}
	q := map[string][]string{}
	if ref.NodeID != "" {
		q["node_id"] = []string{ref.NodeID}
	}
	raw, err := figmaAPI("/files/"+ref.FileKey+"/dev_resources", token, toURLValues(q))
	if err != nil {
		return err
	}
	if rf.jsonOut {
		fmt.Println(string(raw))
		return nil
	}
	var out struct {
		DevResources []struct {
			Name   string `json:"name"`
			Link   string `json:"link"`
			NodeID string `json:"node_id"`
		} `json:"dev_resources"`
	}
	json.Unmarshal(raw, &out)
	fmt.Printf("Dev resources (%d):\n", len(out.DevResources))
	for _, r := range out.DevResources {
		fmt.Printf("  • %q → %s (node %s)\n", r.Name, r.Link, r.NodeID)
	}
	return nil
}

// ---- figma-cli accounts ----

func cmdAccounts(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: figma-cli accounts add|list|use|remove …")
	}
	af, err := loadAccounts()
	if err != nil {
		return err
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "add":
		if len(rest) < 1 {
			return fmt.Errorf("usage: figma-cli accounts add <alias> [token]")
		}
		alias := rest[0]
		if !validAlias(alias) {
			return fmt.Errorf("alias tidak valid (huruf/angka/-_ , maks 32)")
		}
		token := ""
		if len(rest) > 1 {
			token = rest[1]
		} else {
			fmt.Print("Token PAT (figd_…): ")
			raw, _ := bufioReadLine()
			token = strings.TrimSpace(raw)
		}
		if token == "" {
			return fmt.Errorf("token kosong — dibatalkan")
		}
		af.Accounts[alias] = Account{Token: token}
		if af.Default == "" {
			af.Default = alias
		}
		if err := saveAccounts(af); err != nil {
			return err
		}
		fmt.Printf("✅ akun %q tersimpan (%s)%s\n", alias, maskToken(token), map[bool]string{true: " — jadi default", false: ""}[af.Default == alias])
		return nil
	case "list":
		aliases := make([]string, 0, len(af.Accounts))
		for a := range af.Accounts {
			aliases = append(aliases, a)
		}
		sort.Strings(aliases)
		for _, a := range aliases {
			mark := " "
			if a == af.Default {
				mark = "→"
			}
			fmt.Printf("%s %s  %s\n", mark, a, maskToken(af.Accounts[a].Token))
		}
		if len(aliases) == 0 {
			fmt.Println("(belum ada akun)")
		}
		if os.Getenv("FIGMA_TOKEN") != "" {
			fmt.Println("  (fallback env FIGMA_TOKEN terpasang)")
		}
		return nil
	case "use":
		if len(rest) < 1 || af.Accounts[rest[0]].Token == "" {
			return fmt.Errorf("usage: figma-cli accounts use <alias>")
		}
		af.Default = rest[0]
		if err := saveAccounts(af); err != nil {
			return err
		}
		fmt.Printf("✅ default akun: %s\n", af.Default)
		return nil
	case "remove":
		if len(rest) < 1 {
			return fmt.Errorf("usage: figma-cli accounts remove <alias>")
		}
		delete(af.Accounts, rest[0])
		if af.Default == rest[0] {
			af.Default = ""
		}
		if err := saveAccounts(af); err != nil {
			return err
		}
		fmt.Printf("🗑  akun %q dihapus\n", rest[0])
		return nil
	default:
		return fmt.Errorf("subperintah accounts tidak dikenal: %s", sub)
	}
}

// ---- figma-cli mcp ----

func cmdMCP(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: figma-cli mcp tools|call|status|reset …")
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("mcp "+sub, flag.ExitOnError)
	account := fs.String("account", "default", "alias akun MCP (OAuth terisolasi per alias)")
	var argsJSON string
	var tool string
	var timeoutSec int
	switch sub {
	case "tools":
		fs.Parse(rest)
		data, err := callDaemon("mcp.tools", map[string]any{"account": *account}, 330*time.Second)
		if err != nil {
			return err
		}
		var tools []mcpToolInfo
		if err := json.Unmarshal(data, &tools); err != nil {
			return err
		}
		fmt.Printf("Tools MCP Figma (%d) — akun %q:\n", len(tools), *account)
		for _, t := range tools {
			req := ""
			var schema struct {
				Required []string `json:"required"`
			}
			if json.Unmarshal(t.InputSchema, &schema) == nil && len(schema.Required) > 0 {
				req = "(" + strings.Join(schema.Required, ", ") + ")"
			}
			fmt.Printf("  • %s%s — %s\n", t.Name, req, truncate(strings.ReplaceAll(t.Description, "\n", " "), 110))
		}
		return nil
	case "call":
		fs.StringVar(&tool, "tool", "", "nama tool MCP (mis. get_code)")
		fs.StringVar(&argsJSON, "args", "{}", "arguments JSON")
		fs.IntVar(&timeoutSec, "timeout", 180, "timeout detik")
		fs.Parse(rest)
		if tool == "" {
			return fmt.Errorf("usage: figma-cli mcp call --tool get_code --args '{…}' [url|fileKey] [nodeID]")
		}
		// posisi argumen: url/fileKey + nodeID → dipetakan ke args standar MCP Figma
		margs := map[string]any{}
		if argsJSON != "" && argsJSON != "{}" {
			if err := json.Unmarshal([]byte(argsJSON), &margs); err != nil {
				return fmt.Errorf("--args bukan JSON valid: %w", err)
			}
		}
		pos := fs.Args()
		if len(pos) > 0 {
			ref, err := parseFigmaRef(pos[0], "", "")
			if err != nil {
				return err
			}
			if _, ok := margs["fileKey"]; !ok {
				margs["fileKey"] = ref.FileKey
			}
			if len(pos) > 1 {
				if _, ok := margs["nodeId"]; !ok {
					margs["nodeId"] = ref.NodeID
				}
			} else if ref.NodeID != "" {
				if _, ok := margs["nodeId"]; !ok {
					margs["nodeId"] = ref.NodeID
				}
			}
		}
		data, err := callDaemon("mcp.call", map[string]any{"account": *account, "tool": tool, "args": margs, "timeout": float64(timeoutSec)}, time.Duration(timeoutSec+160)*time.Second)
		if err != nil {
			return err
		}
		var result mcpCallResult
		if err := json.Unmarshal(data, &result); err != nil {
			return fmt.Errorf("hasil tidak bisa di-parse: %w", err)
		}
		if result.IsError {
			for _, c := range result.Content {
				if c.Type == "text" {
					fmt.Fprintln(os.Stderr, "MCP error:", c.Text)
				}
			}
			os.Exit(1)
		}
		for i, c := range result.Content {
			switch c.Type {
			case "text":
				fmt.Println(c.Text)
			case "image":
				path := fmt.Sprintf("figma-mcp-%s-%d.png", tool, time.Now().Unix())
				if err := osWriteFile(path, mustDecodeB64(c.Data)); err != nil {
					return err
				}
				fmt.Printf("🖼  [%d] image %s → %s (%d KB)\n", i, c.MimeType, path, len(c.Data)*3/4/1024)
			default:
				fmt.Printf("[%d] %s: %s\n", i, c.Type, truncate(c.Text, 500))
			}
		}
		return nil
	case "status":
		fs.Parse(rest)
		data, err := callDaemon("mcp.status", map[string]any{"account": *account}, 30*time.Second)
		if err != nil {
			return err
		}
		var st struct {
			Alias      string `json:"alias"`
			Authorized bool   `json:"authorized"`
			Running    bool   `json:"running"`
			ExpiresAt  int64  `json:"expires_at"`
			Expired    bool   `json:"expired"`
		}
		json.Unmarshal(data, &st)
		auth := "belum ada OAuth (panggil `mcp tools` untuk mulai)"
		if st.Authorized {
			auth = fmt.Sprintf("ada (expired=%v, expires_at=%s)", st.Expired, time.Unix(st.ExpiresAt, 0).Format(time.RFC3339))
		}
		fmt.Printf("MCP akun %q: OAuth %s; sesi daemon: %v\n", st.Alias, auth, st.Running)
		return nil
	case "reset":
		fs.Parse(rest)
		_, err := callDaemon("mcp.reset", map[string]any{"account": *account}, 30*time.Second)
		if err != nil {
			return err
		}
		fmt.Printf("🔑 OAuth akun %q dihapus — panggilan MCP berikutnya akan otorisasi ulang.\n", *account)
		return nil
	default:
		return fmt.Errorf("subperintah mcp tidak dikenal: %s", sub)
	}
}

// ---- figma-cli doctor ----

func cmdDoctor() error {
	fmt.Println("== figma-cli doctor ==")
	af, _ := loadAccounts()
	fmt.Printf("accounts.json : %d akun, default=%q\n", len(af.Accounts), af.Default)
	if os.Getenv("FIGMA_TOKEN") != "" {
		fmt.Println("FIGMA_TOKEN   : terpasang")
	} else {
		fmt.Println("FIGMA_TOKEN   : tidak diset")
	}
	fmt.Printf("dir config    : %s\n", cliDir())
	if t, ok := loadTokens("default"); ok {
		fmt.Printf("MCP default   : token ada (expired=%v)\n", time.Now().Unix() >= t.ExpiresAt)
	} else {
		fmt.Println("MCP default   : belum ada token OAuth")
	}
	// cek daemon
	if err := ensureDaemon(); err != nil {
		fmt.Printf("daemon        : GAGAL — %v\n", err)
	} else {
		fmt.Println("daemon        : jalan (auto-start ok)")
	}
	// cek API kalau ada token
	if tok, _, err := resolveToken("", ""); err == nil {
		if _, err := figmaAPI("/me", tok, nil); err != nil {
			fmt.Printf("REST API      : GAGAL — %v\n", err)
		} else {
			fmt.Println("REST API      : OK")
		}
	} else {
		fmt.Println("REST API      : dilewati (tidak ada token)")
	}
	return nil
}
