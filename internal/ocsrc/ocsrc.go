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
	"sync"
	"time"

	"github.com/DachiChang/tmux-opencode-status/internal/contract"
)

// SSETypes 是側欄狀態需要的事件型別（`?types=` 過濾；不帶會有極大流量，未知型別會 400）。
// 不含 session.status／session.idle：目標版本從未送出，不得依賴。
// 排隊 prompt（inbox）初版不處理。
var SSETypes = []string{
	"session.created", "session.deleted", "session.renamed",
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

// DefaultIdleTimeout 是無聲斷線的偵測門檻：服務每 15 秒送一次心跳（`: heartbeat`），
// 連續 45 秒（三個心跳間隔）沒有任何位元組即視為斷線，符合 spec 的 45 秒目標。
const DefaultIdleTimeout = 45 * time.Second

// ErrIdleTimeout 表示 SSE 連線仍開著但已無聲過久（半開連線）；呼叫端應重新連線並完整重讀。
var ErrIdleTimeout = errors.New("OpenCode event stream timed out waiting for data")

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
	// IdleTimeout 是 SSE 連續沒收到任何位元組就視為無聲斷線的時間；預設 DefaultIdleTimeout。
	IdleTimeout time.Duration
}

type serviceFile struct {
	Version  string `json:"version"`
	URL      string `json:"url"`
	PID      int    `json:"pid"`
	Password string `json:"password"`
}

var ErrNoService = errors.New("OpenCode service discovery file not found")

var (
	ErrBadDiscovery     = errors.New("incompatible service discovery file format")
	ErrIdentityMismatch = errors.New("service identity does not match the discovery file (the service may have restarted; will rediscover)")
)

// Reason 把連線錯誤歸類成固定的描述，供診斷使用。原始錯誤文字可能夾帶路徑或回應內容，
// 診斷與狀態檔只使用這裡的固定文字，絕不輸出憑證或回應本文。
func Reason(err error) string {
	var h *HTTPError
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrNoService):
		return "service discovery file not found (the OpenCode service is not running yet)"
	case errors.Is(err, ErrBadDiscovery):
		return "incompatible service discovery file format"
	case errors.Is(err, ErrIdentityMismatch):
		return "service identity mismatch (the service may have restarted)"
	case errors.Is(err, ErrIdleTimeout):
		return "event stream timed out waiting for data"
	case errors.As(err, &h):
		return fmt.Sprintf("service returned HTTP %d", h.Status)
	default:
		return "service unreachable"
	}
}

// Dial 讀取探索檔並以 GET /api/info 驗證身分（pid、version 必須與探索檔相符）。
func (s Source) Dial(ctx context.Context) (Service, error) {
	raw, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoService
	}
	if err != nil {
		return nil, fmt.Errorf("read service discovery file: %w", err)
	}
	var f serviceFile
	if err := json.Unmarshal(raw, &f); err != nil || f.URL == "" || f.Password == "" {
		return nil, ErrBadDiscovery
	}
	u, err := url.Parse(f.URL)
	if err != nil || u.Host == "" {
		return nil, errors.New("invalid URL in the service discovery file")
	}
	if h := u.Hostname(); h == "0.0.0.0" || h == "" {
		u.Host = "127.0.0.1:" + u.Port()
	}
	timeout := s.RequestTimeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	idle := s.IdleTimeout
	if idle == 0 {
		idle = DefaultIdleTimeout
	}
	svc := &service{base: strings.TrimRight(u.String(), "/"), pw: f.Password, rest: &http.Client{Timeout: timeout}, stream: &http.Client{}, idle: idle}
	var info struct {
		Version string `json:"version"`
		PID     int    `json:"pid"`
	}
	if err := svc.getJSON(ctx, "/api/info", &info); err != nil {
		return nil, fmt.Errorf("verify service identity: %w", err)
	}
	if info.PID != f.PID || info.Version != f.Version {
		return nil, ErrIdentityMismatch
	}
	return svc, nil
}

type service struct {
	base, pw     string
	rest, stream *http.Client
	idle         time.Duration
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
	// 無聲偵測：任何位元組（含心跳註解行）都重置計時；逾時就取消這條連線。
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	silent := make(chan struct{})
	var once sync.Once
	watchdog := time.AfterFunc(s.idle, func() { once.Do(func() { close(silent) }); cancel() })
	defer watchdog.Stop()
	req, err := s.newReq(ctx, "/api/event?"+q.Encode())
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := s.stream.Do(req)
	if err != nil {
		return silentOr(silent, redact(err, s.pw))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &HTTPError{Status: resp.StatusCode, Path: "/api/event"}
	}
	body := &beatReader{r: resp.Body, beat: func() { watchdog.Reset(s.idle) }}
	if err := contract.StreamSSE(body, handle); err != nil {
		return silentOr(silent, err)
	}
	return silentOr(silent, io.EOF) // 伺服器結束串流也是斷線
}

func silentOr(silent <-chan struct{}, err error) error {
	select {
	case <-silent:
		return ErrIdleTimeout
	default:
		return err
	}
}

// beatReader 在每次讀到位元組時呼叫 beat（重置無聲計時）。
type beatReader struct {
	r    io.Reader
	beat func()
}

func (b *beatReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if n > 0 {
		b.beat()
	}
	return n, err
}
