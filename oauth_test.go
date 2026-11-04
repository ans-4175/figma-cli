package main

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"testing"
	"time"
)

// These tests use synthetic credentials and loopback TLS servers only.
func testOAuthHome(t *testing.T) { t.Helper(); t.Setenv("HOME", t.TempDir()) }
func testOAuthServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	previous := oauthHTTPClient
	oauthHTTPClient = srv.Client()
	oauthHTTPClient.CheckRedirect = denyOAuthRedirect
	t.Cleanup(func() { oauthHTTPClient = previous; srv.Close() })
	return srv
}
func fakeTokens(endpoint string) *mcpTokens {
	return &mcpTokens{ServerURL: mcpServerURL, ClientID: "fake-client", TokenEndpoint: endpoint, AccessToken: "fake-old-access", RefreshToken: "fake-old-refresh", Resource: mcpServerURL, ExpiresAt: time.Now().Add(-time.Hour).Unix()}
}
func saveFakeTokens(t *testing.T, tok *mcpTokens) {
	t.Helper()
	if err := saveTokens("audit", tok); err != nil {
		t.Fatal(err)
	}
}

func TestOAuthFreshProcessLoad(t *testing.T) {
	if os.Getenv("FIGMA_TEST_CHILD") != "1" {
		t.Skip("subprocess helper")
	}
	tok, ok := loadTokens("audit")
	if !ok || tok.AccessToken != "fake-new-access" || tok.RefreshToken != "fake-new-refresh" {
		t.Fatal("persisted credentials not loaded")
	}
}

func TestOAuthRefreshRotatePersistAndRestart(t *testing.T) {
	testOAuthHome(t)
	count := 0
	srv := testOAuthServer(t, func(w http.ResponseWriter, r *http.Request) {
		count++
		if r.Method != "POST" {
			t.Error("expected POST")
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		for k, v := range map[string]string{"grant_type": "refresh_token", "client_id": "fake-client", "refresh_token": "fake-old-refresh", "resource": mcpServerURL} {
			if r.PostForm.Get(k) != v {
				t.Errorf("unexpected %s", k)
			}
		}
		fmt.Fprint(w, `{"access_token":"fake-new-access","refresh_token":"fake-new-refresh","expires_in":3600,"token_type":"Bearer"}`)
	})
	saveFakeTokens(t, fakeTokens(srv.URL))
	tok, err := getValidAccessToken("audit")
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "fake-new-access" || tok.RefreshToken != "fake-new-refresh" || count != 1 {
		t.Fatal("refresh or rotation failed")
	}
	if tok.ExpiresAt < time.Now().Add(59*time.Minute).Unix() {
		t.Fatal("expiration skew applied when storing actual expiry")
	}
	if _, err = getValidAccessToken("audit"); err != nil || count != 1 {
		t.Fatal("valid token not reused")
	}
	for path, mode := range map[string]os.FileMode{tokenFile("audit"): 0600, mcpAuthDir(): 0700} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Fatalf("unsafe mode: %o", info.Mode().Perm())
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestOAuthFreshProcessLoad$")
	cmd.Env = append(os.Environ(), "FIGMA_TEST_CHILD=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fresh process: %v %s", err, out)
	}
}

func TestOAuthRefreshWithoutRotationDoesNotMutateInput(t *testing.T) {
	srv := testOAuthServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"access_token":"fake-new-access","expires_in":3600}`)
	})
	old := fakeTokens(srv.URL)
	refreshed, err := refreshAccessToken(old)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.RefreshToken != "fake-old-refresh" || old.AccessToken != "fake-old-access" {
		t.Fatal("refresh token lost or old state mutated")
	}
}

func TestOAuthRefreshSaveFailureIsReported(t *testing.T) {
	testOAuthHome(t)
	srv := testOAuthServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := os.Remove(tokenFile("audit")); err != nil {
			t.Error(err)
		}
		if err := os.Mkdir(tokenFile("audit"), 0700); err != nil {
			t.Error(err)
		}
		fmt.Fprint(w, `{"access_token":"fake-new-access","refresh_token":"fake-new-refresh","expires_in":3600}`)
	})
	saveFakeTokens(t, fakeTokens(srv.URL))
	if _, err := getValidAccessToken("audit"); err == nil {
		t.Fatal("failed token persistence must not report success")
	}
}

func TestOAuthRejectsInsecureEndpointAndTokenRedirect(t *testing.T) {
	called := false
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer sink.Close()
	if _, err := tokenPOST(sink.URL, url.Values{"refresh_token": {"fake-secret"}}); err == nil {
		t.Fatal("HTTP token endpoint accepted")
	}
	srv := testOAuthServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL, http.StatusTemporaryRedirect)
	})
	if _, err := tokenPOST(srv.URL, url.Values{"refresh_token": {"fake-secret"}}); err == nil {
		t.Fatal("token redirect accepted")
	}
	if called {
		t.Fatal("credentials sent to insecure endpoint or redirect target")
	}
}

func TestOAuthCallbackValidatesStateMethodAndSingleUse(t *testing.T) {
	codes := make(chan string, 1)
	errs := make(chan string, 1)
	handler := oauthCallbackHandler("expected-state", codes, errs)
	for _, query := range []string{"code=evil", "state=wrong&code=evil", "state=wrong&error=access_denied", "state=expected-state"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", "http://127.0.0.1/callback?"+query, nil))
		if rec.Code != 400 {
			t.Fatalf("invalid callback accepted: %s (%d)", query, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("POST", "http://127.0.0.1/callback?state=expected-state&code=evil", nil))
	if rec.Code != 405 {
		t.Fatal("non-GET callback accepted")
	}
	if len(codes) != 0 || len(errs) != 0 {
		t.Fatal("invalid callbacks completed flow")
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "http://127.0.0.1/callback?state=expected-state&code=good", nil))
	if rec.Code != 200 || <-codes != "good" {
		t.Fatal("valid callback rejected")
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "http://127.0.0.1/callback?state=expected-state&code=replay", nil))
	if rec.Code != 409 || len(codes) != 0 {
		t.Fatal("callback replay accepted")
	}
}

func TestOAuthUsesSameCallbackListenerAndS256PKCE(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	expected := "http://" + ln.Addr().String() + "/callback"
	var query url.Values
	opener := func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		query = u.Query()
		if query.Get("redirect_uri") != expected || query.Get("client_id") != "fake-client" || query.Get("code_challenge_method") != "S256" || query.Get("resource") != mcpServerURL {
			return fmt.Errorf("incorrect authorization parameters")
		}
		callback := expected + "?" + url.Values{"state": {query.Get("state")}, "code": {"fake-code"}}.Encode()
		resp, err := http.Get(callback)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		if resp.StatusCode != 200 {
			return fmt.Errorf("callback rejected")
		}
		return nil
	}
	code, verifier, err := authorizeInBrowser(&authServerMeta{AuthorizationEndpoint: "https://www.figma.com/oauth"}, "fake-client", "scope", mcpServerURL, ln, opener)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(verifier))
	if code != "fake-code" || len(verifier) < 43 || query.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(digest[:]) {
		t.Fatal("invalid code or PKCE binding")
	}
}
