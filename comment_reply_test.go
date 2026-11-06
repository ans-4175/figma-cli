package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const replyFixtureComments = `{"comments":[
 {"id":"100","parent_id":"","message":"Tolong ganti label tombol","created_at":"2026-11-05T01:00:00Z"},
 {"id":"101","parent_id":"100","message":"sudah diubah sebelumnya","created_at":"2026-11-05T02:00:00Z"},
 {"id":"200","parent_id":"","message":"Root lain","created_at":"2026-11-05T03:00:00Z"}
]}`

type replyServer struct {
	posts    int
	lastBody map[string]any
	lastPath string
	token    string
	status   int
}

func newReplyServer(t *testing.T, rs *replyServer) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.token = r.Header.Get("X-Figma-Token")
		rs.lastPath = r.URL.Path
		switch r.Method {
		case http.MethodGet:
			fmt.Fprint(w, replyFixtureComments)
		case http.MethodPost:
			rs.posts++
			if r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("POST without JSON content type")
			}
			data, _ := io.ReadAll(r.Body)
			rs.lastBody = map[string]any{}
			json.Unmarshal(data, &rs.lastBody)
			if rs.status != 0 {
				w.WriteHeader(rs.status)
				fmt.Fprint(w, `{"status":`+fmt.Sprint(rs.status)+`,"err":"synthetic failure synthetic-pat"}`)
				return
			}
			fmt.Fprint(w, `{"id":"300","parent_id":"100","message":"ignored","created_at":"2026-11-05T04:00:00Z","user":{"handle":"bot"}}`)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
}

func writeMessage(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "reply.txt")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func withAPIBase(t *testing.T, url string) {
	t.Helper()
	previous := apiBase
	apiBase = url
	t.Cleanup(func() { apiBase = previous })
}

