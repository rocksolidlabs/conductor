package history

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"sync"
	"sync/atomic"
	"testing"
)

// testDriverName is a wrapper of the sqlite driver that counts the
// connections open through it and holds a rollback at a gate: the
// mechanism a test of Close waits on, never a sleep. A test opens its
// index through it with useTestDriver; the package's other tests use the
// driver itself.
const testDriverName = "sqlite-history-test"

var (
	registerTestDriver sync.Once
	// connsOpen counts the connections open through the wrapper.
	connsOpen atomic.Int64
	// heldRollback, when set, is taken by the next rollback, which waits at
	// it (holdNextRollback).
	heldRollback atomic.Pointer[rollbackHold]
)

// useTestDriver opens the test's indexes through the wrapper.
func useTestDriver(t *testing.T) {
	t.Helper()
	registerTestDriver.Do(func() {
		db, err := sql.Open("sqlite", "")
		if err != nil {
			panic(err)
		}
		inner := db.Driver()
		db.Close()
		sql.Register(testDriverName, testDriver{inner})
	})
	old := driverName
	driverName = testDriverName
	t.Cleanup(func() { driverName = old })
}

// rollbackHold holds one rollback of the driver: arrived is closed when it
// reaches the hold, done once the driver has rolled back.
type rollbackHold struct {
	arrived, release, done chan struct{}
	once                   sync.Once
}

func newRollbackHold() *rollbackHold {
	return &rollbackHold{arrived: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
}

// arm makes the driver's next rollback, whoever runs it, wait for let.
func (h *rollbackHold) arm() { heldRollback.Store(h) }

func (h *rollbackHold) let() { h.once.Do(func() { close(h.release) }) }

// ended says the held rollback has ended.
func (h *rollbackHold) ended() bool {
	select {
	case <-h.done:
		return true
	default:
		return false
	}
}

type testDriver struct{ inner driver.Driver }

func (d testDriver) Open(name string) (driver.Conn, error) {
	c, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	connsOpen.Add(1)
	return &testConn{Conn: c}, nil
}

// testConn passes every interface database/sql asks of the sqlite
// connection through, so the index behaves as it does with the driver
// itself.
type testConn struct {
	driver.Conn
	closed atomic.Bool
}

func (c *testConn) Close() error {
	err := c.Conn.Close()
	if !c.closed.Swap(true) {
		connsOpen.Add(-1)
	}
	return err
}

func (c *testConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return testTx{tx}, nil
}

func (c *testConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	return c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, query)
}

func (c *testConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *testConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (c *testConn) Ping(ctx context.Context) error { return c.Conn.(driver.Pinger).Ping(ctx) }

func (c *testConn) ResetSession(ctx context.Context) error {
	return c.Conn.(driver.SessionResetter).ResetSession(ctx)
}

func (c *testConn) IsValid() bool { return c.Conn.(driver.Validator).IsValid() }

type testTx struct{ driver.Tx }

func (t testTx) Rollback() error {
	h := heldRollback.Swap(nil)
	if h == nil {
		return t.Tx.Rollback()
	}
	close(h.arrived)
	<-h.release
	err := t.Tx.Rollback()
	close(h.done)
	return err
}
