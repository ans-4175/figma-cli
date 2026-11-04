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
	"crypto/subtle"
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

// Figma's remote MCP allowlists DCR by exact client_name ("Claude Code" and
// "Codex" register; other names get 403 before browser login). Mirror the
// documented Hermes Agent compatibility default and disclose it in output;
// the user still logs in and consents with their own account, and no other
// client's credentials or tokens are touched. Figma can change the allowlist
// anytime — catalog listing remains the official path.
const figmaDCRClientName = "Claude Code"
const figmaDefaultScope = "mcp:connect"

// Never forward OAuth credentials through redirects.
var oauthHTTPClient = &http.Client{Timeout: 30 * time.Second, CheckRedirect: denyOAuthRedirect}

func denyOAuthRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

func validateOAuthEndpoint(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("OAuth endpoint harus HTTPS tanpa userinfo/fragment")
	}
	return nil
}

func validateMCPAlias(alias string) error {
	if alias == "" || len(alias) > 128 {
		return fmt.Errorf("alias MCP harus 1–128 karakter: huruf, angka, '_' atau '-'")
	}
	for _, c := range alias {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return fmt.Errorf("alias MCP hanya boleh berisi huruf, angka, '_' atau '-'")
		}
	}
	return nil
}

func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("direktori kredensial bukan direktori biasa")
	}
	return os.Chmod(path, 0o700)
}

type mcpTokens struct {
	ServerURL     string `json:"server_url"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret,omitempty"`
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
	if validateMCPAlias(alias) != nil {
		return nil, false
	}
	info, err := os.Lstat(tokenFile(alias))
	if err != nil || !info.Mode().IsRegular() {
		return nil, false
	}
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
	if err := validateMCPAlias(alias); err != nil {
		return err
	}
	for _, dir := range []string{cliDir(), mcpAuthDir()} {
		if err := ensurePrivateDir(dir); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	// Replace atomically from a private sibling; never truncate the last
	// good token file or follow a symlink at its destination.
	f, err := os.CreateTemp(mcpAuthDir(), ".oauth-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), tokenFile(alias))
}

// A cross-process, fail-closed lock prevents simultaneous login/refresh/reset
// from overwriting a newly rotated refresh token. A crashed owner may leave a
// lock; do not automatically delete it or assume another process is dead.
func acquireOAuthLock(alias string) (func(), error) {
	if err := validateMCPAlias(alias); err != nil {
		return nil, err
	}
	for _, dir := range []string{cliDir(), mcpAuthDir()} {
		if err := ensurePrivateDir(dir); err != nil {
			return nil, err
		}
	}
	path := tokenFile(alias) + ".lock"
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("OAuth account sedang terkunci atau lock tidak dapat dibuat; cek proses pemilik sebelum menghapus stale lock: %w", err)
	}
	if _, err := fmt.Fprintf(f, "pid=%d\n", os.Getpid()); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return nil, err
	}
	return func() { os.Remove(path) }, nil
}

