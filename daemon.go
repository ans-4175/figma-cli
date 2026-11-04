// daemon.go — proses `figma-cli serve`: memegang sesi MCP (HTTP + OAuth)
// per-alias dan melayani client lewat unix socket sampai idle 15 menit.
//
// Protokol client <-> daemon: newline-delimited JSON.
//
//	req:  { "id": "…", "op": "mcp.tools|mcp.call|mcp.status|mcp.reset", "params": {…} }
//	resp: { "id": "…", "ok": true|false, "data": …, "error": "…" }
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

const idleTTL = 15 * time.Minute

// ---- sesi MCP per alias ----

type mcpState struct {
	alias string
	toks  *mcpTokens
	cli   *mcpClient
}

type daemon struct {
	requestMu sync.Mutex // serialize session use, refresh rotation, and explicit login/reset
	mu        sync.Mutex
	states    map[string]*mcpState
	lastUsed  time.Time
}

func newDaemon() *daemon {
	return &daemon{states: map[string]*mcpState{}, lastUsed: time.Now()}
}

func (d *daemon) touch() {
	d.mu.Lock()
	d.lastUsed = time.Now()
	d.mu.Unlock()
}

// getSession loads persisted credentials or refreshes an expired session.
// No background browser flow, no silent refresh/save failure.
func (d *daemon) getSession(alias string) (*mcpClient, error) {
	if err := validateMCPAlias(alias); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if st := d.states[alias]; st != nil {
		if time.Now().Unix() >= st.toks.ExpiresAt-60 {
			next, err := refreshAndSave(alias, st.toks)
			if err != nil {
				return nil, err
			}
			st.toks = next
			st.cli.token = next.AccessToken
		}
		return st.cli, nil
	}
	toks, err := getValidAccessToken(alias)
	if err != nil {
		return nil, err
	}
	cli := newMCPClient(mcpServerURL, toks.AccessToken)
	if err := initWithRetry(cli); err != nil {
		if err != errMCPUnauthorized {
			return nil, fmt.Errorf("initialize MCP gagal: %w", err)
		}
		toks, err = refreshAndSave(alias, toks)
		if err != nil {
			return nil, err
		}
		cli = newMCPClient(mcpServerURL, toks.AccessToken)
		if err := cli.initialize(); err != nil {
			return nil, fmt.Errorf("initialize MCP setelah refresh gagal: %w", err)
		}
	}
	d.states[alias] = &mcpState{alias: alias, toks: toks, cli: cli}
	return cli, nil
}

func (d *daemon) resetSession(alias string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := clearTokens(alias); err != nil {
		return err
	}
	delete(d.states, alias)
	return nil
}

// ---- penanganan request client ----

