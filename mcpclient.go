// mcpclient.go — client MCP minimal untuk transport Streamable HTTP.
//
// Cukup yang dipakai figma-cli: initialize, tools/list, tools/call.
// Respons bisa application/json tunggal ATAU text/event-stream (SSE) —
// dua-duanya di-parse. Session dilacak via header Mcp-Session-Id.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

const protoVersion = "2025-06-18"

var errMCPUnauthorized = fmt.Errorf("mcp: unauthorized (401)")

type mcpClient struct {
	serverURL  string
	token      string
	sessionID  string
	httpClient *http.Client
	nextID     atomic.Int64
}

func newMCPClient(serverURL, token string) *mcpClient {
	return &mcpClient{
		serverURL:  serverURL,
		token:      token,
		httpClient: &http.Client{Timeout: 300 * time.Second},
	}
}

// post mengirim satu pesan JSON-RPC dan mengembalikan seluruh payload
// respons (JSON tunggal atau gabungan event SSE).
func (c *mcpClient) post(payload []byte) (*http.Response, []byte, error) {
	return c.postWith(c.httpClient, payload)
}

func (c *mcpClient) postWith(hc *http.Client, payload []byte) (*http.Response, []byte, error) {
	req, err := http.NewRequest("POST", c.serverURL, strings.NewReader(string(payload)))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", protoVersion)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", c.sessionID)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode == 401 {
		resp.Body.Close()
		return resp, nil, errMCPUnauthorized
	}
	if resp.StatusCode == 404 || resp.StatusCode == 405 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return resp, nil, fmt.Errorf("server menolak transport HTTP (status %d): %s", resp.StatusCode, truncate(string(body), 200))
	}
	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "text/event-stream") {
		body, err := readSSEUntilResponse(resp)
		if err != nil {
			resp.Body.Close()
			return resp, nil, err
		}
		resp.Body.Close()
		return resp, body, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	resp.Body.Close()
	if err != nil {
		return resp, nil, err
	}
	return resp, body, nil
}

// readSSEUntilResponse membaca event SSE sampai menemukan JSON-RPC response
// (punya "id" dan bukan notification). Ping dijawab sekilas.
func readSSEUntilResponse(resp *http.Response) ([]byte, error) {
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 64<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		var msg struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(data), &msg) != nil {
			continue
		}
		if msg.ID != nil && (msg.Result != nil || msg.Error != nil) {
			return []byte(data), nil
		}
		// notification/request dari server — v1 abaikan (ping di-SSE-stream
		// tidak wajib dijawab untuk streamable HTTP).
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("stream SSE ditutup tanpa respons")
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// call mengirim request dan mengembalikan field "result" mentah.
func (c *mcpClient) call(method string, params any, timeout time.Duration) (json.RawMessage, http.Header, error) {
	id := c.nextID.Add(1)
	req := rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, nil, err
	}
	hc := c.httpClient
	if timeout > 0 {
		hc = &http.Client{Timeout: timeout}
	}
	resp, body, err := c.postWith(hc, payload)
	if err != nil {
		return nil, nil, err
	}
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.sessionID = sid
	}
	if len(body) == 0 {
		return nil, resp.Header, fmt.Errorf("respons kosong untuk %s", method)
	}
	var out struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, resp.Header, fmt.Errorf("respons bukan JSON-RPC: %s", truncate(string(body), 200))
	}
	if out.Error != nil {
		return nil, resp.Header, fmt.Errorf("MCP error %d: %s", out.Error.Code, out.Error.Message)
	}
	return out.Result, resp.Header, nil
}

// initialize melakukan handshake dan mengirim notification initialized.
func (c *mcpClient) initialize() error {
	params := map[string]any{
		"protocolVersion": protoVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "figma-cli", "version": version},
	}
	result, hdr, err := c.call("initialize", params, 120*time.Second)
	if err != nil {
		return err
	}
	_ = result
	if sid := hdr.Get("Mcp-Session-Id"); sid != "" {
		c.sessionID = sid
	}
	// notification initialized — boleh gagal diam-diam (beberapa server balas 202)
	notif, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	if _, body, err := c.post(notif); err != nil && body == nil {
		// 401 di sini tidak masuk akal setelah initialize sukses; abaikan
	}
	return nil
}

// ---- tipe hasil ----

type mcpToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type mcpContent struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
}

type mcpCallResult struct {
	Content []mcpContent `json:"content"`
	IsError bool         `json:"isError"`
}

func (c *mcpClient) listTools() ([]mcpToolInfo, error) {
	result, _, err := c.call("tools/list", map[string]any{}, 60*time.Second)
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []mcpToolInfo `json:"tools"`
	}
	if err := json.Unmarshal(result, &out); err != nil {
		return nil, err
	}
	return out.Tools, nil
}

func (c *mcpClient) callTool(name string, args map[string]any, timeout time.Duration) (*mcpCallResult, error) {
	result, _, err := c.call("tools/call", map[string]any{"name": name, "arguments": args}, timeout)
	if err != nil {
		return nil, err
	}
	var out mcpCallResult
	if err := json.Unmarshal(result, &out); err != nil {
		return nil, fmt.Errorf("hasil tools/call tidak bisa di-parse: %w", err)
	}
	return &out, nil
}
