package postgres

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"

	"github.com/anderson-0/qh/internal/engine"
)

const nsFilter = `n.nspname NOT IN ('pg_catalog', 'information_schema')
	AND n.nspname NOT LIKE 'pg\_toast%' AND n.nspname NOT LIKE 'pg\_temp%'`

var (
	relationsSQL = `SELECT n.nspname::text, c.relname::text, c.relkind::text, c.reltuples::bigint,
		COALESCE(obj_description(c.oid, 'pg_class'), '')
	FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
	WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f') AND NOT c.relispartition
		AND has_table_privilege(c.oid, 'SELECT') AND ` + nsFilter + `
	ORDER BY 1, 2`

	columnsSQL = `SELECT n.nspname::text, c.relname::text, a.attname::text, format_type(a.atttypid, a.atttypmod),
		NOT a.attnotnull,
		CASE WHEN a.attidentity <> '' THEN 'nextval(identity)' ELSE COALESCE(pg_get_expr(d.adbin, d.adrelid), '') END,
		COALESCE(col_description(c.oid, a.attnum), ''), a.atttypid::bigint
	FROM pg_attribute a
	JOIN pg_class c ON c.oid = a.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace
	LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
	WHERE a.attnum > 0 AND NOT a.attisdropped AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
		AND NOT c.relispartition AND ` + nsFilter + `
	ORDER BY 1, 2, a.attnum`

	constraintsSQL = `SELECT n.nspname::text, c.relname::text, con.contype::text, con.conname::text,
		ARRAY(SELECT a.attname::text FROM unnest(con.conkey) WITH ORDINALITY k(attnum, ord)
			JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum ORDER BY k.ord),
		COALESCE(fn.nspname::text, ''), COALESCE(fc.relname::text, ''),
		ARRAY(SELECT a.attname::text FROM unnest(con.confkey) WITH ORDINALITY k(attnum, ord)
			JOIN pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = k.attnum ORDER BY k.ord)
	FROM pg_constraint con
	JOIN pg_class c ON c.oid = con.conrelid JOIN pg_namespace n ON n.oid = c.relnamespace
	LEFT JOIN pg_class fc ON fc.oid = con.confrelid LEFT JOIN pg_namespace fn ON fn.oid = fc.relnamespace
	WHERE con.contype IN ('p', 'f') AND ` + nsFilter + `
	ORDER BY 1, 2, 4`

	indexesSQL = `SELECT n.nspname::text, c.relname::text, i.relname::text, ix.indisunique,
		ARRAY(SELECT pg_get_indexdef(ix.indexrelid, k, true) FROM generate_series(1, ix.indnkeyatts) AS k ORDER BY k)
	FROM pg_index ix
	JOIN pg_class i ON i.oid = ix.indexrelid JOIN pg_class c ON c.oid = ix.indrelid
	JOIN pg_namespace n ON n.oid = c.relnamespace
	WHERE NOT ix.indisprimary AND ` + nsFilter + `
	ORDER BY 1, 2, 3`

	enumsSQL = `SELECT e.enumtypid::bigint, e.enumlabel::text FROM pg_enum e ORDER BY e.enumtypid, e.enumsortorder`
)

type relRow struct {
	ns, name, kind, comment string
	est                     int64
}

type colRow struct {
	ns, table, name, typ, dflt, comment string
	nullable                            bool
	typeOID                             int64
}

type conRow struct {
	ns, table, kind, name, refNS, refTable string
	cols, refCols                          []string
}

type idxRow struct {
	ns, table, name string
	unique          bool
	cols            []string
}

