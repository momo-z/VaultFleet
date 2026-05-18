package ws

import (
	"errors"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var ErrNilConn = errors.New("websocket connection is nil")

type SafeConn struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func NewSafeConn(conn *websocket.Conn) *SafeConn {
	return &SafeConn{conn: conn}
}

func (c *SafeConn) WriteJSON(v interface{}) error {
	if c == nil || c.conn == nil {
		return ErrNilConn
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	return c.conn.WriteJSON(v)
}

func (c *SafeConn) ReadJSON(v interface{}) error {
	if c == nil || c.conn == nil {
		return ErrNilConn
	}
	return c.conn.ReadJSON(v)
}

func (c *SafeConn) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	return c.conn.Close()
}
