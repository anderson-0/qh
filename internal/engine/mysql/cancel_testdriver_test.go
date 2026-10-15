package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"sync"
	"testing"
)

// scriptConnector uses database/sql's real reservation, waiting and discard
// behavior. Every SQL operation must be explicitly accepted by the script.
type scriptConnector struct {
	rowFactory func(string, [][]driver.Value) driver.Rows
	mu         sync.Mutex
	next       int
	events     []string
	handle     func(context.Context, int, string, []driver.NamedValue) ([][]driver.Value, error)
	connect    func(context.Context) error
}

func (s *scriptConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if s.connect != nil {
		if err := s.connect(ctx); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	return &scriptConn{s: s, id: s.next}, nil
}
func (s *scriptConnector) Driver() driver.Driver { return scriptDriver{} }

type scriptDriver struct{}

func (scriptDriver) Open(string) (driver.Conn, error) { return nil, fmt.Errorf("use connector") }

type scriptConn struct {
	s  *scriptConnector
	id int
}

func (c *scriptConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("unexpected prepare")
}
func (c *scriptConn) Begin() (driver.Tx, error) {
	return nil, fmt.Errorf("unexpected driver transaction")
}
func (c *scriptConn) Close() error {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	c.s.events = append(c.s.events, fmt.Sprintf("close:%d", c.id))
	return nil
}
func (c *scriptConn) run(ctx context.Context, q string, args []driver.NamedValue) ([][]driver.Value, error) {
	c.s.mu.Lock()
	c.s.events = append(c.s.events, fmt.Sprintf("%d:%s", c.id, q))
	c.s.mu.Unlock()
	if c.s.handle == nil {
		return nil, fmt.Errorf("unexpected SQL")
	}
	return c.s.handle(ctx, c.id, q, args)
}
func (c *scriptConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	_, err := c.run(ctx, q, args)
	return driver.RowsAffected(1), err
}
func (c *scriptConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	v, err := c.run(ctx, q, args)
	if err != nil {
		return nil, err
	}
	if c.s.rowFactory != nil {
		return c.s.rowFactory(q, v), nil
	}
	return &scriptRows{values: v}, nil
}

type scriptRows struct {
	values [][]driver.Value
	pos    int
}

func (r *scriptRows) Columns() []string {
	n := 1
	if len(r.values) > 0 {
		n = len(r.values[0])
	}
	v := make([]string, n)
	for i := range v {
		v[i] = fmt.Sprint(i)
	}
	return v
}
func (*scriptRows) Close() error { return nil }
func (r *scriptRows) Next(v []driver.Value) error {
	if r.pos == len(r.values) {
		return io.EOF
	}
	copy(v, r.values[r.pos])
	r.pos++
	return nil
}
func scriptDB(t *testing.T, s *scriptConnector) *sql.DB {
	t.Helper()
	db := sql.OpenDB(s)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}
func (s *scriptConnector) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...)
}
