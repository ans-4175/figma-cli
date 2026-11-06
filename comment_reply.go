// comment_reply.go — reply to an existing root comment thread (REST POST).
// Deliberately narrow: no top-level comments, no delete, no reactions, and no
// "resolve" (the REST API has no resolve endpoint).
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const maxReplyMessageBytes = 64 << 10

var reCommentID = regexp.MustCompile(`^[A-Za-z0-9:_-]{1,128}$`)

type commentRecord struct {
	ID        string `json:"id"`
	ParentID  string `json:"parent_id"`
	Message   string `json:"message"`
	CreatedAt string `json:"created_at"`
	User      struct {
		Handle string `json:"handle"`
	} `json:"user"`
}

type replyResult struct {
	ID        string `json:"id"`
	ParentID  string `json:"parent_id"`
	CreatedAt string `json:"created_at"`
}

// readReplyMessage loads the reply text from a file so message content never
// travels through argv or shell history.
func readReplyMessage(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("--message-file wajib diisi")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("tidak bisa membaca --message-file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("--message-file harus file biasa")
	}
	if info.Size() > maxReplyMessageBytes {
		return "", fmt.Errorf("--message-file terlalu besar (%d byte; maksimal %d)", info.Size(), maxReplyMessageBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("tidak bisa membaca --message-file: %w", err)
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return "", errors.New("--message-file harus teks UTF-8 tanpa byte NUL")
	}
	message := strings.TrimSpace(string(data))
	if message == "" {
		return "", errors.New("--message-file kosong; balasan butuh isi")
	}
	return message, nil
}

// figmaPostJSON sends exactly one POST. Mutations are never retried: a timeout
// or 5xx may still have created the comment server-side.
func figmaPostJSON(path, token string, payload any) (json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, apiBase+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Figma-Token", token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{
		Timeout: 60 * time.Second,
		// Do not forward X-Figma-Token through an unexpected redirect.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s gagal tanpa respons pasti (%v) — balasan MUNGKIN sudah tersimpan; baca ulang thread sebelum mencoba lagi, jangan kirim ulang membabi buta", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	message := restErrorMessage(raw, token)
	switch {
	case resp.StatusCode == 200 || resp.StatusCode == 201:
		return json.RawMessage(raw), nil
	case resp.StatusCode == 400:
		return nil, fmt.Errorf("400 Bad Request (%s) — Figma menolak isi permintaan; periksa comment-id root dan isi pesan%s", path, message)
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return nil, fmt.Errorf("%d (%s) — periksa kredensial, scope file_comments:write, dan akses komentar file; jangan pindah akun untuk melewati penolakan%s", resp.StatusCode, path, message)
	case resp.StatusCode == 404:
		return nil, fmt.Errorf("404 Not Found (%s) — file atau komentar tidak ditemukan/tidak terlihat%s", path, message)
	case resp.StatusCode == 429:
		return nil, fmt.Errorf("429 Too Many Requests (%s) — hormati Retry-After=%q; TIDAK di-retry otomatis%s", path, resp.Header.Get("Retry-After"), message)
	case resp.StatusCode >= 500:
		return nil, fmt.Errorf("Figma API %d (%s) — status mutasi tidak pasti; baca ulang thread sebelum mencoba lagi%s", resp.StatusCode, path, message)
	default:
		return nil, fmt.Errorf("Figma API %d (%s)%s", resp.StatusCode, path, message)
	}
}

// cmdCommentReply: figma-cli comments reply [flags] <url|fileKey>
func cmdCommentReply(args []string) error {
	fs := flag.NewFlagSet("comments reply", flag.ContinueOnError)
	rf := newRestFlags(fs)
	commentID := fs.String("comment-id", "", "ID komentar ROOT yang dibalas (wajib)")
	messageFile := fs.String("message-file", "", "path file berisi teks balasan (wajib)")
	dryRun := fs.Bool("dry-run", false, "validasi + preflight thread, tanpa POST")
	allowDuplicate := fs.Bool("allow-duplicate", false, "izinkan balasan dengan teks identik yang sudah ada di thread")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if len(fs.Args()) > 1 {
		return fmt.Errorf("argumen tak terduga %q — taruh semua flag SEBELUM URL", fs.Args()[1:])
	}
	ref, err := rf.resolveRef(fs.Args())
	if err != nil {
		return err
	}
	id := strings.TrimSpace(*commentID)
	if !reCommentID.MatchString(id) {
		return errors.New("--comment-id wajib dan harus ID komentar root yang valid; balasan top-level baru tidak didukung")
	}
	message, err := readReplyMessage(*messageFile)
	if err != nil {
		return err
	}
	token, _, err := resolveToken(rf.account, rf.token)
	if err != nil {
		return err
	}

	// Preflight (read): the target must exist and be a ROOT comment.
	raw, err := figmaAPI("/v1/files/"+ref.FileKey+"/comments", token, nil)
	if err != nil {
		return fmt.Errorf("preflight baca thread gagal: %w", err)
	}
	var list struct {
		Comments []commentRecord `json:"comments"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return fmt.Errorf("respons komentar tidak terbaca: %w", err)
	}
	var target *commentRecord
	for i := range list.Comments {
		if list.Comments[i].ID == id {
			target = &list.Comments[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("komentar %s tidak ditemukan di file %s — tidak ada yang dikirim", id, ref.FileKey)
	}
	if target.ParentID != "" {
		return fmt.Errorf("komentar %s adalah balasan (parent_id %s); Figma hanya menerima balasan ke komentar root — gunakan --comment-id %s", id, target.ParentID, target.ParentID)
	}
	if !*allowDuplicate {
		for _, c := range list.Comments {
			if c.ParentID == id && strings.TrimSpace(c.Message) == message {
				return fmt.Errorf("thread %s sudah punya balasan identik (id %s, %s) — tidak dikirim ulang; pakai --allow-duplicate bila memang disengaja", id, c.ID, c.CreatedAt)
			}
		}
	}

	if *dryRun {
		out, _ := json.Marshal(map[string]any{
			"dry_run":    true,
			"file_key":   ref.FileKey,
			"comment_id": id,
			"message":    message,
		})
		fmt.Println(string(out))
		return nil
	}

	resp, err := figmaPostJSON("/v1/files/"+ref.FileKey+"/comments", token, map[string]string{
		"message":    message,
		"comment_id": id,
	})
	if err != nil {
		return err
	}
	var result replyResult
	if err := json.Unmarshal(resp, &result); err != nil || result.ID == "" {
		return fmt.Errorf("POST berhasil tetapi respons tidak berisi id komentar; baca ulang thread %s untuk memastikan", id)
	}
	out, _ := json.Marshal(result)
	fmt.Println(string(out))
	return nil
}
