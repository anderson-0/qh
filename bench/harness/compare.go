package harness

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// cell is a normalised value: a number, a lower-cased trimmed string, or null.
type cell struct {
	null  bool
	isNum bool
	num   float64
	str   string
}

func normalize(v any) cell {
	switch x := v.(type) {
	case nil:
		return cell{null: true}
	case float64:
		return cell{isNum: true, num: x}
	case float32:
		return cell{isNum: true, num: float64(x)}
	case int64:
		return cell{isNum: true, num: float64(x)}
	case int:
		return cell{isNum: true, num: float64(x)}
	case json.Number:
		f, _ := x.Float64()
		return cell{isNum: true, num: f}
	case bool:
		return cell{str: strconv.FormatBool(x)}
	case time.Time:
		return cell{str: strings.ToLower(x.UTC().Format(time.RFC3339))}
	case string:
		s := strings.TrimSpace(x)
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return cell{isNum: true, num: f}
		}
		return cell{str: strings.ToLower(s)}
	}
	return cell{str: strings.ToLower(strings.TrimSpace(fmt.Sprint(v)))}
}

func (a cell) equal(b cell) bool {
	switch {
	case a.null || b.null:
		return a.null && b.null
	case a.isNum && b.isNum:
		scale := math.Max(1, math.Max(math.Abs(a.num), math.Abs(b.num)))
		return math.Abs(a.num-b.num) <= 1e-6*scale
	case a.isNum || b.isNum:
		return false
	}
	return a.str == b.str
}

func rowEqual(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !normalize(a[i]).equal(normalize(b[i])) {
			return false
		}
	}
	return true
}

// Equal compares answer rows with the ground truth: numbers within a
// relative 1e-6, strings trimmed and case-insensitive, rows as a multiset
// unless ordered.
func Equal(want, got [][]any, ordered bool) bool {
	if len(want) != len(got) {
		return false
	}
	if ordered {
		for i := range want {
			if !rowEqual(want[i], got[i]) {
				return false
			}
		}
		return true
	}
	used := make([]bool, len(got))
	for _, w := range want {
		found := false
		for j, g := range got {
			if !used[j] && rowEqual(w, g) {
				used[j], found = true, true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
