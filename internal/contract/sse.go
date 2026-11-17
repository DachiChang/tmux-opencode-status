package contract

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// 事件型別（GET /api/event，v2.3.0 實測）。
const (
	EventServerConnected = "server.connected"
)

// Event 是 SSE `data:` 行內的 JSON。只攤平側欄需要的欄位。
type Event struct {
	ID      string
	Type    string
	Created int64
	// SessionID 來自 data.sessionID；form.created 則取 data.form.sessionID。
	SessionID string
	// ParentID 只出現在 session.created。
	ParentID string
	// RequestID：permission.asked 的 data.id、permission.replied 的 data.requestID。
	// FormID：form.created 的 data.form.id、form.replied／form.cancelled 的 data.id。
	RequestID string
	FormID    string
	// Idle：session.viewed 的 data.idle（被確認已讀的 idle 時間戳）。
	Idle int64
}

type rawEvent struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Created int64  `json:"created"`
	Data    struct {
		SessionID string `json:"sessionID"`
		ParentID  string `json:"parentID"`
		ID        string `json:"id"`
		RequestID string `json:"requestID"`
		Idle      int64  `json:"idle"`
		Form      *struct {
			ID        string `json:"id"`
			SessionID string `json:"sessionID"`
		} `json:"form"`
	} `json:"data"`
}

func decodeEvent(payload string) (Event, error) {
	var r rawEvent
	if err := json.Unmarshal([]byte(payload), &r); err != nil {
		return Event{}, fmt.Errorf("事件不是有效 JSON：%w", err)
	}
	if r.Type == "" {
		return Event{}, fmt.Errorf("事件缺少 type")
	}
	e := Event{ID: r.ID, Type: r.Type, Created: r.Created, SessionID: r.Data.SessionID, ParentID: r.Data.ParentID, Idle: r.Data.Idle}
	switch r.Type {
	case "permission.asked":
		e.RequestID = r.Data.ID
	case "permission.replied":
		e.RequestID = r.Data.RequestID
	case "form.created":
		if r.Data.Form != nil {
			e.FormID, e.SessionID = r.Data.Form.ID, r.Data.Form.SessionID
		}
	case "form.replied", "form.cancelled":
		e.FormID = r.Data.ID
	}
	return e, nil
}

// ParseSSE 解析完整的 text/event-stream（到 EOF 為止）；即時串流請用 StreamSSE。
func ParseSSE(r io.Reader) ([]Event, error) {
	var events []Event
	err := StreamSSE(r, func(e Event) error {
		events = append(events, e)
		return nil
	})
	return events, err
}

// StreamSSE 逐個解析 text/event-stream 並交給 handle，直到串流結束或 handle 回傳錯誤。
// 行尾可為 \n 或 \r\n；以 ':' 開頭的行是註解（伺服器每 15 秒送 ": heartbeat"，不是事件）；
// 空行結束一個事件；多行 data 以 \n 串接。
func StreamSSE(r io.Reader, handle func(Event) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var data []string
	flush := func() error {
		if len(data) == 0 {
			return nil
		}
		e, err := decodeEvent(strings.Join(data, "\n"))
		data = nil
		if err != nil {
			return err
		}
		return handle(e)
	}
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		switch {
		case line == "":
			if err := flush(); err != nil {
				return err
			}
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return flush()
}
