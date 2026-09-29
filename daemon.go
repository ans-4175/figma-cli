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
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
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
	mu       sync.Mutex
	states   map[string]*mcpState
	lastUsed time.Time
}

func newDaemon() *daemon {
	return &daemon{states: map[string]*mcpState{}, lastUsed: time.Now()}
}

func (d *daemon) touch() {
	d.mu.Lock()
	d.lastUsed = time.Now()
	d.mu.Unlock()
}

// getSession mengembalikan sesi siap pakai: cache → refresh token → OAuth baru.
func (d *daemon) getSession(alias string) (*mcpClient, error) {
	d.mu.Lock()
	st, ok := d.states[alias]
	d.mu.Unlock()
	if ok && st != nil {
		// token kadaluarsa? refresh di sini (in-place).
		if time.Now().Unix() >= st.toks.ExpiresAt-60 && st.toks.RefreshToken != "" {
			if nt, err := refreshAccessToken(st.toks); err == nil {
				st.toks = nt
				st.cli.token = nt.AccessToken
				saveTokens(alias, nt)
			}
		}
		return st.cli, nil
	}
	toks, err := getValidAccessToken(alias) // bisa buka browser
	if err != nil {
		return nil, err
	}
	cli := newMCPClient(mcpServerURL, toks.AccessToken)
	if err := cli.initialize(); err != nil {
		if err == errMCPUnauthorized && toks.RefreshToken != "" {
			// token baru saja invalid — refresh lalu coba sekali lagi
			if nt, rerr := refreshAccessToken(toks); rerr == nil {
				saveTokens(alias, nt)
				cli = newMCPClient(mcpServerURL, nt.AccessToken)
				if err2 := cli.initialize(); err2 == nil {
					toks = nt
					err = nil
				}
			}
		}
		if err != nil {
			return nil, fmt.Errorf("initialize MCP gagal: %w", err)
		}
	}
	st = &mcpState{alias: alias, toks: toks, cli: cli}
	d.mu.Lock()
	d.states[alias] = st
	d.mu.Unlock()
	return cli, nil
}

func (d *daemon) resetSession(alias string) {
	d.mu.Lock()
	delete(d.states, alias)
	d.mu.Unlock()
	clearTokens(alias)
}

// ---- penanganan request client ----

func (d *daemon) handle(req *cliReq) *cliResp {
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
		tools, err := cli.listTools()
		if err == errMCPUnauthorized {
			if cli2, rerr := d.reauthAndRetry(p.Account, func(c *mcpClient) error { _, err := c.listTools(); return err }); rerr == nil {
				tools, err = cli2.listTools()
			}
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
		result, err := cli.callTool(p.Tool, p.Args, timeout)
		if err == errMCPUnauthorized {
			var cli2 *mcpClient
			cli2, err = d.reauth(p.Account)
			if err == nil {
				result, err = cli2.callTool(p.Tool, p.Args, timeout)
			}
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

	case "mcp.reset":
		var p struct {
			Account string `json:"account"`
		}
		json.Unmarshal(req.Params, &p)
		d.resetSession(p.Account)
		return okResp(req.ID, map[string]any{"reset": p.Account})

	default:
		return errResp(req.ID, fmt.Errorf("op tidak dikenal: %s", req.Op))
	}
}

// reauth memaksa OAuth ulang untuk alias lalu mengembalikan sesi baru.
func (d *daemon) reauth(alias string) (*mcpClient, error) {
	d.resetSession(alias)
	if _, err := d.getSession(alias); err != nil {
		return nil, err
	}
	d.mu.Lock()
	st := d.states[alias]
	d.mu.Unlock()
	return st.cli, nil
}

func (d *daemon) reauthAndRetry(alias string, probe func(*mcpClient) error) (*mcpClient, error) {
	cli, err := d.reauth(alias)
	if err != nil {
		return nil, err
	}
	if err := probe(cli); err != nil {
		return nil, err
	}
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
					c.Write(mustJSON(errResp("?", fmt.Errorf("request bukan JSON"))))
					continue
				}
				d.touch()
				resp := d.handle(&req)
				c.Write(mustJSON(resp))
			}
		}(conn)
	}
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
