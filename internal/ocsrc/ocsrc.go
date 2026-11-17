// Package ocsrc 是 OpenCode 本機共用服務的唯讀客戶端：服務探索、認證、REST（只用 GET）與 SSE。
// 契約見 docs/contracts/opencode-2.3.0.md。嚴格唯讀：不呼叫任何寫入端點（含標記已讀）、
// 不啟動或重啟服務；憑證只用於 Authorization header，不進入錯誤訊息。
package ocsrc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/DachiChang/tmux-opencode-status/internal/contract"
)

// SSETypes 是側欄狀態需要的事件型別（`?types=` 過濾；不帶會有極大流量，未知型別會 400）。
// 不含 session.status／session.idle：目標版本從未送出，不得依賴。
// 排隊 prompt（inbox）初版不處理。
var SSETypes = []string{
	"session.created", "session.deleted",
	"session.execution.started", "session.execution.succeeded",
	"session.execution.failed", "session.execution.interrupted",
	"session.viewed",
	"permission.asked", "permission.replied",
	"form.created", "form.replied", "form.cancelled",
}

// Service 是一次已通過身分驗證的連線：唯讀 REST 加一條 SSE。
type Service interface {
	contract.API
	// Stream 連上 /api/event 並逐個事件呼叫 handle；串流結束（斷線、ctx 取消、handle 回錯誤）才返回。
	Stream(ctx context.Context, handle func(contract.Event) error) error
}

// Dialer 每次呼叫都重新探索服務（不快取 port／密碼／pid）。
type Dialer interface {
	Dial(ctx context.Context) (Service, error)
}

// DefaultPath 是標準本機共用服務的探索檔。
func DefaultPath() string {
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, _ := os.UserHomeDir()
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "opencode", "service.json")
}

type Source struct {
	Path string
	// RequestTimeout 是單一 REST 請求的上限；預設 10 秒。SSE 不受限（由 ctx 控制）。
	RequestTimeout time.Duration
}

type serviceFile struct {
	Version  string `json:"version"`
	URL      string `json:"url"`
	PID      int    `json:"pid"`
	Password string `json:"password"`
}

var ErrNoService = errors.New("找不到 OpenCode 服務探索檔")

// Dial 讀取探索檔並以 GET /api/info 驗證身分（pid、version 必須與探索檔相符）。
func (s Source) Dial(ctx context.Context) (Service, error) {
	raw, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoService
	}
	if err != nil {
		return nil, fmt.Errorf("讀取服務探索檔：%w", err)
	}
	var f serviceFile
	if err := json.Unmarshal(raw, &f); err != nil || f.URL == "" || f.Password == "" {
		return nil, errors.New("服務探索檔格式不相容")
	}
	u, err := url.Parse(f.URL)
	if err != nil || u.Host == "" {
		return nil, errors.New("服務探索檔的 url 無效")
	}
	if h := u.Hostname(); h == "0.0.0.0" || h == "" {
		u.Host = "127.0.0.1:" + u.Port()
	}
	timeout := s.RequestTimeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	svc := &service{base: strings.TrimRight(u.String(), "/"), pw: f.Password, rest: &http.Client{Timeout: timeout}, stream: &http.Client{}}
	var info struct {
		Version string `json:"version"`
		PID     int    `json:"pid"`
	}
	if err := svc.getJSON(ctx, "/api/info", &info); err != nil {
		return nil, fmt.Errorf("驗證服務身分：%w", err)
	}
	if info.PID != f.PID || info.Version != f.Version {
		return nil, errors.New("服務身分與探索檔不符（可能已重啟，將重新探索）")
	}
	return svc, nil
}

type service struct {
	base, pw     string
	rest, stream *http.Client
}

func (s *service) newReq(ctx context.Context, path string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth("opencode", s.pw)
	return req, nil
}

// HTTPError 帶上回應狀態；_tag 取自 OpenCode 的錯誤本文。
type HTTPError struct {
	Status int
	Tag    string
	Path   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("GET %s：HTTP %d %s", e.Path, e.Status, e.Tag)
}

func (s *service) getJSON(ctx context.Context, path string, out any) error {
	req, err := s.newReq(ctx, path)
	if err != nil {
		return err
	}
	resp, err := s.rest.Do(req)
	if err != nil {
		return redact(err, s.pw)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Tag string `json:"_tag"`
		}
		_ = json.Unmarshal(body, &e)
		return &HTTPError{Status: resp.StatusCode, Tag: e.Tag, Path: strings.SplitN(path, "?", 2)[0]}
	}
	return json.Unmarshal(body, out)
}

func redact(err error, pw string) error {
	return errors.New(strings.ReplaceAll(err.Error(), pw, "***"))
}

func notFound(err error) error {
	var h *HTTPError
	if errors.As(err, &h) && h.Status == http.StatusNotFound && h.Tag == "SessionNotFoundError" {
		return contract.ErrSessionNotFound
	}
	return err
}

func (s *service) Session(id string) (contract.Session, error) {
	var out struct {
		Data contract.Session `json:"data"`
	}
	if err := s.getJSON(context.Background(), "/api/session/"+url.PathEscape(id), &out); err != nil {
		return contract.Session{}, notFound(err)
	}
	return out.Data, nil
}

func (s *service) Children(parentID, cursor string, limit int) ([]contract.Session, string, error) {
	q := url.Values{"parentID": {parentID}, "limit": {strconv.Itoa(limit)}}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	var out struct {
		Data   []contract.Session `json:"data"`
		Cursor struct {
			Next *string `json:"next"`
		} `json:"cursor"`
	}
	if err := s.getJSON(context.Background(), "/api/session?"+q.Encode(), &out); err != nil {
		return nil, "", notFound(err)
	}
	next := ""
	if out.Cursor.Next != nil {
		next = *out.Cursor.Next
	}
	return out.Data, next, nil
}

func (s *service) Permissions(sessionID string) ([]contract.PermissionRequest, error) {
	var out struct {
		Data []contract.PermissionRequest `json:"data"`
	}
	if err := s.getJSON(context.Background(), "/api/session/"+url.PathEscape(sessionID)+"/permission", &out); err != nil {
		return nil, notFound(err)
	}
	return out.Data, nil
}

func (s *service) Forms(sessionID string) ([]contract.Form, error) {
	var out struct {
		Data []contract.Form `json:"data"`
	}
	if err := s.getJSON(context.Background(), "/api/session/"+url.PathEscape(sessionID)+"/form", &out); err != nil {
		return nil, notFound(err)
	}
	return out.Data, nil
}

func (s *service) Active() (map[string]bool, error) {
	var out struct {
		Data map[string]struct {
			Type string `json:"type"`
		} `json:"data"`
	}
	if err := s.getJSON(context.Background(), "/api/session/active", &out); err != nil {
		return nil, err
	}
	running := map[string]bool{}
	for id, v := range out.Data {
		if v.Type == "running" {
			running[id] = true
		}
	}
	return running, nil
}

func (s *service) Stream(ctx context.Context, handle func(contract.Event) error) error {
	q := url.Values{"types": SSETypes}
	req, err := s.newReq(ctx, "/api/event?"+q.Encode())
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := s.stream.Do(req)
	if err != nil {
		return redact(err, s.pw)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &HTTPError{Status: resp.StatusCode, Path: "/api/event"}
	}
	if err := contract.StreamSSE(resp.Body, handle); err != nil {
		return err
	}
	return io.EOF // 伺服器結束串流也是斷線
}
