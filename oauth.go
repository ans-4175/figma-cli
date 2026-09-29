// oauth.go — OAuth 2.0 + PKCE (public client) untuk Figma remote MCP.
//
// Alur: discovery (RFC 9728 protected-resource + auth-server metadata) →
// Dynamic Client Registration (RFC 7591) → authorize di browser dengan PKCE
// (RFC 7636), callback ditangkap localhost server → exchange token →
// simpan per-alias di ~/.figma-cli/mcp-auth/<alias>.json (0600).
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const mcpServerURL = "https://mcp.figma.com/mcp"

type mcpTokens struct {
	ServerURL     string `json:"server_url"`
	ClientID      string `json:"client_id"`
	RedirectURI   string `json:"redirect_uri"`
	AuthEndpoint  string `json:"auth_endpoint"`
	TokenEndpoint string `json:"token_endpoint"`
	Scope         string `json:"scope,omitempty"`
	Resource      string `json:"resource,omitempty"`
	AccessToken   string `json:"access_token"`
	RefreshToken  string `json:"refresh_token,omitempty"`
	ExpiresAt     int64  `json:"expires_at"`
}

// ---- token store per alias ----

func loadTokens(alias string) (*mcpTokens, bool) {
	raw, err := os.ReadFile(tokenFile(alias))
	if err != nil {
		return nil, false
	}
	var t mcpTokens
	if json.Unmarshal(raw, &t) != nil || t.AccessToken == "" {
		return nil, false
	}
	return &t, true
}

func saveTokens(alias string, t *mcpTokens) error {
	if err := os.MkdirAll(mcpAuthDir(), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(tokenFile(alias), data, 0o600)
}

func clearTokens(alias string) {
	os.Remove(tokenFile(alias))
}

// ---- discovery (RFC 9728 + RFC 8414) ----

type protectedResource struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

type authServerMeta struct {
	Issuer                        string   `json:"issuer"`
	RegistrationEndpoint          string   `json:"registration_endpoint"`
	AuthorizationEndpoint         string   `json:"authorization_endpoint"`
	TokenEndpoint                 string   `json:"token_endpoint"`
	ScopesSupported               []string `json:"scopes_supported"`
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported"`
}

func httpGetJSON(rawURL string, hdrs map[string]string) (int, http.Header, []byte, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return 0, nil, nil, err
	}
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, resp.Header, body, nil
}

// discoverAuth mencari auth-server metadata: 401 → WWW-Authenticate →
// protected-resource metadata → authorization_servers → auth-server metadata.
func discoverAuth() (*protectedResource, *authServerMeta, error) {
	// 1) Coba probe: minta resource metadata dari 401 challenge.
	prURL := ""
	u, _ := url.Parse(mcpServerURL)
	wkCandidates := []string{
		u.Scheme + "://" + u.Host + "/.well-known/oauth-protected-resource",
		u.Scheme + "://" + u.Host + "/.well-known/oauth-protected-resource" + u.Path,
	}
	client := &http.Client{Timeout: 30 * time.Second}
	probeReq, _ := http.NewRequest("POST", mcpServerURL, strings.NewReader(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{}}`))
	probeReq.Header.Set("Content-Type", "application/json")
	probeReq.Header.Set("Accept", "application/json, text/event-stream")
	if probeResp, err := client.Do(probeReq); err == nil {
		io.Copy(io.Discard, io.LimitReader(probeResp.Body, 1<<20))
		probeResp.Body.Close()
		if probeResp.StatusCode == 401 {
			if wa := probeResp.Header.Get("WWW-Authenticate"); wa != "" {
				for _, part := range strings.Split(wa, ",") {
					part = strings.TrimSpace(part)
					if strings.HasPrefix(part, "resource_metadata=") {
						prURL = strings.Trim(strings.TrimPrefix(part, "resource_metadata="), `"`)
					}
				}
			}
		}
	}
	var pr protectedResource
	gotPR := false
	tryPR := func(cand string) bool {
		status, _, body, err := httpGetJSON(cand, map[string]string{"Accept": "application/json"})
		if err != nil || status != 200 {
			return false
		}
		if json.Unmarshal(body, &pr) != nil || len(pr.AuthorizationServers) == 0 {
			return false
		}
		gotPR = true
		return true
	}
	if prURL != "" && tryPR(prURL) {
		// dari WWW-Authenticate
	} else {
		for _, cand := range wkCandidates {
			if tryPR(cand) {
				break
			}
		}
	}

	// 2) Pilih authorization server pertama, ambil metadata-nya.
	var asm *authServerMeta
	servers := pr.AuthorizationServers
	if !gotPR {
		servers = []string{u.Scheme + "://" + u.Host}
	}
	for _, server := range servers {
		for _, cand := range []string{
			strings.TrimSuffix(server, "/") + "/.well-known/oauth-authorization-server",
			strings.TrimSuffix(server, "/") + "/.well-known/openid-configuration",
		} {
			status, _, body, err := httpGetJSON(cand, map[string]string{"Accept": "application/json"})
			if err != nil || status != 200 {
				continue
			}
			var m authServerMeta
			if json.Unmarshal(body, &m) == nil && m.AuthorizationEndpoint != "" && m.TokenEndpoint != "" {
				asm = &m
				break
			}
		}
		if asm != nil {
			break
		}
	}
	if asm == nil {
		return nil, nil, fmt.Errorf("tidak menemukan OAuth metadata untuk %s — server mungkin tidak memakai OAuth atau struktur metadata berbeda", mcpServerURL)
	}
	return &pr, asm, nil
}

