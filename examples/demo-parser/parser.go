package parser

import (
	"errors"
	"strconv"
	"strings"
)

// ParseKV parses lines of "key=value" pairs separated by ';' or '\n'.
func ParseKV(input string) (map[string]string, error) {
	out := make(map[string]string)
	for _, token := range strings.FieldsFunc(input, func(r rune) bool {
		return r == ';' || r == '\n'
	}) {
		token = strings.TrimSpace(token)
		if token == "" || strings.HasPrefix(token, "#") {
			continue
		}
		k, v, ok := strings.Cut(token, "=")
		if !ok {
			return nil, errors.New("missing '=' separator")
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" {
			return nil, errors.New("empty key")
		}
		if strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`) && len(v) >= 2 {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out, nil
}

// EvalSum evaluates a simple '+' / '-' integer expression like "10 + 25 - 3".
func EvalSum(expr string) (int64, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return 0, nil
	}
	var total int64
	var sign int64 = 1
	var numBuf strings.Builder

	flush := func() error {
		if numBuf.Len() == 0 {
			return errors.New("expected operand")
		}
		v, err := strconv.ParseInt(numBuf.String(), 10, 64)
		if err != nil {
			return err
		}
		total += sign * v
		numBuf.Reset()
		return nil
	}

	for _, r := range expr {
		switch {
		case r == ' ' || r == '\t':
			continue
		case r == '+':
			if err := flush(); err != nil {
				return 0, err
			}
			sign = 1
		case r == '-':
			if err := flush(); err != nil {
				return 0, err
			}
			sign = -1
		case r >= '0' && r <= '9':
			numBuf.WriteRune(r)
		default:
			return 0, errors.New("unexpected character")
		}
	}
	if err := flush(); err != nil {
		return 0, err
	}
	return total, nil
}
