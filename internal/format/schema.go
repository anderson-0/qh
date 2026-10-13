package format

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/anderson-0/qh/internal/engine"
)

// SchemaOpts selects what Schema renders.
type SchemaOpts struct {
	Pattern     string // glob with *, or a substring when it has no *
	Object      string // exact "name" or "namespace.name", ignoring case
	MaxOverview int    // default 200
	MaxDetail   int    // default 30
}

// Schema renders an overview (no Pattern/Object) or object details.
// matched is the number of matching objects (the total for the overview).
// When nothing matches it returns ("", 0).
func Schema(c *engine.Catalog, o SchemaOpts) (string, int) {
	if o.MaxOverview <= 0 {
		o.MaxOverview = 200
	}
	if o.MaxDetail <= 0 {
		o.MaxDetail = 30
	}
	if strings.TrimSpace(o.Pattern) == "" && strings.TrimSpace(o.Object) == "" {
		return overview(c, o.MaxOverview)
	}
	objs := match(c, o)
	switch {
	case len(objs) == 0:
		return "", 0
	case len(objs) > o.MaxDetail:
		return namesOnly(objs), len(objs)
	}
	var b strings.Builder
	for i, obj := range objs {
		if i > 0 {
			b.WriteByte('\n')
		}
		writeObject(&b, obj)
	}
	return b.String(), len(objs)
}

// nsHeader is a namespace's overview line. A namespace with a note, which is
// one the engine could not fully list, shows it in parentheses.
func nsHeader(ns engine.Namespace) string {
	switch {
	case ns.Note == "":
		return fmt.Sprintf("%s: %s", ns.Name, kindCounts(ns.Objects))
	case len(ns.Objects) == 0:
		return fmt.Sprintf("%s: (%s)", ns.Name, ns.Note)
	}
	return fmt.Sprintf("%s: %s (%s)", ns.Name, kindCounts(ns.Objects), ns.Note)
}

// RichOverviewBudget is the largest overview, in bytes, that lists every
// object's columns. A schema that fits is described in one call, so an agent
// can write a join without a call per table; a larger one gets names only.
const RichOverviewBudget = 8000

func overview(c *engine.Catalog, max int) (string, int) {
	total := c.Count()
	if total == 0 && !c.HasNotes() {
		return "-- no tables or views found\n", 0
	}
	if total <= max {
		if s, ok := richOverview(c); ok {
			return s, total
		}
	}
	var b strings.Builder
	shown := 0
	for _, ns := range c.Namespaces {
		if len(ns.Objects) == 0 && ns.Note == "" {
			continue
		}
		b.WriteString(nsHeader(ns) + "\n")
		if len(ns.Objects) == 0 {
			continue
		}
		parts := make([]string, 0, len(ns.Objects))
		for _, obj := range ns.Objects {
			if shown == max {
				parts = append(parts, "…")
				break
			}
			parts = append(parts, overviewEntry(obj))
			shown++
		}
		b.WriteString("  " + strings.Join(parts, " · ") + "\n")
	}
	if shown < total {
		fmt.Fprintf(&b, "-- showing %d of %d objects; use pattern= to narrow\n", shown, total)
	}
	return b.String(), total
}

// richOverview lists each object with its columns: PK, single-column FK
// targets (→table, or →table.column when the names differ) and short enums.
// ok is false when it would exceed RichOverviewBudget.
func richOverview(c *engine.Catalog) (string, bool) {
	var b strings.Builder
	for _, ns := range c.Namespaces {
		if len(ns.Objects) == 0 && ns.Note == "" {
			continue
		}
		b.WriteString(nsHeader(ns) + "\n")
		for _, o := range ns.Objects {
			b.WriteString("  " + overviewEntry(o) + ": " + columnList(o) + "\n")
			if b.Len() > RichOverviewBudget {
				return "", false
			}
		}
	}
	return b.String(), true
}

func columnList(o *engine.Object) string {
	pk := map[string]bool{}
	for _, c := range o.PrimaryKey {
		pk[c] = true
	}
	fks := map[string]engine.ForeignKey{}
	for _, fk := range o.ForeignKeys {
		if len(fk.Columns) == 1 && len(fk.RefColumns) == 1 {
			fks[fk.Columns[0]] = fk
		}
	}
	// Only sampled (Inferred) fields fold: a SQL column named
	// "address.city" stays a column of its own.
	names := map[string]bool{}
	for _, f := range o.Fields {
		if f.Inferred {
			names[f.Name] = true
		}
	}
	children := map[string][]string{}
	// childRefs holds "→target" for a child that is itself a referencing
	// field, such as product_id in items[].product_id.
	childRefs := map[string]string{}
	for _, f := range o.Fields {
		p, c, ok := childOf(f.Name)
		if !ok || !f.Inferred || !names[p] {
			continue
		}
		if !contains(children[p], c) {
			children[p] = append(children[p], c)
		}
		if fk, ok := fks[f.Name]; ok && (f.Name == p+"."+c || f.Name == p+"[]."+c) {
			childRefs[p+"\x00"+c] = arrow(o.Namespace, f, fk)
		}
	}
	var cols []string
	for _, f := range o.Fields {
		if p, _, ok := childOf(f.Name); ok && f.Inferred && names[p] {
			continue
		}
		s := f.Name
		if kids := children[f.Name]; len(kids) > 0 {
			s += "{" + strings.Join(foldChildren(kids, func(c string) string { return childRefs[f.Name+"\x00"+c] }), ",") + "}"
		}
		if pk[f.Name] {
			s += " PK"
		}
		if fk, ok := fks[f.Name]; ok {
			s += arrow(o.Namespace, f, fk)
		}
		if len(f.Enum) > 0 && len(f.Enum) <= 10 {
			s += " [" + strings.Join(f.Enum, "|") + "]"
		}
		cols = append(cols, s)
	}
	return strings.Join(cols, ", ")
}