func (d *daemon) handle(req *cliReq) *cliResp {
	d.requestMu.Lock()
	defer d.requestMu.Unlock()
	var params map[string]json.RawMessage
	if err := json.Unmarshal(req.Params, &params); err != nil || params == nil {
		return errResp(req.ID, fmt.Errorf("params harus objek JSON"))
	}
	alias := "default"
	if raw, ok := params["account"]; ok {
		if err := json.Unmarshal(raw, &alias); err != nil {
			return errResp(req.ID, fmt.Errorf("account harus string"))
		}
	}
	if err := validateMCPAlias(alias); err != nil {
		return errResp(req.ID, err)
	}
	params["account"] = mustJSON(alias)
	req.Params = mustJSON(params)
	switch req.Op {
	case "mcp.tools":
		var p struct {
			Account string `json:"account"`
		}
		json.Unmarshal(req.Params, &p)
		cli, err := d.getSession(p.Account)
		if err != nil {
			return errResp(req.ID, err)
		}
		var tools []mcpToolInfo
		fetch := func() error {
			var e error
			tools, e = cli.listTools()
			return e
		}
		err = fetch()
		if err == errMCPUnauthorized {
			var next *mcpClient
			next, err = d.reauth(p.Account)
			if err == nil {
				cli = next
				err = fetch()
			}
		}
		if err != nil && err != errMCPUnauthorized {
			err = retryMCPRead(fetch)
		}
		if err != nil {
			return errResp(req.ID, err)
		}
		return okResp(req.ID, tools)

	case "mcp.call":
		var p struct {
			Account string         `json:"account"`
			Tool    string         `json:"tool"`
			Args    map[string]any `json:"args"`
			Timeout float64        `json:"timeout"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, err)
		}
		cli, err := d.getSession(p.Account)
		if err != nil {
			return errResp(req.ID, err)
		}
		timeout := time.Duration(p.Timeout * float64(time.Second))
		if timeout <= 0 {
			timeout = 180 * time.Second
		}
		var result *mcpCallResult
		callOnce := func() error {
			var e error
			result, e = cli.callTool(p.Tool, p.Args, timeout)
			return e
		}
		err = callOnce()
		if err == errMCPUnauthorized {
			var cli2 *mcpClient
			cli2, err = d.reauth(p.Account)
			if err == nil {
				cli = cli2
				err = callOnce()
			}
		}
		if err != nil && err != errMCPUnauthorized && isMCPReadTool(p.Tool) {
			// Bounded retry for transient read failures only.
			err = retryMCPRead(callOnce)
		}
		if err != nil {
			return errResp(req.ID, err)
		}
		return okResp(req.ID, result)

	case "mcp.status":
		var p struct {
			Account string `json:"account"`
		}
		json.Unmarshal(req.Params, &p)
		alias := p.Account
		if alias == "" {
			alias = "default"
		}
		t, ok := loadTokens(alias)
		out := map[string]any{"alias": alias, "authorized": ok, "running": false}
		if ok {
			out["expires_at"] = t.ExpiresAt
			out["expired"] = time.Now().Unix() >= t.ExpiresAt
		}
		d.mu.Lock()
		_, running := d.states[alias]
		d.mu.Unlock()
		out["running"] = running
		return okResp(req.ID, out)

	case "mcp.invalidate":
		d.mu.Lock()
		delete(d.states, alias)
		d.mu.Unlock()
		return okResp(req.ID, map[string]any{"invalidated": alias})

	case "mcp.reset":
		var p struct {
			Account string `json:"account"`
		}
		json.Unmarshal(req.Params, &p)
		if err := d.resetSession(p.Account); err != nil {
			return errResp(req.ID, err)
		}
		return okResp(req.ID, map[string]any{"reset": p.Account})

	default:
		return errResp(req.ID, fmt.Errorf("op tidak dikenal: %s", req.Op))
	}
}

// reauth refreshes on an unexpected 401, without deleting valid credentials
// or starting an interactive login. A rejected refresh is surfaced to the user.
func (d *daemon) reauth(alias string) (*mcpClient, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	st := d.states[alias]
	if st == nil {
		return nil, fmt.Errorf("sesi MCP tidak tersedia; jalankan mcp login")
	}
	next, err := refreshAndSave(alias, st.toks)
	if err != nil {
		return nil, err
	}
	cli := newMCPClient(st.cli.serverURL, next.AccessToken)
	if err := initWithRetry(cli); err != nil {
		// Rotated tokens are already on disk. Drop the rejected session only.
		delete(d.states, alias)
		return nil, fmt.Errorf("initialize MCP setelah refresh gagal: %w", err)
	}
	d.states[alias] = &mcpState{alias: alias, toks: next, cli: cli}
	return cli, nil
}

// ---- loop utama daemon ----

type cliReq struct {
	ID     string          `json:"id"`
	Op     string          `json:"op"`
	Params json.RawMessage `json:"params"`
}

type cliResp struct {
	ID    string          `json:"id"`
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

func okResp(id string, data any) *cliResp {
	raw, err := json.Marshal(data)
	if err != nil {
		return &cliResp{ID: id, OK: false, Error: err.Error()}
	}
	return &cliResp{ID: id, OK: true, Data: raw}
}

func errResp(id string, err error) *cliResp {
	return &cliResp{ID: id, OK: false, Error: err.Error()}
}

func runDaemon() error {
	// Single instance: kalau socket masih hidup, keluar.
	if conn, err := net.Dial("unix", sockPath()); err == nil {
		conn.Close()
		return fmt.Errorf("daemon sudah berjalan")
	} else if _, statErr := os.Stat(sockPath()); statErr == nil {
		os.Remove(sockPath()) // socket basi
	}
	if err := os.MkdirAll(runtimeDir(), 0o700); err != nil {
		return err
	}
	pid := []byte(fmt.Sprintf("%d\n", os.Getpid()))
	os.WriteFile(pidPath(), pid, 0o600)

	logFile, err := os.OpenFile(logPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	log.SetOutput(logFile)
	log.Printf("daemon start pid=%d", os.Getpid())

	ln, err := net.Listen("unix", sockPath())
	if err != nil {
		return err
	}
	if err := os.Chmod(sockPath(), 0o600); err != nil {
		ln.Close()
		os.Remove(sockPath())
		return err
	}
	defer func() {
		ln.Close()
		os.Remove(sockPath())
		os.Remove(pidPath())
	}()

	d := newDaemon()
	// Idle watchdog.
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				d.mu.Lock()
				idle := time.Since(d.lastUsed)
				d.mu.Unlock()
				if idle > idleTTL {
					log.Printf("idle %s — daemon berhenti", idle.Truncate(time.Second))
					close(done)
					ln.Close()
					return
				}
			case <-done:
				return
			}
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("sinyal keluar — daemon berhenti")
		ln.Close()
		os.Remove(sockPath())
		os.Remove(pidPath())
		os.Exit(0)
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-done:
				return nil
			default:
			}
			return err
		}
		go func(c net.Conn) {
			defer c.Close()
			sc := bufio.NewScanner(c)
			sc.Buffer(make([]byte, 0, 64*1024), 64<<20)
			for sc.Scan() {
				line := sc.Bytes()
				if len(line) == 0 {
					continue
				}
				var req cliReq
				if json.Unmarshal(line, &req) != nil {
					if err := writeDaemonResponse(c, errResp("?", fmt.Errorf("request bukan JSON"))); err != nil {
						return
					}
					continue
				}
				d.touch()
				resp := d.handle(&req)
				if err := writeDaemonResponse(c, resp); err != nil {
					return
				}
			}
		}(conn)
	}
}

func writeDaemonResponse(w io.Writer, resp *cliResp) error {
	return json.NewEncoder(w).Encode(resp)
}

const mcpMaxAttempts = 3

// Bounded backoff for transient MCP read failures; overridden in tests.
var mcpRetryBackoff = 750 * time.Millisecond

// Mutating tools are never auto-retried: the server may have applied the
// change before the failed response, and a replay could duplicate it.
func isMCPReadTool(name string) bool {
	return strings.HasPrefix(name, "get_") || strings.HasPrefix(name, "list_") ||
		strings.HasPrefix(name, "search_") || name == "whoami" ||
		name == "weave_list_tools" || name == "weave_get_tool_inputs" ||
		name == "weave_get_tool_run_output" || name == "weave_find_model"
}

func retryMCPRead(fn func() error) error {
	for attempt := 0; ; attempt++ {
		err := fn()
		var httpErr *mcpHTTPError
		if err == nil || !errors.As(err, &httpErr) ||
			(httpErr.Status != 429 && httpErr.Status != 502 && httpErr.Status != 503 && httpErr.Status != 504) ||
			attempt >= mcpMaxAttempts-1 {
			return err
		}
		time.Sleep(retryDelay(attempt, httpErr.RetryAfter))
	}
}

func initWithRetry(cli *mcpClient) error {
	return retryMCPRead(func() error { return cli.initialize() })
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// cmdDaemon: subperintah `figma-cli daemon status|stop`.
func cmdDaemon(args []string) error {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "status":
		conn, err := net.Dial("unix", sockPath())
		if err != nil {
			fmt.Println("daemon: tidak berjalan")
			return nil
		}
		conn.Close()
		if raw, err := os.ReadFile(pidPath()); err == nil {
			fmt.Printf("daemon: berjalan (pid %s)\n", string(raw))
		} else {
			fmt.Println("daemon: berjalan")
		}
		return nil
	case "stop":
		raw, err := os.ReadFile(pidPath())
		if err != nil {
			fmt.Println("daemon: tidak berjalan")
			return nil
		}
		var pid int
		fmt.Sscanf(string(raw), "%d", &pid)
		if pid <= 0 {
			return fmt.Errorf("pid file tidak valid")
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			return err
		}
		if err := proc.Signal(syscall.SIGTERM); err != nil {
			return err
		}
		fmt.Printf("daemon (pid %d) diminta berhenti\n", pid)
		return nil
	default:
		return fmt.Errorf("usage: figma-cli daemon status|stop")
	}
}

// ensureDaemon dipakai client: dial; kalau gagal spawn `serve` detached lalu
// tunggu socket muncul.
func ensureDaemon() error {
	if c, err := net.Dial("unix", sockPath()); err == nil {
		c.Close()
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(self, "serve")
	detachDaemonProcess(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("gagal auto-start daemon: %w", err)
	}
	go cmd.Wait()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("unix", sockPath()); err == nil {
			c.Close()
			return nil
		}
		// daemon bisa saja langsung mati (single-instance guard) — cek lagi
		if c, err := net.Dial("unix", sockPath()); err == nil {
			c.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("daemon tidak kunjung siap (cek %s)", logPath())
}

// callDaemon mengirim satu request dan menunggu respons dengan id cocok.
func callDaemon(op string, params any, timeout time.Duration) (json.RawMessage, error) {
	if err := ensureDaemon(); err != nil {
		return nil, err
	}
	conn, err := net.Dial("unix", sockPath())
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	id := fmt.Sprintf("req-%d", time.Now().UnixNano())
	payload, _ := json.Marshal(cliReq{ID: id, Op: op, Params: mustJSON(params)})
	conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(append(payload, '\n')); err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), 128<<20)
	for sc.Scan() {
		var resp cliResp
		if json.Unmarshal(sc.Bytes(), &resp) != nil {
			continue
		}
		if resp.ID != id {
			continue
		}
		if !resp.OK {
			return nil, fmt.Errorf("%s", resp.Error)
		}
		return resp.Data, nil
	}
	return nil, fmt.Errorf("daemon tidak membalas (timeout %s)", timeout)
}
