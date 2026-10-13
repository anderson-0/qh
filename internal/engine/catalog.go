package engine

import "strings"

// Kind is the type of a schema object.
type Kind string

const (
	KindTable      Kind = "table"
	KindView       Kind = "view"
	KindMatView    Kind = "matview"
	KindCollection Kind = "collection"
)

// Catalog describes a database's schema in engine-neutral terms.
type Catalog struct {
	Namespaces []Namespace
}

// Namespace is a schema (Postgres), database (MySQL) or attached file (SQLite).
type Namespace struct {
	Name    string
	Objects []*Object
	// Note is a short reason the namespace is incomplete, such as
	// "not listed: <reason>". It is empty for a normal namespace.
	Note string
}

// Object is a table, view or collection.
type Object struct {
	Namespace    string
	Name         string
	Kind         Kind
	Comment      string
	RowEstimate  int64 // -1 when unknown
	Fields       []Field
	PrimaryKey   []string
	ForeignKeys  []ForeignKey // outgoing
	ReferencedBy []ForeignKey // incoming; filled by Link
	Indexes      []Index
}

// Field is a column, or an inferred document field.
type Field struct {
	Name     string
	Type     string
	Default  string
	Comment  string
	Nullable bool
	Enum     []string
	Inferred bool // sampled from documents rather than declared
}

// ForeignKey links Columns of Namespace.Object to RefColumns of RefNamespace.RefObject.
type ForeignKey struct {
	Namespace    string
	Object       string
	Columns      []string
	RefNamespace string
	RefObject    string
	RefColumns   []string
}

// Index is a secondary index.
type Index struct {
	Name    string
	Columns []string
	Unique  bool
}

// Link fills ReferencedBy from every object's ForeignKeys. It is idempotent.
func (c *Catalog) Link() {
	for _, ns := range c.Namespaces {
		for _, o := range ns.Objects {
			o.ReferencedBy = nil
		}
	}
	for _, ns := range c.Namespaces {
		for _, o := range ns.Objects {
			for _, fk := range o.ForeignKeys {
				if t := c.find(fk.RefNamespace, fk.RefObject); t != nil {
					t.ReferencedBy = append(t.ReferencedBy, fk)
				}
			}
		}
	}
}

func (c *Catalog) find(ns, name string) *Object {
	for _, n := range c.Namespaces {
		if n.Name != ns {
			continue
		}
		for _, o := range n.Objects {
			if o.Name == name {
				return o
			}
		}
	}
	return nil
}

// Lookup finds objects by "name" or "namespace.name", ignoring case.
func (c *Catalog) Lookup(ref string) []*Object {
	ns, name, qualified := strings.Cut(ref, ".")
	if !qualified {
		ns, name = "", ref
	}
	var out []*Object
	for _, n := range c.Namespaces {
		if ns != "" && !strings.EqualFold(n.Name, ns) {
			continue
		}
		for _, o := range n.Objects {
			if strings.EqualFold(o.Name, name) {
				out = append(out, o)
			}
		}
	}
	return out
}

// Count returns the number of objects.
func (c *Catalog) Count() int {
	n := 0
	for _, ns := range c.Namespaces {
		n += len(ns.Objects)
	}
	return n
}

// HasNotes reports whether any namespace carries a Note.
func (c *Catalog) HasNotes() bool {
	for _, ns := range c.Namespaces {
		if ns.Note != "" {
			return true
		}
	}
	return false
}

// Only returns the catalog limited to the named namespaces (ignoring case).
// An empty list keeps everything and returns c itself.
func (c *Catalog) Only(namespaces []string) *Catalog {
	if len(namespaces) == 0 {
		return c
	}
	keep := map[string]bool{}
	for _, n := range namespaces {
		keep[strings.ToLower(n)] = true
	}
	out := &Catalog{}
	for _, ns := range c.Namespaces {
		if keep[strings.ToLower(ns.Name)] {
			out.Namespaces = append(out.Namespaces, ns)
		}
	}
	return out
}
