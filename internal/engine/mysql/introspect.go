package mysql

import (
	"context"
	"database/sql"
	"slices"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/anderson-0/qh/internal/engine"
)

const systemSchemas = "'mysql', 'information_schema', 'performance_schema', 'sys'"

// filter scopes catalog queries to the connection's database, or to every
// non-system database when the connection string names none.
func (c *conn) filter(col string) (string, []any) {
	if c.dbName != "" {
		return col + " = ?", []any{c.dbName}
	}
	return col + " NOT IN (" + systemSchemas + ")", nil
}

type tableRow struct {
	schema, name, kind, comment string
	rows                        sql.NullInt64
}

type columnRow struct {
	schema, table, name, colType, dataType, nullable, extra, comment string
	dflt                                                             sql.NullString
}

type keyRow struct {
	schema, table, constraint, column, refSchema, refTable, refColumn string
}

type indexRow struct {
	schema, table, index string
	nonUnique            int
	column               sql.NullString
}

func (c *conn) queryRows(ctx context.Context, q string, args []any, each func(*sql.Rows) error) error {
	rows, err := c.db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := each(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (c *conn) Introspect(ctx context.Context) (*engine.Catalog, error) {
	var (
		tables  []tableRow
		columns []columnRow
		keys    []keyRow
		indexes []indexRow
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		f, args := c.filter("TABLE_SCHEMA")
		return c.queryRows(gctx, `SELECT TABLE_SCHEMA, TABLE_NAME, TABLE_TYPE, TABLE_ROWS, COALESCE(TABLE_COMMENT, '')
			FROM information_schema.TABLES WHERE `+f+` ORDER BY TABLE_SCHEMA, TABLE_NAME`, args, func(r *sql.Rows) error {
			var x tableRow
			if err := r.Scan(&x.schema, &x.name, &x.kind, &x.rows, &x.comment); err != nil {
				return err
			}
			tables = append(tables, x)
			return nil
		})
	})
	g.Go(func() error {
		f, args := c.filter("TABLE_SCHEMA")
		return c.queryRows(gctx, `SELECT TABLE_SCHEMA, TABLE_NAME, COLUMN_NAME, COLUMN_TYPE, DATA_TYPE, IS_NULLABLE,
				COLUMN_DEFAULT, EXTRA, COALESCE(COLUMN_COMMENT, '')
			FROM information_schema.COLUMNS WHERE `+f+` ORDER BY TABLE_SCHEMA, TABLE_NAME, ORDINAL_POSITION`, args, func(r *sql.Rows) error {
			var x columnRow
			if err := r.Scan(&x.schema, &x.table, &x.name, &x.colType, &x.dataType, &x.nullable, &x.dflt, &x.extra, &x.comment); err != nil {
				return err
			}
			columns = append(columns, x)
			return nil
		})
	})
	g.Go(func() error {
		f, args := c.filter("TABLE_SCHEMA")
		return c.queryRows(gctx, `SELECT TABLE_SCHEMA, TABLE_NAME, CONSTRAINT_NAME, COLUMN_NAME,
				REFERENCED_TABLE_SCHEMA, REFERENCED_TABLE_NAME, REFERENCED_COLUMN_NAME
			FROM information_schema.KEY_COLUMN_USAGE
			WHERE REFERENCED_TABLE_NAME IS NOT NULL AND `+f+`
			ORDER BY TABLE_SCHEMA, TABLE_NAME, CONSTRAINT_NAME, ORDINAL_POSITION`, args, func(r *sql.Rows) error {
			var x keyRow
			if err := r.Scan(&x.schema, &x.table, &x.constraint, &x.column, &x.refSchema, &x.refTable, &x.refColumn); err != nil {
				return err
			}
			keys = append(keys, x)
			return nil
		})
	})
	g.Go(func() error {
		f, args := c.filter("TABLE_SCHEMA")
		return c.queryRows(gctx, `SELECT TABLE_SCHEMA, TABLE_NAME, INDEX_NAME, NON_UNIQUE, COLUMN_NAME
			FROM information_schema.STATISTICS WHERE `+f+`
			ORDER BY TABLE_SCHEMA, TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX`, args, func(r *sql.Rows) error {
			var x indexRow
			if err := r.Scan(&x.schema, &x.table, &x.index, &x.nonUnique, &x.column); err != nil {
				return err
			}
			indexes = append(indexes, x)
			return nil
		})
	})
	if err := g.Wait(); err != nil {
		return nil, classify(ctx, err)
	}
	return c.assemble(tables, columns, keys, indexes), nil
}

func (c *conn) assemble(tables []tableRow, columns []columnRow, keys []keyRow, indexes []indexRow) *engine.Catalog {
	cat := &engine.Catalog{}
	nsIndex := map[string]int{}
	objs := map[string]*engine.Object{}
	key := func(schema, name string) string { return schema + "\x00" + name }
	for _, t := range tables {
		kind := engine.KindTable
		switch t.kind {
		case "VIEW", "SYSTEM VIEW":
			kind = engine.KindView
		case "SEQUENCE":
			continue
		}
		i, ok := nsIndex[t.schema]
		if !ok {
			i = len(cat.Namespaces)
			nsIndex[t.schema] = i
			cat.Namespaces = append(cat.Namespaces, engine.Namespace{Name: t.schema})
		}
		o := &engine.Object{Namespace: t.schema, Name: t.name, Kind: kind, RowEstimate: -1}
		if kind == engine.KindTable {
			o.Comment = t.comment
			if t.rows.Valid {
				o.RowEstimate = t.rows.Int64
			}
		}
		cat.Namespaces[i].Objects = append(cat.Namespaces[i].Objects, o)
		objs[key(t.schema, t.name)] = o
	}
	for _, col := range columns {
		o := objs[key(col.schema, col.table)]
		if o == nil {
			continue
		}
		f := engine.Field{Name: col.name, Type: col.colType, Nullable: col.nullable == "YES", Comment: col.comment}
		if col.dataType == "enum" || col.dataType == "set" {
			f.Type = col.dataType
			f.Enum = parseEnum(col.colType)
		}
		switch {
		case strings.Contains(col.extra, "auto_increment"):
			f.Default = "auto"
		case col.dflt.Valid && !(c.flavor == flavorMariaDB && col.dflt.String == "NULL"):
			f.Default = col.dflt.String
		}
		o.Fields = append(o.Fields, f)
	}
	type fkKey struct{ schema, table, constraint string }
	fks := map[fkKey]*engine.ForeignKey{}
	var fkOrder []fkKey
	for _, k := range keys {
		id := fkKey{k.schema, k.table, k.constraint}
		fk := fks[id]
		if fk == nil {
			fk = &engine.ForeignKey{Namespace: k.schema, Object: k.table, RefNamespace: k.refSchema, RefObject: k.refTable}
			fks[id] = fk
			fkOrder = append(fkOrder, id)
		}
		fk.Columns = append(fk.Columns, k.column)
		fk.RefColumns = append(fk.RefColumns, k.refColumn)
	}
	for _, id := range fkOrder {
		if o := objs[key(id.schema, id.table)]; o != nil {
			o.ForeignKeys = append(o.ForeignKeys, *fks[id])
		}
	}
	type ixKey struct{ schema, table, index string }
	ixs := map[ixKey]*engine.Index{}
	var ixOrder []ixKey
	for _, x := range indexes {
		o := objs[key(x.schema, x.table)]
		if o == nil {
			continue
		}
		col := x.column.String
		if !x.column.Valid {
			col = "<expr>"
		}
		if x.index == "PRIMARY" {
			o.PrimaryKey = append(o.PrimaryKey, col)
			continue
		}
		id := ixKey{x.schema, x.table, x.index}
		ix := ixs[id]
		if ix == nil {
			ix = &engine.Index{Name: x.index, Unique: x.nonUnique == 0}
			ixs[id] = ix
			ixOrder = append(ixOrder, id)
		}
		ix.Columns = append(ix.Columns, col)
	}
	for _, id := range ixOrder {
		o := objs[key(id.schema, id.table)]
		o.Indexes = append(o.Indexes, *ixs[id])
	}
	// Server collations order names differently (MariaDB and MySQL disagree
	// about "_"), so sort here for the same output on both.
	byName := func(a, b string) int {
		if c := strings.Compare(strings.ToLower(a), strings.ToLower(b)); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	}
	slices.SortFunc(cat.Namespaces, func(a, b engine.Namespace) int { return byName(a.Name, b.Name) })
	for i := range cat.Namespaces {
		slices.SortFunc(cat.Namespaces[i].Objects, func(a, b *engine.Object) int { return byName(a.Name, b.Name) })
	}
	cat.Link()
	return cat
}
