package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestFigmaAPIRetriesTransientAndFailsFastOn403(t *testing.T) {
	prevBase, prevAPI := restRetryBackoff, apiBase
	defer func() { restRetryBackoff, apiBase = prevBase, prevAPI }()
	restRetryBackoff = time.Millisecond
	counts := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counts[r.URL.Path]++
		switch r.URL.Path {
		case "/flaky":
			if counts["/flaky"] == 1 {
				w.WriteHeader(503)
				fmt.Fprint(w, "upstream overflow")
				return
			}
			fmt.Fprint(w, `{"ok":true}`)
		case "/limited":
			if counts["/limited"] == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(429)
				return
			}
			fmt.Fprint(w, `{"ok":true}`)
		default:
			w.WriteHeader(403)
		}
	}))
	defer srv.Close()
	apiBase = srv.URL
	if _, err := figmaAPI("/flaky", "fake-token", url.Values{}); err != nil {
		t.Fatal(err)
	}
	if counts["/flaky"] != 2 {
		t.Fatalf("503 retry attempts = %d, want 2", counts["/flaky"])
	}
	if _, err := figmaAPI("/limited", "fake-token", url.Values{}); err != nil {
		t.Fatal(err)
	}
	if counts["/limited"] != 2 {
		t.Fatalf("429 retry attempts = %d, want 2", counts["/limited"])
	}
	_, err := figmaAPI("/private", "fake-token", url.Values{})
	if err == nil || counts["/private"] != 1 {
		t.Fatalf("403 must fail fast without retry (attempts=%d, err=%v)", counts["/private"], err)
	}
}

func TestMCPRetriesReadsNeverWrites(t *testing.T) {
	prevBase := mcpRetryBackoff
	defer func() { mcpRetryBackoff = prevBase }()
	mcpRetryBackoff = time.Millisecond
	testOAuthHome(t)
	tok := fakeTokens("https://example.invalid/must-not-refresh")
	tok.ExpiresAt = time.Now().Add(time.Hour).Unix()
	saveFakeTokens(t, tok)
	listAttempts, writeAttempts := 0, 0
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
		switch req.Method {
		case "tools/list":
			listAttempts++
			if listAttempts == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(503)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[]}}`, req.ID)
		case "tools/call":
			writeAttempts++
			w.WriteHeader(503)
		default:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{}}`, req.ID)
		}
	}))
	defer mcp.Close()
	d := newDaemon()
	d.states["audit"] = &mcpState{alias: "audit", toks: tok, cli: newMCPClient(mcp.URL, tok.AccessToken)}
	resp := d.handle(&cliReq{ID: "t1", Op: "mcp.tools", Params: json.RawMessage(`{"account":"audit"}`)})
	if !resp.OK || listAttempts != 2 {
		t.Fatalf("read retry failed: ok=%v attempts=%d err=%s", resp.OK, listAttempts, resp.Error)
	}
	resp = d.handle(&cliReq{ID: "t2", Op: "mcp.call", Params: json.RawMessage(`{"account":"audit","tool":"use_figma","args":{}}`)})
	if resp.OK || writeAttempts != 1 {
		t.Fatalf("write must not be retried: ok=%v attempts=%d", resp.OK, writeAttempts)
	}
}
