// Package suggest finds names close to an unknown identifier.
package suggest

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/anderson-0/qh/internal/engine"
)

// Distance is the case-insensitive Levenshtein distance between a and b.
func Distance(a, b string) int {
	ra := []rune(strings.ToLower(a))
	rb := []rune(strings.ToLower(b))
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// Candidate is a name to compare (Key) and the text to show for it (Label).
type Candidate struct {
	Key   string
	Label string
}

type scored struct {
	key, label string
	d          int
}

func rank(target string, cands []Candidate) []scored {
	out := make([]scored, len(cands))
	for i, c := range cands {
		out[i] = scored{c.Key, c.Label, Distance(target, c.Key)}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].d != out[j].d {
			return out[i].d < out[j].d
		}
		return out[i].label < out[j].label
	})
	return out
}

// Closest returns up to n labels whose keys are near target, best first. A key
// qualifies when its edit distance is at most max(2, len(target)/3), or when
// one name contains the other (both at least 3 characters).
func Closest(target string, cands []Candidate, n int) []string {
	t := strings.ToLower(target)
	tl := utf8.RuneCountInString(t)
	limit := max(2, tl/3)
	var out []string
	seen := map[string]bool{}
	for _, s := range rank(target, cands) {
		k := strings.ToLower(s.key)
		kl := utf8.RuneCountInString(k)
		near := s.d <= limit || tl >= 3 && kl >= 3 && (strings.Contains(k, t) || strings.Contains(t, k))
		if near && !seen[s.label] {
			seen[s.label] = true
			out = append(out, s.label)
			if len(out) == n {
				break
			}
		}
	}
	return out
}

// ColumnHint explains an unknown column. It names the closest column in the
// tables the query references and lists their columns, nearest first. With
// no known tables it searches the whole catalog.
func ColumnHint(c *engine.Catalog, column string, tables []string) string {
	col := column
	if i := strings.LastIndex(col, "."); i >= 0 {
		col = col[i+1:]
	}
	var objs []*engine.Object
	seen := map[*engine.Object]bool{}
	for _, t := range tables {
		for _, o := range c.Lookup(t) {
			if !seen[o] && len(objs) < 4 {
				seen[o] = true
				objs = append(objs, o)
			}
		}
	}
	if len(objs) == 0 {
		var all []Candidate
		for _, ns := range c.Namespaces {
			for _, o := range ns.Objects {
				for _, f := range o.Fields {
					all = append(all, Candidate{Key: f.Name, Label: o.Name + "." + f.Name})
				}
			}
		}
		if top := Closest(col, all, 5); len(top) > 0 {
			return "Did you mean " + strings.Join(top, ", ") + "?"
		}
		return ""
	}
	var parts, best []string
	for _, o := range objs {
		fields := make([]Candidate, len(o.Fields))
		for i, f := range o.Fields {
			fields[i] = Candidate{Key: f.Name, Label: f.Name}
		}
		if top := Closest(col, fields, 1); len(top) > 0 {
			best = append(best, o.Name+"."+top[0])
		}
		ranked := rank(col, fields)
		names := make([]string, 0, len(ranked))
		for _, r := range ranked {
			names = append(names, r.label)
		}
		more := ""
		if len(names) > 8 {
			names, more = names[:8], ", …"
		}
		parts = append(parts, o.Name+" has: "+strings.Join(names, ", ")+more)
	}
	hint := strings.Join(parts, "; ")
	if len(best) > 0 {
		hint = "Did you mean " + strings.Join(best, ", ") + "? " + hint
	}
	return hint
}

// TableHint explains an unknown table by naming the closest ones.
func TableHint(c *engine.Catalog, table string) string {
	name := table
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	qualify := len(c.Namespaces) > 1
	var all []Candidate
	for _, ns := range c.Namespaces {
		for _, o := range ns.Objects {
			label := o.Name
			if qualify {
				label = o.Namespace + "." + o.Name
			}
			all = append(all, Candidate{Key: o.Name, Label: label})
		}
	}
	if top := Closest(name, all, 5); len(top) > 0 {
		return "Did you mean " + strings.Join(top, ", ") + "?"
	}
	return "Use the schema tool to list tables"
}