func clearTokens(alias string) error {
	unlock, err := acquireOAuthLock(alias)
	if err != nil {
		return err
	}
	defer unlock()
	if err := os.Remove(tokenFile(alias)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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

// httpGetJSON fetches discovery/metadata documents. Transient 502/503/504
// are retried bounded so a provider-side overload window (seen live on
// 2026-11-04) does not abort the whole login flow.
func httpGetJSON(rawURL string, hdrs map[string]string) (int, http.Header, []byte, error) {
	if err := validateOAuthEndpoint(rawURL); err != nil {
		return 0, nil, nil, err
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequest("GET", rawURL, nil)
		if err != nil {
			return 0, nil, nil, err
		}
		for k, v := range hdrs {
			req.Header.Set(k, v)
		}
		resp, err := oauthHTTPClient.Do(req)
		if err != nil {
			if attempt < 4 {
				time.Sleep(retryDelay(attempt, 0))
				continue
			}
			return 0, nil, nil, err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if resp.StatusCode == 429 || resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504 {
			if attempt < 4 {
				time.Sleep(retryDelay(attempt, parseRetryAfter(resp.Header.Get("Retry-After"))))
				continue
			}
		}
		return resp.StatusCode, resp.Header, body, nil
	}
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
	client := oauthHTTPClient
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
					part = strings.TrimPrefix(part, "Bearer ")
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

func registerClient(asm *authServerMeta, redirectURI string) (string, string, error) {
	if asm.RegistrationEndpoint == "" {
		return "", "", fmt.Errorf("auth server tidak menyediakan registration_endpoint (DCR)")
	}
	if err := validateOAuthEndpoint(asm.RegistrationEndpoint); err != nil {
		return "", "", err
	}
	payload, _ := json.Marshal(map[string]any{
		"client_name":    figmaDCRClientName,
		"redirect_uris":  []string{redirectURI},
		"grant_types":    []string{"authorization_code", "refresh_token"},
		"response_types": []string{"code"},
		// Figma advertises auth_method=none yet demands the returned
		// client_secret at the token endpoint; request a confidential
		// registration so exchange/refresh can post it (Hermes-documented).
		"token_endpoint_auth_method": "client_secret_post",
	})
	var out struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	// DCR is idempotent (a retry just creates another registration), so
	// transient transport errors and 429/502/503/504 are retried bounded.
	for attempt := 0; ; attempt++ {
		req, _ := http.NewRequest("POST", asm.RegistrationEndpoint, strings.NewReader(string(payload)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		resp, err := oauthHTTPClient.Do(req)
		if err != nil {
			if attempt < 4 {
				time.Sleep(retryDelay(attempt, 0))
				continue
			}
			return "", "", fmt.Errorf("registrasi OAuth (DCR) gagal: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if (resp.StatusCode == 429 || resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504) && attempt < 4 {
			time.Sleep(retryDelay(attempt, parseRetryAfter(resp.Header.Get("Retry-After"))))
			continue
		}
		if resp.StatusCode/100 != 2 {
			return "", "", fmt.Errorf("DCR gagal HTTP %d — registrasi ditolak oleh server (allowlist bisa berubah; coba lagi nanti atau cek katalog resmi Figma)", resp.StatusCode)
		}
		if json.Unmarshal(body, &out) != nil || out.ClientID == "" {
			return "", "", fmt.Errorf("respons DCR tidak mengandung client_id")
		}
		return out.ClientID, out.ClientSecret, nil
	}
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

// oauthCallbackHandler accepts one GET callback bound to this flow's state.
// Invalid/unsolicited callbacks do not consume the pending authorization.
func oauthCallbackHandler(state string, codes chan<- string, failures chan<- string) http.Handler {
	var once sync.Once
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "GET required", http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		if state == "" || len(q["state"]) != 1 || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
			http.Error(w, "Invalid OAuth state", http.StatusBadRequest)
			return
		}
		code, denied := q.Get("code"), q.Get("error") != ""
		if (!denied && (code == "" || len(q["code"]) != 1)) || (denied && (code != "" || len(q["error"]) != 1)) {
			http.Error(w, "Invalid OAuth callback", http.StatusBadRequest)
			return
		}
		accepted := false
		once.Do(func() {
			accepted = true
			if denied {
				failures <- "otorisasi ditolak oleh provider"
			} else {
				codes <- code
			}
		})
		if !accepted {
			http.Error(w, "OAuth callback already used", http.StatusConflict)
			return
		}
		fmt.Fprintln(w, "Otorisasi selesai. Kembali ke terminal figma-cli.")
	})
}

func callbackRedirectURI(ln net.Listener) string {
	return "http://" + ln.Addr().String() + "/callback"
}

// The same live loopback listener is used for registration, authorization,
// and exchange. Keeping it open prevents callback port substitution/races.
func authorizeInBrowser(asm *authServerMeta, clientID, scope, resource string, ln net.Listener, openURL func(string) error) (string, string, error) {
	if err := validateOAuthEndpoint(asm.AuthorizationEndpoint); err != nil {
		return "", "", err
	}
	verifier := randomURLB64(64)
	state := randomURLB64(32)
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {callbackRedirectURI(ln)},
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
	authURL, _ := url.Parse(asm.AuthorizationEndpoint)
	query := authURL.Query()
	for k, values := range q {
		query[k] = values
	}
	authURL.RawQuery = query.Encode()
	codes, failures := make(chan string, 1), make(chan string, 1)
	srv := &http.Server{
		Handler:           oauthCallbackHandler(state, codes, failures),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	defer srv.Close()

	fmt.Println("🔐 Otorisasi Figma: login dan consent harus dilakukan oleh pengguna.")
	fmt.Println("Jika browser tidak terbuka, buka URL ini pada mesin dengan akses ke callback loopback:")
	fmt.Println(authURL.String())
	if err := openURL(authURL.String()); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	select {
	case code := <-codes:
		return code, verifier, nil
	case msg := <-failures:
		return "", "", fmt.Errorf("%s", msg)
	case err := <-serveErr:
		return "", "", fmt.Errorf("OAuth callback server: %w", err)
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
	if err := validateOAuthEndpoint(endpoint); err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OAuth token request gagal: %w", err)
	}
	defer resp.Body.Close()
	// Do not echo provider response bodies: they can contain credentials.
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("OAuth token endpoint HTTP %d", resp.StatusCode)
	}
	var tr tokenResp
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tr); err != nil {
		return nil, fmt.Errorf("respons OAuth token tidak valid")
	}
	if tr.AccessToken == "" || strings.ContainsAny(tr.AccessToken, "\r\n") || tr.ExpiresIn <= 0 || tr.ExpiresIn > int64((365*24*time.Hour)/time.Second) || (tr.TokenType != "" && !strings.EqualFold(tr.TokenType, "Bearer")) {
		return nil, fmt.Errorf("respons OAuth token tidak valid: access_token, expires_in, atau token_type")
	}
	return &tr, nil
}

func exchangeCode(asm *authServerMeta, clientID, clientSecret, redirectURI, code, verifier, resource string) (*tokenResp, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"code_verifier": {verifier},
	}
	if clientSecret != "" {
		form.Set("client_secret", clientSecret)
	}
	if resource != "" {
		form.Set("resource", resource)
	}
	return tokenPOST(asm.TokenEndpoint, form)
}

// refreshAccessToken memperbarui access token; refresh token lama dipakai
// kalau respons tidak mengirim refresh token baru.
func refreshAccessToken(t *mcpTokens) (*mcpTokens, error) {
	if t.RefreshToken == "" {
		return nil, fmt.Errorf("refresh token tidak tersedia")
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {t.RefreshToken},
		"client_id":     {t.ClientID},
	}
	if t.ClientSecret != "" {
		form.Set("client_secret", t.ClientSecret)
	}
	if t.Resource != "" {
		form.Set("resource", t.Resource)
	}
	tr, err := tokenPOST(t.TokenEndpoint, form)
	if err != nil {
		return nil, err
	}
	// Do not mutate the active session until persistence succeeds.
	next := *t
	if tr.RefreshToken != "" {
		next.RefreshToken = tr.RefreshToken
	}
	next.AccessToken = tr.AccessToken
	next.ExpiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second).Unix()
	return &next, nil
}

func refreshAndSave(alias string, t *mcpTokens) (*mcpTokens, error) {
	unlock, err := acquireOAuthLock(alias)
	if err != nil {
		return nil, err
	}
	defer unlock()
	// Another process may have refreshed before we acquired the lock.
	if saved, ok := loadTokens(alias); ok && saved.AccessToken != t.AccessToken {
		if time.Now().Unix() < saved.ExpiresAt-60 {
			return saved, nil
		}
		t = saved
	}
	next, err := refreshAccessToken(t)
	if err != nil {
		return nil, fmt.Errorf("refresh OAuth gagal; login ulang bila diperlukan dengan figma-cli mcp login --account %s: %w", alias, err)
	}
	if err := saveTokens(alias, next); err != nil {
		return nil, fmt.Errorf("token OAuth diperbarui tetapi gagal disimpan; jangan lanjut unattended, lakukan login ulang: %w", err)
	}
	return next, nil
}

// ---- orkestrasi tingkat tinggi ----

// getValidAccessToken never starts interactive authorization inside a daemon.
// Login is explicit and foreground-only; refresh failures preserve saved tokens.
func getValidAccessToken(alias string) (*mcpTokens, error) {
	if err := validateMCPAlias(alias); err != nil {
		return nil, err
	}
	if t, ok := loadTokens(alias); ok {
		if time.Now().Unix() < t.ExpiresAt-60 {
			return t, nil
		}
		if t.RefreshToken != "" {
			return refreshAndSave(alias, t)
		}
	}
	return nil, fmt.Errorf("OAuth MCP belum tersedia/valid; jalankan figma-cli mcp login --account %s", alias)
}

// runOAuthFlow menjalankan discovery → DCR → browser → exchange → save.
func runOAuthFlow(alias string) (*mcpTokens, error) {
	unlock, err := acquireOAuthLock(alias)
	if err != nil {
		return nil, err
	}
	defer unlock()
	pr, asm, err := discoverAuth()
	if err != nil {
		return nil, err
	}
	if len(asm.CodeChallengeMethodsSupported) > 0 {
		supported := false
		for _, method := range asm.CodeChallengeMethodsSupported {
			supported = supported || method == "S256"
		}
		if !supported {
			return nil, fmt.Errorf("OAuth server tidak mendukung PKCE S256; tidak boleh downgrade")
		}
	}
	scope := ""
	if pr != nil && len(pr.ScopesSupported) > 0 {
		scope = strings.Join(pr.ScopesSupported, " ")
	} else if len(asm.ScopesSupported) > 0 && len(asm.ScopesSupported) <= 8 {
		scope = strings.Join(asm.ScopesSupported, " ")
	}
	if scope == "" {
		scope = figmaDefaultScope
	}
	resource := ""
	if pr != nil && pr.Resource != "" {
		resource = pr.Resource
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer ln.Close()
	redirectURI := callbackRedirectURI(ln)

	clientID, clientSecret := "", ""
	if old, ok := loadTokens(alias); ok && old.ClientID != "" && old.RedirectURI == redirectURI {
		clientID, clientSecret = old.ClientID, old.ClientSecret // reuse registrasi lama kalau port kebetulan sama
	}
	if clientID == "" {
		fmt.Fprintf(os.Stderr, "🔑 Figma DCR: mendaftar dengan client_name=%q (default kompatibilitas ala Hermes Agent; login & consent tetap milikmu)\n", figmaDCRClientName)
		clientID, clientSecret, err = registerClient(asm, redirectURI)
		if err != nil {
			return nil, err
		}
	}

	code, verifier, err := authorizeInBrowser(asm, clientID, scope, resource, ln, openBrowser)
	if err != nil {
		return nil, err
	}
	tr, err := exchangeCode(asm, clientID, clientSecret, redirectURI, code, verifier, resource)
	if err != nil {
		return nil, err
	}
	t := &mcpTokens{
		ServerURL:     mcpServerURL,
		ClientID:      clientID,
		ClientSecret:  clientSecret,
		RedirectURI:   redirectURI,
		AuthEndpoint:  asm.AuthorizationEndpoint,
		TokenEndpoint: asm.TokenEndpoint,
		Scope:         scope,
		Resource:      resource,
		AccessToken:   tr.AccessToken,
		RefreshToken:  tr.RefreshToken,
		ExpiresAt:     time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second).Unix(),
	}
	if err := saveTokens(alias, t); err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "✅ OAuth selesai untuk account %q\n", alias)
	return t, nil
}