func TestCommentReplyPostsOnlyMessageAndRootID(t *testing.T) {
	rs := &replyServer{}
	srv := newReplyServer(t, rs)
	defer srv.Close()
	withAPIBase(t, srv.URL)
	msg := writeMessage(t, "  Sudah diubah: label tombol jadi \"Simpan\".\n")
	out, err := captureRESTOutput(t, func() error {
		return cmdComments([]string{"reply", "--token", "synthetic-pat", "--comment-id", "100", "--message-file", msg, "fixture"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if rs.posts != 1 || rs.lastPath != "/v1/files/fixture/comments" || rs.token != "synthetic-pat" {
		t.Fatalf("posts=%d path=%s", rs.posts, rs.lastPath)
	}
	if len(rs.lastBody) != 2 || rs.lastBody["comment_id"] != "100" || rs.lastBody["message"] != `Sudah diubah: label tombol jadi "Simpan".` {
		t.Fatalf("body must contain only trimmed message + comment_id: %v", rs.lastBody)
	}
	var result map[string]string
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("output not JSON: %q", out)
	}
	if len(result) != 3 || result["id"] != "300" || result["parent_id"] != "100" || result["created_at"] == "" {
		t.Fatalf("unexpected output %v", result)
	}
}

func TestCommentReplyRejectsBeforePost(t *testing.T) {
	rs := &replyServer{}
	srv := newReplyServer(t, rs)
	defer srv.Close()
	withAPIBase(t, srv.URL)
	good := writeMessage(t, "Sudah diubah.")
	cases := []struct {
		name, want string
		args       []string
	}{
		{"missing id", "--comment-id", []string{"--message-file", good, "fixture"}},
		{"bad id", "--comment-id", []string{"--comment-id", "../1", "--message-file", good, "fixture"}},
		{"missing file", "--message-file", []string{"--comment-id", "100", "fixture"}},
		{"empty file", "kosong", []string{"--comment-id", "100", "--message-file", writeMessage(t, " \n\t"), "fixture"}},
		{"binary file", "UTF-8", []string{"--comment-id", "100", "--message-file", writeMessage(t, "a\x00b"), "fixture"}},
		{"reply to reply", "parent_id 100", []string{"--comment-id", "101", "--message-file", good, "fixture"}},
		{"unknown id", "tidak ditemukan", []string{"--comment-id", "999", "--message-file", good, "fixture"}},
		{"duplicate", "identik", []string{"--comment-id", "100", "--message-file", writeMessage(t, "sudah diubah sebelumnya"), "fixture"}},
		{"late flag", "SEBELUM URL", []string{"--comment-id", "100", "--message-file", good, "fixture", "--dry-run"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"reply", "--token", "synthetic-pat"}, tc.args...)
			_, err := captureRESTOutput(t, func() error { return cmdComments(args) })
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
	if rs.posts != 0 {
		t.Fatalf("validation failures must never POST (posts=%d)", rs.posts)
	}
	big := filepath.Join(t.TempDir(), "big.txt")
	os.WriteFile(big, []byte(strings.Repeat("a", maxReplyMessageBytes+1)), 0o600)
	if _, err := readReplyMessage(big); err == nil || !strings.Contains(err.Error(), "terlalu besar") {
		t.Fatalf("oversized message must fail: %v", err)
	}
}

func TestCommentReplyDryRunAndAllowDuplicate(t *testing.T) {
	rs := &replyServer{}
	srv := newReplyServer(t, rs)
	defer srv.Close()
	withAPIBase(t, srv.URL)
	dup := writeMessage(t, "sudah diubah sebelumnya")
	out, err := captureRESTOutput(t, func() error {
		return cmdComments([]string{"reply", "--token", "synthetic-pat", "--dry-run", "--allow-duplicate", "--comment-id", "100", "--message-file", dup, "fixture"})
	})
	if err != nil || rs.posts != 0 || !strings.Contains(out, `"dry_run":true`) {
		t.Fatalf("dry run must not POST: posts=%d out=%q err=%v", rs.posts, out, err)
	}
	if _, err := captureRESTOutput(t, func() error {
		return cmdComments([]string{"reply", "--token", "synthetic-pat", "--allow-duplicate", "--comment-id", "100", "--message-file", dup, "fixture"})
	}); err != nil || rs.posts != 1 {
		t.Fatalf("allow-duplicate should POST once: posts=%d err=%v", rs.posts, err)
	}
}

func TestCommentReplyNeverRetriesAndRedactsToken(t *testing.T) {
	for _, status := range []int{400, 403, 429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			rs := &replyServer{status: status}
			srv := newReplyServer(t, rs)
			defer srv.Close()
			withAPIBase(t, srv.URL)
			msg := writeMessage(t, "Sudah diubah.")
			_, err := captureRESTOutput(t, func() error {
				return cmdComments([]string{"reply", "--token", "synthetic-pat", "--comment-id", "100", "--message-file", msg, "fixture"})
			})
			if err == nil || rs.posts != 1 {
				t.Fatalf("mutation must fail after exactly one POST (posts=%d err=%v)", rs.posts, err)
			}
			if strings.Contains(err.Error(), "synthetic-pat") || !strings.Contains(err.Error(), fmt.Sprint(status)) {
				t.Fatalf("error must report status and redact token: %v", err)
			}
		})
	}
}

func TestCommentReplyDoesNotForwardTokenThroughRedirect(t *testing.T) {
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls++ }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	withAPIBase(t, origin.URL)
	if _, err := figmaPostJSON("/v1/files/fixture/comments", "synthetic-pat", map[string]string{"message": "x", "comment_id": "100"}); err == nil || targetCalls != 0 {
		t.Fatalf("redirect must stop without forwarding the PAT (targetCalls=%d err=%v)", targetCalls, err)
	}
}

func TestCommentsListStillReadOnly(t *testing.T) {
	rs := &replyServer{}
	srv := newReplyServer(t, rs)
	defer srv.Close()
	withAPIBase(t, srv.URL)
	if _, err := captureRESTOutput(t, func() error {
		return cmdComments([]string{"--token", "synthetic-pat", "--json", "fixture"})
	}); err != nil || rs.posts != 0 {
		t.Fatalf("comments listing must remain GET-only (posts=%d err=%v)", rs.posts, err)
	}
}
