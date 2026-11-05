package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func captureRESTOutput(t *testing.T, run func() error) (string, error) {
	t.Helper()
	previous := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer func() { os.Stdout = previous }()
	os.Stdout = w
	callErr := run()
	w.Close()
	os.Stdout = previous
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(data), callErr
}

func TestParseCatalogID(t *testing.T) {
	tests := []struct {
		raw, kind, want string
	}{
		{"250718366", "folder", "250718366"},
		{"https://www.figma.com/files/team/1389434445689513013/folder/250718366?fuid=1684147274778036801", "folder", "250718366"},
		{"https://figma.com/files/project/250718366", "folder", "250718366"},
		{"https://figma.com/project/250718366/Name", "folder", "250718366"},
		{"https://figma.com/files/123/team/456", "team", "456"},
		{"https://figma.com/files/team/456/folder/789", "team", "456"},
		{"https://figma.com.evil.invalid/files/folder/123", "folder", ""},
		{"https://evil.invalid/figma.com/files/folder/123", "folder", ""},
		{"https://user@figma.com/files/folder/123", "folder", ""},
		{"https://figma.com/design/abc/Name", "folder", ""},
		{"https://figma.com/files/team/123", "folder", ""},
		{"https://figma.com/files/folder/abc", "folder", ""},
		{"123/../../me", "folder", ""},
	}
	for _, tt := range tests {
		t.Run(tt.raw+"-"+tt.kind, func(t *testing.T) {
			got, err := parseCatalogID(tt.raw, tt.kind)
			if (tt.want == "") != (err != nil) || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestCatalogUsesOnlyCurrentV2Routes(t *testing.T) {
	previous := apiBase
	defer func() { apiBase = previous }()
	var path, query string
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		path, query = r.URL.Path, r.URL.RawQuery
		if r.Method != "GET" || r.Header.Get("X-Figma-Token") != "synthetic-pat" {
			t.Errorf("unexpected method or token")
		}
		fmt.Fprint(w, `{"id":"123","name":"Catalog","file_count":1,"files":[{"key":"example","name":"GEM"}],"folders":[{"id":"456","name":"Child"}]}`)
	}))
	defer srv.Close()
	apiBase = srv.URL
	for _, tt := range []struct {
		group, sub, extra, path, query string
	}{
		{"folder", "files", "--branch-data", "/v2/folders/123/files", "branch_data=true"},
		{"folder", "folders", "", "/v2/folders/123/folders", ""},
		{"folder", "meta", "", "/v2/folders/123/meta", ""},
		{"team", "folders", "", "/v2/teams/123/folders", ""},
	} {
		t.Run(tt.group+"-"+tt.sub, func(t *testing.T) {
			args := []string{tt.sub, "--token", "synthetic-pat", "--json"}
			if tt.extra != "" {
				args = append(args, tt.extra)
			}
			args = append(args, "123")
			before := calls
			output, err := captureRESTOutput(t, func() error { return cmdCatalog(tt.group, args) })
			if err != nil || !json.Valid([]byte(output)) || path != tt.path || query != tt.query || calls != before+1 {
				t.Fatalf("path=%s query=%s calls=%d output=%q err=%v", path, query, calls-before, output, err)
			}
		})
	}
	before := calls
	if err := cmdCatalog("folder", []string{"files", "123", "--json"}); err == nil || calls != before {
		t.Fatal("late flags must fail before any network request")
	}
}

func TestFileCommandsRetainDocumentedV1Routes(t *testing.T) {
	previous := apiBase
	defer func() { apiBase = previous }()
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		fmt.Fprint(w, `{"name":"Example","document":{"children":[]},"nodes":{"1:2":{"document":{"id":"1:2","name":"Example","type":"FRAME"}}},"comments":[],"versions":[],"dev_resources":[],"meta":{"components":[]}}`)
	}))
	defer srv.Close()
	apiBase = srv.URL
	for _, tt := range []struct {
		name, suffix string
		run          func([]string) error
		args         []string
	}{
		{"file", "", cmdFile, nil},
		{"understand", "", cmdUnderstand, nil},
		{"node", "/nodes", cmdNode, []string{"--n", "1:2"}},
		{"components", "/components", cmdComponents, nil},
		{"comments", "/comments", cmdComments, nil},
		{"versions", "/versions", cmdVersions, nil},
		{"dev-resources", "/dev_resources", cmdDevResources, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"--token", "synthetic-pat", "--json"}, tt.args...)
			args = append(args, "fixture")
			_, err := captureRESTOutput(t, func() error { return tt.run(args) })
			if err != nil || path != "/v1/files/fixture"+tt.suffix {
				t.Fatalf("path=%s err=%v", path, err)
			}
		})
	}
	if _, err := figmaAPI("/v1/me", "synthetic-pat", nil); err != nil || path != "/v1/me" {
		t.Fatalf("doctor route=%s err=%v", path, err)
	}
}

func TestRESTDoesNotForwardPATThroughRedirect(t *testing.T) {
	previous := apiBase
	defer func() { apiBase = previous }()
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls++
		fmt.Fprint(w, "{}")
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer origin.Close()
	apiBase = origin.URL
	_, err := figmaAPI("/v1/me", "synthetic-pat", nil)
	if err == nil || !strings.Contains(err.Error(), "302") || targetCalls != 0 {
		t.Fatalf("redirect must stop without forwarding the PAT: targetCalls=%d err=%v", targetCalls, err)
	}
}

func TestREST403ReportsScopeWithoutTokenOrFallback(t *testing.T) {
	previous := apiBase
	defer func() { apiBase = previous }()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"Requires folders:read; synthetic-secret-pat"}`)
	}))
	defer srv.Close()
	apiBase = srv.URL
	_, err := captureRESTOutput(t, func() error {
		return cmdCatalog("folder", []string{"files", "--token", "synthetic-secret-pat", "123"})
	})
	if err == nil || calls != 1 || !strings.Contains(err.Error(), "folders:read") ||
		!strings.Contains(err.Error(), "/v2/folders/123/files") ||
		strings.Contains(err.Error(), "synthetic-secret-pat") ||
		strings.Contains(err.Error(), "coba --account") {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	_, err = captureRESTOutput(t, func() error {
		return cmdComponents([]string{"--token", "synthetic-secret-pat", "fixture"})
	})
	if err == nil || calls != 2 {
		t.Fatalf("component failure must return an error and stop (calls=%d err=%v)", calls, err)
	}
}
