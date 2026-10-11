package harness

import "testing"

func TestEqual(t *testing.T) {
	cases := []struct {
		name      string
		want, got [][]any
		ordered   bool
		eq        bool
	}{
		{"number vs numeric string", [][]any{{"4.99"}}, [][]any{{4.99}}, false, true},
		{"float tolerance", [][]any{{190.1}}, [][]any{{190.10000000000002}}, false, true},
		{"different numbers", [][]any{{1000.0}}, [][]any{{999.0}}, false, false},
		{"case and space insensitive", [][]any{{"MARY.SMITH@x.org "}}, [][]any{{"mary.smith@x.org"}}, false, true},
		{"set ignores order", [][]any{{"a"}, {"b"}}, [][]any{{"b"}, {"a"}}, false, true},
		{"ordered respects order", [][]any{{"a"}, {"b"}}, [][]any{{"b"}, {"a"}}, true, false},
		{"multiset counts", [][]any{{"a"}, {"a"}}, [][]any{{"a"}, {"b"}}, false, false},
		{"row count differs", [][]any{{"a"}}, [][]any{{"a"}, {"a"}}, false, false},
		{"width differs", [][]any{{"a", 1}}, [][]any{{"a"}}, false, false},
		{"nulls", [][]any{{nil}}, [][]any{{nil}}, false, true},
		{"null vs string", [][]any{{nil}}, [][]any{{"null"}}, false, false},
		{"integer as float", [][]any{{int64(1000)}}, [][]any{{1000.0}}, false, true},
		{"big relative tolerance", [][]any{{67416.51}}, [][]any{{67416.51000000001}}, false, true},
	}
	for _, c := range cases {
		if got := Equal(c.want, c.got, c.ordered); got != c.eq {
			t.Errorf("%s: Equal = %v, want %v", c.name, got, c.eq)
		}
	}
}