// ---- PKCE helpers ----

func randomURLB64(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func sha256URLB64(s string) string {
	h := sha256.Sum256([]byte(s))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// ---- DCR (RFC 7591) ----

func registerClient(asm *authServerMeta, redirectURI string) (string, error) {
	if asm.RegistrationEndpoint == "" {
		return "", fmt.Errorf("auth server tidak menyediakan registration_endpoint (DCR)")
	}
	payload, _ := json.Marshal(map[string]any{
		"client_name":                "figma-cli",
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
		"client_public":              true,
	})
	req, _ := http.NewRequest("POST", asm.RegistrationEndpoint, strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("DCR gagal HTTP %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	var out struct {
		ClientID string `json:"client_id"`
	}
	if json.Unmarshal(body, &out) != nil || out.ClientID == "" {
		return "", fmt.Errorf("respons DCR tidak mengandung client_id: %s", truncate(string(body), 300))
	}
	return out.ClientID, nil
}

// ---- authorize via browser + localhost callback ----

func openBrowser(rawURL string) error {
	switch {
	case commandExists("open"):
		return exec.Command("open", rawURL).Start()
	case commandExists("xdg-open"):
		return exec.Command("xdg-open", rawURL).Start()
	default:
		return fmt.Errorf("tidak ada perintah open/xdg-open — buka manual:\n%s", rawURL)
	}
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// authorizeInBrowser membuka browser, menangkap callback di localhost, dan
// mengembalikan (authorization code, code_verifier) yang cocok.
func authorizeInBrowser(asm *authServerMeta, clientID, redirectURI, scope, resource string) (string, string, error) {
	verifier := randomURLB64(64)
	state := randomURLB64(16)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", "", err
	}
	// redirectURI harus cocok dengan port yang benar-benar dipakai.
	if redirectURI == "" {
		redirectURI = fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)
	}
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"code_challenge":        {sha256URLB64(verifier)},
		"code_challenge_method": {"S256"},
	}
	if scope != "" {
		q.Set("scope", scope)
	}
	if resource != "" {
		q.Set("resource", resource)
	}
	authURL := asm.AuthorizationEndpoint + "?" + q.Encode()

	codeCh := make(chan string, 1)
	errCh := make(chan string, 1)
	var once sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("error") != "" {
			msg := r.URL.Query().Get("error") + ": " + r.URL.Query().Get("error_description")
			once.Do(func() { errCh <- msg })
			fmt.Fprint(w, `<html><body><h2>Autorisasi gagal.</h2>Cek terminal figma-cli.</body></html>`)
			return
		}
		code := r.URL.Query().Get("code")
		fmt.Fprint(w, `<html><body><h2>✅ Otorisasi selesai.</h2>Kembali ke terminal.</body></html>`)
		if code != "" {
			once.Do(func() { codeCh <- code })
		}
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	fmt.Println("🔐 Membuka browser untuk otorisasi Figma…")
	fmt.Println("   (kalau browser tidak terbuka, buka manual URL di bawah)")
	fmt.Println()
	fmt.Println(authURL)
	fmt.Println()
	if err := openBrowser(authURL); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}

	select {
	case code := <-codeCh:
		return code, verifier, nil
	case emsg := <-errCh:
		return "", "", fmt.Errorf("otorisasi ditolak: %s", emsg)
	case <-time.After(5 * time.Minute):
		return "", "", fmt.Errorf("timeout menunggu otorisasi browser (5 menit)")
	}
}