func (c *conn) queryRows(ctx context.Context, sql string, each func(pgx.Rows) error) error {
	rows, err := c.pool.Query(ctx, sql, pgx.QueryExecModeExec)
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
		rels  []relRow
		cols  []colRow
		cons  []conRow
		idxs  []idxRow
		enums = map[int64][]string{}
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return c.queryRows(gctx, relationsSQL, func(r pgx.Rows) error {
			var x relRow
			if err := r.Scan(&x.ns, &x.name, &x.kind, &x.est, &x.comment); err != nil {
				return err
			}
			rels = append(rels, x)
			return nil
		})
	})
	g.Go(func() error {
		return c.queryRows(gctx, columnsSQL, func(r pgx.Rows) error {
			var x colRow
			if err := r.Scan(&x.ns, &x.table, &x.name, &x.typ, &x.nullable, &x.dflt, &x.comment, &x.typeOID); err != nil {
				return err
			}
			cols = append(cols, x)
			return nil
		})
	})
	g.Go(func() error {
		return c.queryRows(gctx, constraintsSQL, func(r pgx.Rows) error {
			var x conRow
			if err := r.Scan(&x.ns, &x.table, &x.kind, &x.name, &x.cols, &x.refNS, &x.refTable, &x.refCols); err != nil {
				return err
			}
			cons = append(cons, x)
			return nil
		})
	})
	g.Go(func() error {
		return c.queryRows(gctx, indexesSQL, func(r pgx.Rows) error {
			var x idxRow
			if err := r.Scan(&x.ns, &x.table, &x.name, &x.unique, &x.cols); err != nil {
				return err
			}
			idxs = append(idxs, x)
			return nil
		})
	})
	g.Go(func() error {
		return c.queryRows(gctx, enumsSQL, func(r pgx.Rows) error {
			var oid int64
			var label string
			if err := r.Scan(&oid, &label); err != nil {
				return err
			}
			enums[oid] = append(enums[oid], label)
			return nil
		})
	})
	if err := g.Wait(); err != nil {
		return nil, c.classify(ctx, err)
	}
	return assemble(rels, cols, cons, idxs, enums), nil
}

func assemble(rels []relRow, cols []colRow, cons []conRow, idxs []idxRow, enums map[int64][]string) *engine.Catalog {
	cat := &engine.Catalog{}
	nsIndex := map[string]int{}
	objs := map[string]*engine.Object{}
	key := func(ns, name string) string { return ns + "\x00" + name }
	for _, r := range rels {
		i, ok := nsIndex[r.ns]
		if !ok {
			i = len(cat.Namespaces)
			nsIndex[r.ns] = i
			cat.Namespaces = append(cat.Namespaces, engine.Namespace{Name: r.ns})
		}
		o := &engine.Object{Namespace: r.ns, Name: r.name, Kind: kindOf(r.kind), Comment: r.comment, RowEstimate: r.est}
		if o.Kind == engine.KindView || o.RowEstimate < 0 {
			o.RowEstimate = -1
		}
		cat.Namespaces[i].Objects = append(cat.Namespaces[i].Objects, o)
		objs[key(r.ns, r.name)] = o
	}
	for _, c := range cols {
		if o := objs[key(c.ns, c.table)]; o != nil {
			o.Fields = append(o.Fields, engine.Field{
				Name: c.name, Type: shortType(c.typ), Nullable: c.nullable, Default: c.dflt,
				Comment: c.comment, Enum: enums[c.typeOID],
			})
		}
	}
	for _, c := range cons {
		o := objs[key(c.ns, c.table)]
		if o == nil {
			continue
		}
		switch c.kind {
		case "p":
			o.PrimaryKey = c.cols
		case "f":
			o.ForeignKeys = append(o.ForeignKeys, engine.ForeignKey{
				Namespace: c.ns, Object: c.table, Columns: c.cols,
				RefNamespace: c.refNS, RefObject: c.refTable, RefColumns: c.refCols,
			})
		}
	}
	for _, x := range idxs {
		if o := objs[key(x.ns, x.table)]; o != nil {
			o.Indexes = append(o.Indexes, engine.Index{Name: x.name, Columns: x.cols, Unique: x.unique})
		}
	}
	cat.Link()
	return cat
}

func kindOf(relkind string) engine.Kind {
	switch relkind {
	case "v":
		return engine.KindView
	case "m":
		return engine.KindMatView
	}
	return engine.KindTable
}

// shortType shortens Postgres's verbose type names to save tokens.
func shortType(t string) string {
	switch {
	case strings.HasSuffix(t, " without time zone"):
		t = strings.TrimSuffix(t, " without time zone")
	case strings.HasSuffix(t, " with time zone"):
		t = strings.TrimSuffix(t, " with time zone")
		if strings.HasPrefix(t, "timestamp") {
			t = "timestamptz" + strings.TrimPrefix(t, "timestamp")
		} else if strings.HasPrefix(t, "time") {
			t = "timetz" + strings.TrimPrefix(t, "time")
		}
	}
	t = strings.Replace(t, "character varying", "varchar", 1)
	return strings.Replace(t, "double precision", "float8", 1)
}
