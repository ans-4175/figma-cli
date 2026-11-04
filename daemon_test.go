package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDaemonCachedSessionRefresh(t *testing.T) {
	testOAuthHome(t)
	srv := testOAuthServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"access_token":"fake-new-access","refresh_token":"fake-new-refresh","expires_in":3600}`)
	})
	tok := fakeTokens(srv.URL)
	saveFakeTokens(t, tok)
	d := newDaemon()
	d.states["audit"] = &mcpState{alias: "audit", toks: tok, cli: newMCPClient(mcpServerURL, tok.AccessToken)}
	cli, err := d.getSession("audit")
	if err != nil || cli.token != "fake-new-access" {
		t.Fatalf("cached refresh failed: %v", err)
	}
	saved, ok := loadTokens("audit")
	if !ok || saved.AccessToken != "fake-new-access" {
		t.Fatal("refresh not persisted")
	}
}

func TestDaemonCachedRefreshFailureIsReturnedAndPreservesCredentials(t *testing.T) {
	testOAuthHome(t)
	srv := testOAuthServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":"invalid_grant"}`)
	})
	tok := fakeTokens(srv.URL)
	saveFakeTokens(t, tok)
	d := newDaemon()
	d.states["audit"] = &mcpState{alias: "audit", toks: tok, cli: newMCPClient(mcpServerURL, tok.AccessToken)}
	if _, err := d.getSession("audit"); err == nil {
		t.Fatal("expired token returned after failed refresh")
	}
	if saved, ok := loadTokens("audit"); !ok || saved.RefreshToken != tok.RefreshToken {
		t.Fatal("failed refresh deleted saved credentials")
	}
}

func TestDaemon401RefreshesOnceWithoutLoginOrDeletingTokens(t *testing.T) {
	for _, operation := range []string{"mcp.tools", "mcp.call"} {
		t.Run(operation, func(t *testing.T) {
			testOAuthHome(t)
			refreshes, operations := 0, 0
			tokens := testOAuthServer(t, func(w http.ResponseWriter, r *http.Request) {
				refreshes++
				fmt.Fprint(w, `{"access_token":"fake-new-access","refresh_token":"fake-new-refresh","expires_in":3600}`)
			})
			mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					ID     int64  `json:"id"`
					Method string `json:"method"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				if req.Method == "notifications/initialized" {
					w.WriteHeader(202)
					return
				}
				if req.Method == "tools/list" || req.Method == "tools/call" {
					operations++
					if r.Header.Get("Authorization") == "Bearer fake-old-access" {
						w.WriteHeader(401)
						return
					}
				}
				w.Header().Set("Content-Type", "application/json")
				result := `{}`
				if req.Method == "tools/list" {
					result = `{"tools":[]}`
				}
				if req.Method == "tools/call" {
					result = `{"content":[],"isError":false}`
				}
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":%s}`, req.ID, result)
			}))
			defer mcp.Close()
			tok := fakeTokens(tokens.URL)
			tok.ExpiresAt = time.Now().Add(time.Hour).Unix()
			saveFakeTokens(t, tok)
			d := newDaemon()
			d.states["audit"] = &mcpState{alias: "audit", toks: tok, cli: newMCPClient(mcp.URL, tok.AccessToken)}
			params, _ := json.Marshal(map[string]any{"account": "audit", "tool": "fake-test-tool", "args": map[string]any{}})
			resp := d.handle(&cliReq{ID: "test", Op: operation, Params: params})
			if !resp.OK {
				t.Fatal(resp.Error)
			}
			if refreshes != 1 || operations != 2 {
				t.Fatalf("expected one refresh and one retry; got refresh=%d operations=%d", refreshes, operations)
			}
			saved, ok := loadTokens("audit")
			if !ok || saved.RefreshToken != "fake-new-refresh" {
				t.Fatal("rotated credentials not retained")
			}
		})
	}
}

func TestDaemonResponseIsLineFramedWithoutClosingSocket(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	done := make(chan error, 1)
	go func() { done <- writeDaemonResponse(server, okResp("test", map[string]bool{"ok": true})) }()
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(client)
	if !scanner.Scan() {
		t.Fatalf("reply did not arrive before socket close: %v", scanner.Err())
	}
	var resp cliResp
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.ID != "test" {
		t.Fatal("wrong reply")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mustJSON(map[string]string{"account": "audit"})), "\n") {
		t.Fatal("raw JSON helper should remain unframed")
	}
}
