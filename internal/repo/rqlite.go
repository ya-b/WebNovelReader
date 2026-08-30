package repo

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"math"

	"gorm.io/gorm"

	rqlitestdlib "github.com/rqlite/gorqlite/stdlib"
	rqlite "goki.dev/rqlite"
)

// RqliteDialector returns a GORM dialector for rqlite whose result values are
// normalized (integral JSON numbers become int64) so that bool/int scans work.
func RqliteDialector(dsn string) gorm.Dialector {
	drv := &normalizedRqliteDriver{inner: &rqlitestdlib.Driver{}}
	pool := sql.OpenDB(singleDSNConnector{dsn: dsn, drv: drv})
	return &rqlite.Dialector{Conn: pool}
}

type singleDSNConnector struct {
	dsn string
	drv driver.Driver
}

func (c singleDSNConnector) Connect(context.Context) (driver.Conn, error) {
	return c.drv.Open(c.dsn)
}

func (c singleDSNConnector) Driver() driver.Driver { return c.drv }

type normalizedRqliteDriver struct{ inner driver.Driver }

func (d *normalizedRqliteDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &normalizedConn{conn}, nil
}

type normalizedConn struct{ driver.Conn }

func (c *normalizedConn) Prepare(query string) (driver.Stmt, error) {
	stmt, err := c.Conn.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &normalizedStmt{stmt}, nil
}

type normalizedStmt struct{ driver.Stmt }

func (s *normalizedStmt) Query(args []driver.Value) (driver.Rows, error) {
	rows, err := s.Stmt.Query(args)
	if err != nil {
		return nil, err
	}
	return &normalizedRows{rows}, nil
}

func (s *normalizedStmt) QueryContext(ctx context.Context, namedArgs []driver.NamedValue) (driver.Rows, error) {
	if qc, ok := s.Stmt.(driver.StmtQueryContext); ok {
		rows, err := qc.QueryContext(ctx, namedArgs)
		if err != nil {
			return nil, err
		}
		return &normalizedRows{rows}, nil
	}
	args := make([]driver.Value, len(namedArgs))
	for i, a := range namedArgs {
		args[i] = a.Value
	}
	return s.Query(args)
}

func (s *normalizedStmt) ExecContext(ctx context.Context, namedArgs []driver.NamedValue) (driver.Result, error) {
	if ec, ok := s.Stmt.(driver.StmtExecContext); ok {
		return ec.ExecContext(ctx, namedArgs)
	}
	args := make([]driver.Value, len(namedArgs))
	for i, a := range namedArgs {
		args[i] = a.Value
	}
	return s.Stmt.Exec(args)
}

type normalizedRows struct{ driver.Rows }

func (r *normalizedRows) Next(dest []driver.Value) error {
	err := r.Rows.Next(dest)
	for i, v := range dest {
		dest[i] = normalizeDriverValue(v)
	}
	return err
}

func normalizeDriverValue(v driver.Value) driver.Value {
	f, ok := v.(float64)
	if !ok || f != math.Trunc(f) || math.IsInf(f, 0) || math.IsNaN(f) {
		return v
	}
	return int64(f)
}
