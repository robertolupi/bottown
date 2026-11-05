package webshot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"golang.org/x/net/websocket"
)

// cdpConn is a minimal Chrome DevTools Protocol client: one WebSocket to
// the browser, with flattened sessions for its pages. It sends commands and
// waits for their answers, and hands every event to onEvent.
type cdpConn struct {
	ws      *websocket.Conn
	onEvent func(cdpMessage)

	mu      sync.Mutex
	next    int64
	pending map[int64]chan cdpMessage
	err     error // why the read loop ended
	done    chan struct{}
}

// cdpMessage is a command's answer, or an event.
type cdpMessage struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *cdpError       `json:"error,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
}

type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

func (e *cdpError) Error() string {
	if e.Data != "" {
		return fmt.Sprintf("%s (%d): %s", e.Message, e.Code, e.Data)
	}
	return fmt.Sprintf("%s (%d)", e.Message, e.Code)
}

// dialCDP connects to a browser's DevTools WebSocket. onEvent runs on the
// read loop, so it must not call the connection.
func dialCDP(wsURL string, onEvent func(cdpMessage)) (*cdpConn, error) {
	cfg, err := websocket.NewConfig(wsURL, "http://127.0.0.1")
	if err != nil {
		return nil, err
	}
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		return nil, err
	}
	// A full-page screenshot is one large message.
	ws.MaxPayloadBytes = 256 << 20
	if onEvent == nil {
		onEvent = func(cdpMessage) {}
	}
	c := &cdpConn{ws: ws, onEvent: onEvent, pending: map[int64]chan cdpMessage{}, done: make(chan struct{})}
	go c.read()
	return c, nil
}

func (c *cdpConn) read() {
	var err error
	for {
		var m cdpMessage
		if err = websocket.JSON.Receive(c.ws, &m); err != nil {
			break
		}
		if m.ID == 0 {
			c.onEvent(m)
			continue
		}
		c.mu.Lock()
		ch := c.pending[m.ID]
		delete(c.pending, m.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- m
		}
	}
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
	close(c.done)
}

// call sends method to the browser (sessionID "") or to a page's session,
// and decodes its result into result, when that is not nil.
func (c *cdpConn) call(ctx context.Context, sessionID, method string, params, result any) error {
	if params == nil {
		params = struct{}{}
	}
	ch := make(chan cdpMessage, 1)
	c.mu.Lock()
	c.next++
	id := c.next
	c.pending[id] = ch
	c.mu.Unlock()
	msg := map[string]any{"id": id, "method": method, "params": params}
	if sessionID != "" {
		msg["sessionId"] = sessionID
	}
	if err := websocket.JSON.Send(c.ws, msg); err != nil {
		c.forget(id)
		return fmt.Errorf("%s: %w", method, err)
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return fmt.Errorf("%s: %w", method, m.Error)
		}
		if result != nil && len(m.Result) > 0 {
			if err := json.Unmarshal(m.Result, result); err != nil {
				return fmt.Errorf("%s: %w", method, err)
			}
		}
		return nil
	case <-c.done:
		c.mu.Lock()
		err := c.err
		c.mu.Unlock()
		if err == nil {
			err = errors.New("connection closed")
		}
		return fmt.Errorf("%s: the browser went away: %w", method, err)
	case <-ctx.Done():
		c.forget(id)
		return fmt.Errorf("%s: %w", method, ctx.Err())
	}
}

func (c *cdpConn) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *cdpConn) Close() error {
	err := c.ws.Close()
	<-c.done
	return err
}
