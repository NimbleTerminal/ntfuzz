package tui

import (
	"encoding/binary"
	"math"
	"strings"
	"testing"
	"unicode/utf8"
)

func FuzzFormatAndRenderHelpers(f *testing.F) {
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0x50, 0x40}, 8, 12500.0, "ok", false, "abcdef1234567890", true, "FuzzTargetName", 10)
	f.Add([]byte{}, 0, -5.0, "crash", true, "日本語テスト文字列", false, "short", 0)
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f}, 1000, math.NaN(), "unknown", false, "", false, "🔥🚀✨🎉", 2)

	f.Fuzz(func(t *testing.T, rawPoints []byte, width int, rate float64, status string, isRunning bool, sha string, dirty bool, text string, maxLen int) {
		if len(rawPoints) > 1024 || len(status) > 1024 || len(sha) > 1024 || len(text) > 1024 {
			return
		}

		points := make([]float64, 0, len(rawPoints)/8)
		for i := 0; i+8 <= len(rawPoints); i += 8 {
			bits := binary.LittleEndian.Uint64(rawPoints[i : i+8])
			points = append(points, math.Float64frombits(bits))
		}

		trend := RenderCoverageTrend(points, width)
		if trend == "" {
			t.Fatalf("RenderCoverageTrend returned empty string")
		}

		rateStr := FormatExecRate(rate)
		if strings.Contains(rateStr, "NaN") || strings.Contains(rateStr, "Inf") {
			t.Fatalf("FormatExecRate(%v) returned %q", rate, rateStr)
		}

		badge := RenderStatusBadge(status, isRunning)
		if badge == "" {
			t.Fatalf("RenderStatusBadge returned empty string")
		}

		rev := formatRevision(sha, dirty)
		if utf8.ValidString(sha) && !utf8.ValidString(rev) {
			t.Fatalf("formatRevision(%q, %t) produced invalid UTF-8 %q", sha, dirty, rev)
		}

		trunc := truncate(text, maxLen)
		if utf8.ValidString(text) && !utf8.ValidString(trunc) {
			t.Fatalf("truncate(%q, %d) produced invalid UTF-8 %q", text, maxLen, trunc)
		}
	})
}
