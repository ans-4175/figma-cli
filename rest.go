// rest.go — client REST API Figma (api.figma.com), parser URL, dan
// outline builder (port dari extension pi figma).
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// API versions are per endpoint: file reads use v1, folder reads use v2.
var apiBase = "https://api.figma.com"

// Bounded backoff for transient REST failures; overridden in tests.
var restRetryBackoff = 750 * time.Millisecond

func parseRetryAfter(raw string) time.Duration {
	if raw == "" {
		return 0
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && secs >= 0 {
		if time.Duration(secs)*time.Second > 30*time.Second {
			return 30 * time.Second
		}
		return time.Duration(secs) * time.Second
	}
	return 0
}

func retryDelay(attempt int, after time.Duration) time.Duration {
	if after > 0 {
		return after
	}
	return restRetryBackoff << attempt
}

var (
	reFile = regexp.MustCompile(`figma\.com/(?:file|design|proto|board|site)/([A-Za-z0-9]+)`)
	reNode = regexp.MustCompile(`[?&]node-id=([0-9]+)[-:]([0-9]+)`)
)

type figmaRef struct {
	FileKey string
	NodeID  string // "451:36249" — kosong kalau tidak ada
}

// parseFigmaRef menerima URL Figma penuh ATAU fileKey + nodeID terpisah.
func parseFigmaRef(rawURL, fileKey, nodeID string) (figmaRef, error) {
	ref := figmaRef{FileKey: strings.TrimSpace(fileKey), NodeID: strings.TrimSpace(nodeID)}
	if s := strings.TrimSpace(rawURL); s != "" {
		if m := reFile.FindStringSubmatch(s); m != nil {
			ref.FileKey = m[1]
		}
		if n := reNode.FindStringSubmatch(s); n != nil {
			ref.NodeID = n[1] + ":" + n[2]
		}
		if ref.FileKey == "" && !strings.Contains(s, "figma.com") {
			ref.FileKey = s // anggap bare fileKey
		}
		if ref.FileKey == "" {
			return ref, fmt.Errorf("URL Figma tidak dikenali — harapannya https://www.figma.com/design/<fileKey>/…?node-id=…")
		}
	}
	if ref.FileKey == "" {
		return ref, fmt.Errorf("butuh URL Figma atau fileKey")
	}
	ref.NodeID = strings.Replace(ref.NodeID, "-", ":", 1)
	return ref, nil
}

// figmaAPI memanggil endpoint REST (semuanya GET/read) dan mengembalikan
// JSON mentah. Transient 502/503/504 dan 429 di-retry maksimal 3 percobaan
// dengan hormat ke Retry-After; 401/403/404 gagal tanpa retry.
func figmaAPI(path, token string, query url.Values) (json.RawMessage, error) {
	u := apiBase + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	client := &http.Client{
		Timeout: 60 * time.Second,
		// Do not forward X-Figma-Token through an unexpected redirect.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-Figma-Token", token)
		resp, err := client.Do(req)
		if err != nil {
			if attempt < 2 {
				time.Sleep(retryDelay(attempt, 0))
				continue
			}
			return nil, err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		switch {
		case resp.StatusCode == 200:
			return json.RawMessage(body), nil
		case resp.StatusCode == 401:
			return nil, fmt.Errorf("401 Unauthorized — token invalid/expired (figma-cli doctor)")
		case resp.StatusCode == 403:
			message := restErrorMessage(body, token)
			return nil, fmt.Errorf("403 Forbidden (%s) — periksa scope, akses resource, dan endpoint resmi; jangan pindah akun untuk melewati penolakan%s", path, message)
		case resp.StatusCode == 404:
			return nil, fmt.Errorf("404 Not Found — resource tidak ditemukan; ID salah/terhapus atau akses tidak terlihat")
		case resp.StatusCode == 429 || resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504:
			if attempt >= 2 {
				msg := fmt.Sprintf("Figma API %d tetap gagal setelah %d percobaan", resp.StatusCode, attempt+1)
				if lt := resp.Header.Get("X-Figma-Rate-Limit-Type"); lt != "" {
					msg += " (seat-type: " + lt + ")"
				}
				return nil, fmt.Errorf("%s", msg)
			}
			time.Sleep(retryDelay(attempt, parseRetryAfter(resp.Header.Get("Retry-After"))))
			continue
		default:
			return nil, fmt.Errorf("Figma API %d: %s", resp.StatusCode, truncate(string(body), 300))
		}
	}
}

// Retain a bounded server explanation (including missing scopes), never the PAT.
func restErrorMessage(body []byte, token string) string {
	var data map[string]any
	if json.Unmarshal(body, &data) != nil {
		return ""
	}
	for _, key := range []string{"message", "err", "error"} {
		message, _ := data[key].(string)
		if message == "" {
			continue
		}
		if token != "" {
			message = strings.ReplaceAll(message, token, "[REDACTED]")
		}
		return ": " + truncate(strings.Join(strings.Fields(message), " "), 600)
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ---- akses map[string]any yang aman ----

func mStr(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}
func mNum(m map[string]any, k string) float64 {
	if v, ok := m[k].(float64); ok {
		return v
	}
	return 0
}
func mMap(m map[string]any, k string) map[string]any {
	if v, ok := m[k].(map[string]any); ok {
		return v
	}
	return nil
}
func mArr(m map[string]any, k string) []any {
	if v, ok := m[k].([]any); ok {
		return v
	}
	return nil
}

// ---- outline builder ----

func toHex(c map[string]any) string {
	f := func(v any) int {
		n, _ := v.(float64)
		return int(math.Round(n * 255))
	}
	return fmt.Sprintf("#%02x%02x%02x", f(c["r"]), f(c["g"]), f(c["b"]))
}

func fillSummary(node map[string]any) string {
	var parts []string
	for _, f := range mArr(node, "fills") {
		fill, ok := f.(map[string]any)
		if !ok || fill["visible"] == false {
			continue
		}
		t := mStr(fill, "type")
		switch {
		case t == "SOLID":
			if c := mMap(fill, "color"); c != nil {
				s := toHex(c)
				if op, ok := fill["opacity"].(float64); ok && op < 1 {
					s += fmt.Sprintf(" @%d%%", int(math.Round(op*100)))
				}
				parts = append(parts, s)
			}
		case strings.Contains(t, "GRADIENT"):
			parts = append(parts, "gradient")
		case t == "IMAGE":
			parts = append(parts, "image")
		}
		if len(parts) >= 3 {
			break
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " fill=" + strings.Join(parts, ",")
}

type outlineCtx struct {
	b         *strings.Builder
	lines     int
	maxLines  int
	maxDepth  int
	truncated bool
}

func describeNode(node map[string]any, depth int, o *outlineCtx) {
	if o.lines >= o.maxLines {
		o.truncated = true
		return
	}
	o.lines++
	t := mStr(node, "type")
	if t == "" {
		t = "?"
	}
	pad := strings.Repeat("  ", depth)
	size := ""
	if bb := mMap(node, "absoluteBoundingBox"); bb != nil {
		size = fmt.Sprintf(" %d×%d", int(mNum(bb, "width")), int(mNum(bb, "height")))
	}
	extra := fillSummary(node)
	if lm := mStr(node, "layoutMode"); lm != "" {
		dir := "V"
		if lm == "HORIZONTAL" {
			dir = "H"
		}
		p := []int{int(mNum(node, "paddingTop")), int(mNum(node, "paddingRight")), int(mNum(node, "paddingBottom")), int(mNum(node, "paddingLeft"))}
		padStr := ""
		if p[0]|p[1]|p[2]|p[3] != 0 {
			padStr = fmt.Sprintf(" pad(%d,%d,%d,%d)", p[0], p[1], p[2], p[3])
		}
		extra += fmt.Sprintf(" auto[%s] gap=%d%s", dir, int(mNum(node, "itemSpacing")), padStr)
	}
	if r := mNum(node, "cornerRadius"); r > 0 {
		extra += fmt.Sprintf(" r=%d", int(r))
	}
	if t == "TEXT" {
		ch := strings.ReplaceAll(mStr(node, "characters"), "\n", "⏎")
		if len(ch) > 80 {
			ch = ch[:80] + "…"
		}
		st := mMap(node, "style")
		font := ""
		if st != nil {
			fam := mStr(st, "fontFamily")
			if fam == "" {
				if fn := mMap(st, "fontName"); fn != nil {
					fam = mStr(fn, "family")
				}
			}
			sz := mNum(st, "fontSize")
			if fam != "" {
				font = fmt.Sprintf(" %s %gpx", fam, sz)
			}
		}
		extra += " «" + ch + "»" + font
	}
	switch t {
	case "COMPONENT":
		extra += " ⧉ component"
	case "COMPONENT_SET":
		extra += " ⧉ set"
	case "INSTANCE":
		extra += " ⧉ instance"
	}
	hidden := ""
	if node["visible"] == false {
		hidden = " (hidden)"
	}
	fmt.Fprintf(o.b, "%s%s %q%s%s%s\n", pad, t, mStr(node, "name"), size, extra, hidden)
	if depth >= o.maxDepth {
		return
	}
	for _, k := range mArr(node, "children") {
		if kid, ok := k.(map[string]any); ok {
			describeNode(kid, depth+1, o)
		}
	}
}

const outlineMaxLines = 2000

func buildOutline(node map[string]any, maxDepth int) string {
	o := &outlineCtx{b: &strings.Builder{}, maxLines: outlineMaxLines, maxDepth: maxDepth}
	describeNode(node, 0, o)
	if o.truncated {
		fmt.Fprintf(o.b, "… (outline dipotong di %d baris — ambil child node yang lebih dalam)\n", o.maxLines)
	}
	return o.b.String()
}

func osWriteFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}