// arrow renders a single-column reference as →table, →table.column when the
// names differ, or →collection for a sampled field's reference to _id.
func arrow(from string, f engine.Field, fk engine.ForeignKey) string {
	s := "→" + target(from, fk.RefNamespace, fk.RefObject)
	if fk.RefColumns[0] != f.Name && !(f.Inferred && fk.RefColumns[0] == "_id") {
		s += "." + fk.RefColumns[0]
	}
	return s
}

// foldChildren shows up to two child names, then "…". Children that
// reference another collection carry their arrow and, when the cut would
// hide them, move to the front.
func foldChildren(kids []string, ref func(string) string) []string {
	order := kids
	if len(kids) > 2 {
		var refs, rest []string
		for _, k := range kids {
			if ref(k) != "" {
				refs = append(refs, k)
			} else {
				rest = append(rest, k)
			}
		}
		hidden := false
		for _, k := range kids[2:] {
			hidden = hidden || ref(k) != ""
		}
		if hidden {
			order = append(refs, rest...)
		}
	}
	var out []string
	for i, k := range order {
		if i == 2 {
			out = append(out, "…")
			break
		}
		out = append(out, k+ref(k))
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// childOf returns the top-level parent and the next segment of a nested
// field name such as "address.city" or "items[].sku".
func childOf(name string) (parent, child string, ok bool) {
	i := strings.IndexAny(name, ".[")
	if i <= 0 {
		return "", "", false
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(name[i:], "[]"), ".")
	if j := strings.IndexAny(rest, ".["); j >= 0 {
		rest = rest[:j]
	}
	return name[:i], rest, rest != ""
}

var kindLabels = []struct {
	kind      engine.Kind
	one, many string
}{
	{engine.KindTable, "table", "tables"},
	{engine.KindCollection, "collection", "collections"},
	{engine.KindView, "view", "views"},
	{engine.KindMatView, "materialized view", "materialized views"},
}

func kindCounts(objs []*engine.Object) string {
	counts := map[engine.Kind]int{}
	for _, o := range objs {
		counts[o.Kind]++
	}
	var parts []string
	for _, k := range kindLabels {
		n := counts[k.kind]
		if n == 0 {
			continue
		}
		label := k.many
		if n == 1 {
			label = k.one
		}
		parts = append(parts, fmt.Sprintf("%d %s", n, label))
	}
	return strings.Join(parts, ", ")
}

func overviewEntry(o *engine.Object) string {
	s := o.Name
	switch o.Kind {
	case engine.KindView:
		return s + " (view)"
	case engine.KindMatView:
		s += " (mview)"
	}
	if o.RowEstimate >= 0 {
		s += " ~" + Compact(o.RowEstimate)
	}
	return s
}

// Compact formats a row count as 999, 1.2k, 90k, 1.2M, 48M, 3.4B.
func Compact(n int64) string {
	switch {
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 1_000_000:
		return scaled(n, 1000, "k")
	case n < 1_000_000_000:
		return scaled(n, 1_000_000, "M")
	}
	return scaled(n, 1_000_000_000, "B")
}

func scaled(n, unit int64, suffix string) string {
	v := float64(n) / float64(unit)
	if v < 10 {
		return strings.TrimSuffix(strconv.FormatFloat(v, 'f', 1, 64), ".0") + suffix
	}
	return strconv.FormatFloat(math.Floor(v), 'f', 0, 64) + suffix
}

func match(c *engine.Catalog, o SchemaOpts) []*engine.Object {
	if obj := strings.TrimSpace(o.Object); obj != "" {
		return c.Lookup(obj)
	}
	re := globRegexp(o.Pattern)
	var out []*engine.Object
	for _, ns := range c.Namespaces {
		for _, x := range ns.Objects {
			if re.MatchString(x.Name) || re.MatchString(x.Namespace+"."+x.Name) {
				out = append(out, x)
			}
		}
	}
	return out
}

func globRegexp(p string) *regexp.Regexp {
	p = strings.TrimSpace(p)
	if !strings.Contains(p, "*") {
		p = "*" + p + "*"
	}
	parts := strings.Split(p, "*")
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	return regexp.MustCompile("(?i)^" + strings.Join(parts, ".*") + "$")
}

func namesOnly(objs []*engine.Object) string {
	names := make([]string, 0, len(objs))
	for i, o := range objs {
		if i == 200 {
			names = append(names, "…")
			break
		}
		names = append(names, o.Namespace+"."+o.Name)
	}
	return fmt.Sprintf("-- %d objects match; showing names only. Narrow the pattern or use object=\n%s\n",
		len(objs), strings.Join(names, ", "))
}

func writeObject(b *strings.Builder, o *engine.Object) {
	b.WriteString(o.Namespace + "." + o.Name)
	switch o.Kind {
	case engine.KindView:
		b.WriteString(" (view)")
	case engine.KindMatView:
		b.WriteString(" (materialized view)")
	}
	if o.RowEstimate >= 0 && o.Kind != engine.KindView {
		b.WriteString(" ~" + Compact(o.RowEstimate) + " rows")
	}
	if o.Comment != "" {
		b.WriteString(": " + clip(o.Comment, 120))
	}
	b.WriteByte('\n')

	pk := map[string]bool{}
	for _, c := range o.PrimaryKey {
		pk[c] = true
	}
	single := map[string]engine.ForeignKey{}
	var multi []engine.ForeignKey
	for _, fk := range o.ForeignKeys {
		if len(fk.Columns) == 1 && len(fk.RefColumns) == 1 {
			single[fk.Columns[0]] = fk
		} else {
			multi = append(multi, fk)
		}
	}
	for _, f := range o.Fields {
		b.WriteString("  " + f.Name)
		if f.Type != "" {
			b.WriteString(" " + f.Type)
		}
		if pk[f.Name] {
			b.WriteString(" PK")
		} else if !f.Nullable {
			b.WriteString(" NN")
		}
		if fk, ok := single[f.Name]; ok {
			b.WriteString(" → " + target(o.Namespace, fk.RefNamespace, fk.RefObject) + "." + fk.RefColumns[0])
		}
		if len(f.Enum) > 0 {
			b.WriteString(" [" + enumList(f.Enum) + "]")
		}
		if f.Default != "" {
			b.WriteString(" =" + defaultExpr(f.Default))
		}
		if f.Comment != "" {
			b.WriteString("  # " + clip(f.Comment, 120))
		}
		b.WriteByte('\n')
	}
	for _, fk := range multi {
		fmt.Fprintf(b, "  (%s) → %s(%s)\n", strings.Join(fk.Columns, ","),
			target(o.Namespace, fk.RefNamespace, fk.RefObject), strings.Join(fk.RefColumns, ","))
	}
	if len(o.ReferencedBy) > 0 {
		refs := make([]string, 0, len(o.ReferencedBy))
		for i, fk := range o.ReferencedBy {
			if i == 15 {
				refs = append(refs, fmt.Sprintf("…(+%d)", len(o.ReferencedBy)-15))
				break
			}
			name := target(o.Namespace, fk.Namespace, fk.Object)
			if len(fk.Columns) == 1 {
				name += "." + fk.Columns[0]
			} else {
				name += "(" + strings.Join(fk.Columns, ",") + ")"
			}
			refs = append(refs, name)
		}
		b.WriteString("  ← " + strings.Join(refs, ", ") + "\n")
	}
	var idx []string
	for _, ix := range o.Indexes {
		if sameColumns(ix.Columns, o.PrimaryKey) {
			continue
		}
		s := "(" + strings.Join(ix.Columns, ",") + ")"
		if ix.Unique {
			s = "unique" + s
		}
		idx = append(idx, s)
	}
	if len(idx) > 0 {
		b.WriteString("  idx " + strings.Join(idx, " ") + "\n")
	}
}

func target(from, ns, name string) string {
	if ns == "" || ns == from {
		return name
	}
	return ns + "." + name
}

func enumList(v []string) string {
	if len(v) > 20 {
		return strings.Join(v[:20], "|") + "|…"
	}
	return strings.Join(v, "|")
}

func defaultExpr(d string) string {
	if strings.HasPrefix(d, "nextval(") {
		return "auto"
	}
	return clip(d, 40)
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func sameColumns(a, b []string) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Matching returns the objects Schema would describe for pattern/object, or
// every object when both are empty.
func Matching(c *engine.Catalog, pattern, object string) []*engine.Object {
	if strings.TrimSpace(pattern) == "" && strings.TrimSpace(object) == "" {
		var out []*engine.Object
		for _, ns := range c.Namespaces {
			out = append(out, ns.Objects...)
		}
		return out
	}
	return match(c, SchemaOpts{Pattern: pattern, Object: object})
}
