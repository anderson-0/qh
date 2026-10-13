package mongodb

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/anderson-0/qh/internal/engine"
)

const (
	sampleSize    = 100
	maxPathDepth  = 4
	maxPaths      = 200
	maxEnumValues = 8
	minEnumDocs   = 50
	refSamples    = 5
	maxEnumLen    = 40 // a longer sampled value means free text, not a set of values
)

// inferred is what sampling found about one collection.
type inferred struct {
	fields  []engine.Field
	comment string                     // "…(+N fields)" when paths were cut
	samples map[string][]bson.RawValue // up to refSamples distinct scalar values per path
	types   map[string]string          // the dominant type per path
}

type pathStat struct {
	types   map[string]int  // per document; an array counts as "array"
	elems   map[string]int  // element types of the path's arrays, across documents
	present int             // documents where the path holds a non-null value
	strs    map[string]bool // distinct strings, up to maxEnumValues+1
	nonStr  bool
	long    bool // a string longer than maxEnumLen was seen
	samples []bson.RawValue
	seen    map[string]bool // sample dedup by raw bytes
}

// infer summarizes sampled documents: dotted paths to depth 4, arrays of
// documents as path[].field, type unions by frequency, nullability, sampled
// string values, and a few distinct values per path for reference checks.
// Paths keep the order in which they were first seen.
func infer(docs []bson.Raw) *inferred {
	stats := map[string]*pathStat{}
	var order []string
	stat := func(p string) *pathStat {
		s := stats[p]
		if s == nil {
			s = &pathStat{types: map[string]int{}, elems: map[string]int{}, strs: map[string]bool{}, seen: map[string]bool{}}
			stats[p] = s
			order = append(order, p)
		}
		return s
	}
	for _, d := range docs {
		present := map[string]bool{}
		walk(d, "", 1, stat, present)
		for p := range present {
			stats[p].present++
		}
	}
	out := &inferred{samples: map[string][]bson.RawValue{}, types: map[string]string{}}
	for i, p := range order {
		if i == maxPaths {
			out.comment = fmt.Sprintf("…(+%d fields)", len(order)-maxPaths)
			break
		}
		s := stats[p]
		types := s.rendered()
		typ := union(types)
		if typ == "" {
			typ = "null"
		}
		f := engine.Field{Name: p, Type: typ, Nullable: s.present < len(docs), Inferred: true}
		if !s.nonStr && !s.long && s.types["string"] > 0 && s.present >= minEnumDocs && len(s.strs) <= maxEnumValues {
			for v := range s.strs {
				f.Enum = append(f.Enum, v)
			}
			sort.Strings(f.Enum)
			f.Enum = append(f.Enum, "…")
		}
		out.fields = append(out.fields, f)
		out.samples[p] = s.samples
		out.types[p] = dominant(types)
	}
	return out
}

// rendered returns the path's types with "array" spelled out as
// array<element union>, or plain "array" when no element was ever seen.
func (s *pathStat) rendered() map[string]int {
	n, ok := s.types["array"]
	if !ok || len(s.elems) == 0 {
		return s.types
	}
	out := make(map[string]int, len(s.types))
	for t, c := range s.types {
		out[t] = c
	}
	delete(out, "array")
	out["array<"+union(s.elems)+">"] = n
	return out
}

func walk(d bson.Raw, prefix string, depth int, stat func(string) *pathStat, present map[string]bool) {
	elems, _ := d.Elements()
	for _, e := range elems {
		p := prefix + e.Key()
		v := e.Value()
		s := stat(p)
		if v.Type == bson.TypeNull || v.Type == bson.TypeUndefined {
			continue
		}
		present[p] = true
		if v.Type == bson.TypeArray {
			s.types["array"]++
			vals, _ := v.Array().Values()
			for _, x := range vals {
				if x.Type != bson.TypeNull && x.Type != bson.TypeUndefined {
					s.elems[typeName(x)]++
				}
			}
		} else {
			s.types[typeName(v)]++
		}
		if v.Type == bson.TypeString {
			str := v.StringValue()
			if utf8.RuneCountInString(str) > maxEnumLen {
				s.long = true
			}
			if len(s.strs) <= maxEnumValues {
				s.strs[str] = true
			}
		} else {
			s.nonStr = true
		}
		switch v.Type {
		case bson.TypeEmbeddedDocument:
			if depth < maxPathDepth {
				walk(v.Document(), p+".", depth+1, stat, present)
			}
		case bson.TypeArray:
			if depth < maxPathDepth {
				vals, _ := v.Array().Values()
				for _, x := range vals {
					if x.Type == bson.TypeEmbeddedDocument {
						walk(x.Document(), p+"[].", depth+1, stat, present)
					}
				}
			}
		default:
			if len(s.samples) < refSamples && !s.seen[string(v.Value)] {
				s.seen[string(v.Value)] = true
				s.samples = append(s.samples, v)
			}
		}
	}
}

func typeName(v bson.RawValue) string {
	switch v.Type {
	case bson.TypeString, bson.TypeSymbol:
		return "string"
	case bson.TypeInt32:
		return "int"
	case bson.TypeInt64:
		return "long"
	case bson.TypeDouble:
		return "double"
	case bson.TypeDecimal128:
		return "decimal"
	case bson.TypeBoolean:
		return "bool"
	case bson.TypeDateTime:
		return "date"
	case bson.TypeObjectID:
		return "objectId"
	case bson.TypeEmbeddedDocument:
		return "object"
	case bson.TypeBinary:
		if sub, data := v.Binary(); sub == 4 && len(data) == 16 {
			return "uuid"
		}
		return "binData"
	case bson.TypeRegex:
		return "regex"
	case bson.TypeTimestamp:
		return "timestamp"
	case bson.TypeArray:
		vals, _ := v.Array().Values()
		inner := map[string]int{}
		for _, x := range vals {
			if x.Type != bson.TypeNull && x.Type != bson.TypeUndefined {
				inner[typeName(x)]++
			}
		}
		if len(inner) == 0 {
			return "array"
		}
		return "array<" + union(inner) + ">"
	}
	return strings.ToLower(v.Type.String())
}

// union joins type names by descending frequency, then by name.
func union(types map[string]int) string {
	names := make([]string, 0, len(types))
	for n := range types {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if types[names[i]] != types[names[j]] {
			return types[names[i]] > types[names[j]]
		}
		return names[i] < names[j]
	})
	return strings.Join(names, "|")
}

func dominant(types map[string]int) string {
	first, _, _ := strings.Cut(union(types), "|")
	return first
}
