package sqlite

import (
	"context"
	"database/sql"
	"sort"
	"strings"

	"github.com/anderson-0/qh/internal/engine"
)

const (
	columnsSQL = `SELECT m.name, m.type, p.name, p.type, p."notnull", p.dflt_value, p.pk
		FROM sqlite_schema m JOIN pragma_table_info(m.name) p
		WHERE m.type IN ('table', 'view') AND m.name NOT LIKE 'sqlite_%'
		ORDER BY m.name, p.cid`
	foreignKeysSQL = `SELECT m.name, f.id, f."table", f."from", f."to"
		FROM sqlite_schema m JOIN pragma_foreign_key_list(m.name) f
		WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite_%'
		ORDER BY m.name, f.id, f.seq`
	indexesSQL = `SELECT m.name, il.name, il."unique", il.origin, ii.name
		FROM sqlite_schema m JOIN pragma_index_list(m.name) il JOIN pragma_index_info(il.name) ii
		WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite_%'
		ORDER BY m.name, il.name, ii.seqno`
)

func (c *conn) Introspect(ctx context.Context) (*engine.Catalog, error) {
	sc, err := c.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer sc.Close()

	ns := engine.Namespace{Name: "main"}
	objs := map[string]*engine.Object{}
	pkPos := map[string]map[int]string{}
	rows, err := sc.QueryContext(ctx, columnsSQL)
	if err != nil {
		return nil, classify(ctx, err)
	}
	for rows.Next() {
		var table, kind, col, typ string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&table, &kind, &col, &typ, &notNull, &dflt, &pk); err != nil {
			rows.Close()
			return nil, classify(ctx, err)
		}
		o := objs[table]
		if o == nil {
			k := engine.KindTable
			if kind == "view" {
				k = engine.KindView
			}
			o = &engine.Object{Namespace: "main", Name: table, Kind: k, RowEstimate: -1}
			objs[table] = o
			ns.Objects = append(ns.Objects, o)
		}
		o.Fields = append(o.Fields, engine.Field{
			Name: col, Type: strings.ToLower(typ), Nullable: notNull == 0 && pk == 0, Default: dflt.String,
		})
		if pk > 0 {
			if pkPos[table] == nil {
				pkPos[table] = map[int]string{}
			}
			pkPos[table][pk] = col
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, classify(ctx, err)
	}
	for table, pos := range pkPos {
		keys := make([]int, 0, len(pos))
		for k := range pos {
			keys = append(keys, k)
		}
		sort.Ints(keys)
		for _, k := range keys {
			objs[table].PrimaryKey = append(objs[table].PrimaryKey, pos[k])
		}
	}

	rows, err = sc.QueryContext(ctx, foreignKeysSQL)
	if err != nil {
		return nil, classify(ctx, err)
	}
	type fkKey struct {
		table string
		id    int
	}
	fks := map[fkKey]*engine.ForeignKey{}
	var order []fkKey
	for rows.Next() {
		var table, ref, from string
		var to sql.NullString
		var id int
		if err := rows.Scan(&table, &id, &ref, &from, &to); err != nil {
			rows.Close()
			return nil, classify(ctx, err)
		}
		k := fkKey{table, id}
		fk := fks[k]
		if fk == nil {
			fk = &engine.ForeignKey{Namespace: "main", Object: table, RefNamespace: "main", RefObject: ref}
			fks[k] = fk
			order = append(order, k)
		}
		fk.Columns = append(fk.Columns, from)
		fk.RefColumns = append(fk.RefColumns, to.String) // empty means "the referenced primary key"
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, classify(ctx, err)
	}
	for _, k := range order {
		fk := fks[k]
		if target := objs[fk.RefObject]; target != nil && len(target.PrimaryKey) == len(fk.RefColumns) {
			for i, rc := range fk.RefColumns {
				if rc == "" {
					fk.RefColumns[i] = target.PrimaryKey[i]
				}
			}
		}
		if o := objs[k.table]; o != nil {
			o.ForeignKeys = append(o.ForeignKeys, *fk)
		}
	}

	rows, err = sc.QueryContext(ctx, indexesSQL)
	if err != nil {
		return nil, classify(ctx, err)
	}
	idx := map[string]*engine.Index{}
	var idxOrder []struct{ table, name string }
	for rows.Next() {
		var table, name, origin string
		var unique int
		var col sql.NullString
		if err := rows.Scan(&table, &name, &unique, &origin, &col); err != nil {
			rows.Close()
			return nil, classify(ctx, err)
		}
		if origin == "pk" {
			continue
		}
		ix := idx[name]
		if ix == nil {
			ix = &engine.Index{Name: name, Unique: unique == 1}
			idx[name] = ix
			idxOrder = append(idxOrder, struct{ table, name string }{table, name})
		}
		c := col.String
		if !col.Valid {
			c = "<expr>"
		}
		ix.Columns = append(ix.Columns, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, classify(ctx, err)
	}
	for _, e := range idxOrder {
		if o := objs[e.table]; o != nil {
			o.Indexes = append(o.Indexes, *idx[e.name])
		}
	}

	cat := &engine.Catalog{Namespaces: []engine.Namespace{ns}}
	cat.Link()
	return cat, nil
}
