package parser

import "testing"

func FuzzParseKV(f *testing.F) {
	f.Add("host=localhost;port=8080")
	f.Add("# comment\nname=\"ntfuzz\";workers=4")
	f.Add("empty=")

	f.Fuzz(func(t *testing.T, input string) {
		m, err := ParseKV(input)
		if err != nil {
			return
		}
		for k := range m {
			if k == "" {
				t.Fatalf("parsed empty key from %q", input)
			}
		}
	})
}

func FuzzEvalSum(f *testing.F) {
	f.Add("1 + 2 + 3")
	f.Add("100 - 45 + 12")
	f.Add("0")

	f.Fuzz(func(t *testing.T, expr string) {
		_, _ = EvalSum(expr)
	})
}
