// Package ocsrc is a read-only client for the local shared OpenCode service: service discovery, authentication, REST (GET only) and SSE.
// The contract is in docs/contracts/opencode-2.4.1.md. Strictly read-only: it calls no write endpoints (including mark-as-read),
// never starts or restarts the service; credentials are used only in the Authorization header and never appear in error messages.
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

// SSETypes are the event types the sidebar needs (the `?types=` filter; omitting it produces very heavy traffic, and unknown types get a 400).
// session.status and session.idle are excluded: the target version never emits them, so they must not be relied on.
// Queued prompts (inbox) are not handled in the initial version.
var SSETypes = []string{
	"session.created", "session.deleted", "session.renamed",
	"session.execution.started", "session.execution.succeeded",
	"session.execution.failed", "session.execution.interrupted",
	"session.viewed",
	"permission.asked", "permission.replied",
	"form.created", "form.replied", "form.cancelled",
}

// Service is one authenticated connection: read-only REST plus one SSE stream.
type Service interface {
	contract.API
	// Stream connects to /api/event and calls handle for each event; it returns only when the stream ends (disconnect, ctx cancellation, or handle returning an error).
	Stream(ctx context.Context, handle func(contract.Event) error) error
}

// Dialer rediscovers the service on every call (it does not cache port, password or pid).
type Dialer interface {
	Dial(ctx context.Context) (Service, error)
}

// DefaultIdleTimeout is the threshold for detecting a silent disconnect: the service sends a heartbeat (`: heartbeat`) every 15 seconds,
// and 45 consecutive seconds (three heartbeat intervals) without any bytes counts as a disconnect, matching the spec's 45-second target.
const DefaultIdleTimeout = 45 * time.Second

// ErrIdleTimeout means the SSE connection is still open but has been silent for too long (half-open connection); callers should reconnect and reread everything.
var ErrIdleTimeout = errors.New("OpenCode event stream timed out waiting for data")

// DefaultPath is the discovery file of the standard local shared service.
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
	// RequestTimeout caps a single REST request; the default is 10 seconds. SSE is not limited (controlled by ctx).
	RequestTimeout time.Duration
	// IdleTimeout is how long SSE may receive no bytes before it is treated as a silent disconnect; the default is DefaultIdleTimeout.
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

// Reason classifies a connection error into a fixed description for diagnostics. The raw error text may carry paths or response content,
// so diagnostics and the status file use only this fixed text and never output credentials or response bodies.
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

// Dial reads the discovery file and verifies identity with GET /api/info (pid and version must match the discovery file).
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

// HTTPError carries the response status; _tag comes from OpenCode's error body.
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
	// Silence detection: any byte (including heartbeat comment lines) resets the timer; on timeout the connection is cancelled.
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
	return silentOr(silent, io.EOF) // the server ending the stream also counts as a disconnect
}

func silentOr(silent <-chan struct{}, err error) error {
	select {
	case <-silent:
		return ErrIdleTimeout
	default:
		return err
	}
}

// beatReader calls beat (resetting the silence timer) whenever bytes are read.
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