// ---- token exchange & refresh ----

type tokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

func tokenPOST(endpoint string, form url.Values) (*tokenResp, error) {
	req, _ := http.NewRequest("POST", endpoint, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("token endpoint HTTP %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	var tr tokenResp
	if json.Unmarshal(body, &tr) != nil || tr.AccessToken == "" {
		return nil, fmt.Errorf("respons token tidak mengandung access_token: %s", truncate(string(body), 300))
	}
	return &tr, nil
}

func exchangeCode(asm *authServerMeta, clientID, redirectURI, code, verifier, resource string) (*tokenResp, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"code_verifier": {verifier},
	}
	if resource != "" {
		form.Set("resource", resource)
	}
	return tokenPOST(asm.TokenEndpoint, form)
}

// refreshAccessToken memperbarui access token; refresh token lama dipakai
// kalau respons tidak mengirim refresh token baru.
func refreshAccessToken(t *mcpTokens) (*mcpTokens, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {t.RefreshToken},
		"client_id":     {t.ClientID},
	}
	if t.Resource != "" {
		form.Set("resource", t.Resource)
	}
	tr, err := tokenPOST(t.TokenEndpoint, form)
	if err != nil {
		return nil, err
	}
	if tr.RefreshToken != "" {
		t.RefreshToken = tr.RefreshToken
	}
	t.AccessToken = tr.AccessToken
	t.ExpiresAt = time.Now().Add(time.Duration(tr.ExpiresIn-60) * time.Second).Unix()
	return t, nil
}

// ---- orkestrasi tingkat tinggi ----

// getValidAccessToken mengembalikan access token valid untuk alias:
// cache → refresh → flow OAuth penuh (buka browser).
func getValidAccessToken(alias string) (*mcpTokens, error) {
	if t, ok := loadTokens(alias); ok {
		if time.Now().Unix() < t.ExpiresAt-60 {
			return t, nil
		}
		if t.RefreshToken != "" {
			nt, rerr := refreshAccessToken(t)
			if rerr == nil {
				saveTokens(alias, nt)
				fmt.Fprintf(os.Stderr, "🔑 token MCP diperbarui (account %q)\n", alias)
				return nt, nil
			}
			fmt.Fprintf(os.Stderr, "⚠️  refresh token gagal (%v) — otorisasi ulang\n", rerr)
		}
	}
	return runOAuthFlow(alias)
}

// runOAuthFlow menjalankan discovery → DCR → browser → exchange → save.
func runOAuthFlow(alias string) (*mcpTokens, error) {
	pr, asm, err := discoverAuth()
	if err != nil {
		return nil, err
	}
	scope := ""
	if pr != nil && len(pr.ScopesSupported) > 0 {
		scope = strings.Join(pr.ScopesSupported, " ")
	} else if len(asm.ScopesSupported) > 0 && len(asm.ScopesSupported) <= 8 {
		scope = strings.Join(asm.ScopesSupported, " ")
	}
	resource := ""
	if pr != nil && pr.Resource != "" {
		resource = pr.Resource
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	clientID := ""
	if old, ok := loadTokens(alias); ok && old.ClientID != "" && old.RedirectURI == redirectURI {
		clientID = old.ClientID // reuse registrasi lama kalau port kebetulan sama
	}
	if clientID == "" {
		clientID, err = registerClient(asm, redirectURI)
		if err != nil {
			return nil, err
		}
	}

	code, verifier, err := authorizeInBrowser(asm, clientID, redirectURI, scope, resource)
	if err != nil {
		return nil, err
	}
	tr, err := exchangeCode(asm, clientID, redirectURI, code, verifier, resource)
	if err != nil {
		return nil, err
	}
	t := &mcpTokens{
		ServerURL:     mcpServerURL,
		ClientID:      clientID,
		RedirectURI:   redirectURI,
		AuthEndpoint:  asm.AuthorizationEndpoint,
		TokenEndpoint: asm.TokenEndpoint,
		Scope:         scope,
		Resource:      resource,
		AccessToken:   tr.AccessToken,
		RefreshToken:  tr.RefreshToken,
		ExpiresAt:     time.Now().Add(time.Duration(tr.ExpiresIn-60) * time.Second).Unix(),
	}
	if err := saveTokens(alias, t); err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "✅ OAuth selesai untuk account %q\n", alias)
	return t, nil
}
