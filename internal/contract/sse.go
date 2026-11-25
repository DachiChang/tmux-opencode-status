package contract

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Event types (GET /api/event, observed on v2.3.0).
const (
	EventServerConnected = "server.connected"
)

// Event is the JSON inside an SSE `data:` line. Only the fields the sidebar needs are flattened.
type Event struct {
	ID      string
	Type    string
	Created int64
	// SessionID comes from data.sessionID; for form.created it is data.form.sessionID.
	SessionID string
	// ParentID appears only in session.created.
	ParentID string
	// RequestID: data.id of permission.asked, data.requestID of permission.replied.
	// FormID: data.form.id of form.created, data.id of form.replied/form.cancelled.
	RequestID string
	FormID    string
	// Idle: data.idle of session.viewed (the idle timestamp that was confirmed as read).
	Idle int64
	// Title: data.title of session.renamed (may also appear in session.created).
	Title string
	// Reason: data.reason of session.execution.interrupted（user | shutdown | superseded | inactivity）。
	Reason string
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
		Reason    string `json:"reason"`
		Title     string `json:"title"`
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
		return Event{}, fmt.Errorf("event is not valid JSON: %w", err)
	}
	if r.Type == "" {
		return Event{}, fmt.Errorf("event is missing type")
	}
	e := Event{ID: r.ID, Type: r.Type, Created: r.Created, SessionID: r.Data.SessionID, ParentID: r.Data.ParentID, Title: r.Data.Title, Idle: r.Data.Idle, Reason: r.Data.Reason}
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

// ParseSSE parses a complete text/event-stream (up to EOF); use StreamSSE for live streams.
func ParseSSE(r io.Reader) ([]Event, error) {
	var events []Event
	err := StreamSSE(r, func(e Event) error {
		events = append(events, e)
		return nil
	})
	return events, err
}

// StreamSSE parses a text/event-stream event by event and passes each to handle, until the stream ends or handle returns an error.
// Line endings may be \n or \r\n; lines starting with ':' are comments (the server sends ": heartbeat" every 15 seconds; it is not an event);
// a blank line ends an event; multiple data lines are joined with \n.
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
