package mysql

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/anderson-0/qh/internal/engine"
	"github.com/anderson-0/qh/internal/qerr"
	gomysql "github.com/go-sql-driver/mysql"
)

type cappedRows struct {
	scriptRows
	ctx      context.Context
	killed   <-chan struct{}
	terminal error
}

func (r *cappedRows) Next(v []driver.Value) error {
	if r.pos < len(r.values) {
		return r.scriptRows.Next(v)
	}
	select {
	case <-r.ctx.Done():
		return r.ctx.Err()
	case <-r.killed:
		return r.terminal
	}
}
func TestWriteRowCapPublicFailures(t *testing.T) {
	for _, kind := range []string{"marker", "denied", "caller", "native"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			checking, release, killed := make(chan struct{}), make(chan struct{}), make(chan struct{})
			const query = "SELECT 1 LIMIT 5000"
			s := preparationScript(t)
			base := s.handle
			s.handle = func(ctx context.Context, id int, q string, a []driver.NamedValue) ([][]driver.Value, error) {
				switch {
				case strings.Contains(q, "information_schema.PROCESSLIST"):
					close(checking)
					select {
					case <-release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					r := activeTarget()
					if kind == "marker" {
						r[0][1] = nil
					}
					return r, nil
				case strings.HasPrefix(q, "KILL QUERY"):
					if kind == "denied" {
						return nil, errors.New("private-control-detail")
					}
					close(killed)
					return nil, nil
				case q == query:
					return make([][]driver.Value, drainBudget+2), nil
				case strings.HasPrefix(q, "SET SESSION"):
					return nil, nil
				default:
					return base(ctx, id, q, a)
				}
			}
			var statementCtx context.Context
			oldHandler := s.handle
			s.handle = func(ctx context.Context, id int, q string, a []driver.NamedValue) ([][]driver.Value, error) {
				if q == query {
					statementCtx = ctx
				}
				return oldHandler(ctx, id, q, a)
			}
			s.rowFactory = func(q string, v [][]driver.Value) driver.Rows {
				if q != query {
					return &scriptRows{values: v}
				}
				for i := range v {
					v[i] = []driver.Value{int64(1)}
				}
				terminal := interrupted()
				if kind == "native" {
					terminal = &gomysql.MySQLError{Number: 3024, Message: "server timeout"}
				}
				return &cappedRows{scriptRows: scriptRows{values: v}, ctx: statementCtx, killed: killed, terminal: terminal}
			}
			owner := &conn{db: scriptDB(t, s), flavor: flavorMySQL}
			owner.writes = newWriteCanceler(owner, s, 1)
			defer owner.Close()
			done := make(chan error, 1)
			go func() {
				_, err := owner.Query(ctx, query, engine.QueryOpts{MaxRows: 1, Timeout: time.Second})
				done <- err
			}()
			awaitGuard(t, checking)
			if kind == "caller" || kind == "native" {
				cancel()
			}
			close(release)
			var err error
			select {
			case err = <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("protected path did not return")
			}
			if err == nil {
				t.Fatal("unconfirmed/canceled result returned success")
			}
			qe := qerr.From(err)
			want := qerr.QueryError
			if kind == "native" {
				want = qerr.Timeout
			}
			hint := kind == "marker" || kind == "denied"
			if qe.Category != want || (qe.Hint == unconfirmedHint) != hint || strings.Contains(err.Error(), "private-control-detail") {
				t.Fatalf("result=%v", err)
			}
		})
	}
}

type closeErrorRows struct{ laterErrorRows }

func (*closeErrorRows) NextResultSet() error { return io.EOF }
func TestWriteCallCloseError(t *testing.T) {
	s := &scriptConnector{handle: func(context.Context, int, string, []driver.NamedValue) ([][]driver.Value, error) {
		return [][]driver.Value{{int64(1)}}, nil
	}, rowFactory: func(string, [][]driver.Value) driver.Rows {
		return &closeErrorRows{laterErrorRows{scriptRows{values: [][]driver.Value{{int64(1)}}}}}
	}}
	db := scriptDB(t, s)
	sc, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	_, _, err = collectWithStop(context.Background(), func() {}, sc, "CALL close_error()", 10, false, true)
	var me *gomysql.MySQLError
	if !errors.As(err, &me) || me.Number != 1317 {
		t.Fatalf("close-only failure lost: %v", err)
	}
}
