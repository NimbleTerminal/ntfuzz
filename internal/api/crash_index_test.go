package api

import (
	"strings"
	"testing"
	"time"
)

func TestCrashIndexHTML(t *testing.T) {
	lastSeen := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	page := renderCrashIndexHTML([]Crash{{
		ID:            23,
		ProjectName:   "nt3d",
		TargetName:    "FuzzDecode",
		LastSeen:      lastSeen,
		Stack:         "--- FAIL: FuzzDecode\npanic: slice bounds out of range",
		BlamedModule:  "github.com/qmuntal/gltf",
		BlamedVersion: "v0.29.0",
		BlamedFile:    "decoder.go",
		BlamedLine:    151,
	}}, true)
	for _, want := range []string{
		`<main>`,
		`<h1>Open crashes</h1>`,
		`1 open crash`,
		`Read-only snapshot`,
		`href="/crashes/23"`,
		`Crash #23 — nt3d / FuzzDecode`,
		`panic: slice bounds out of range`,
		`github.com/qmuntal/gltf@v0.29.0 · decoder.go:151`,
		`datetime="2026-11-08T12:00:00Z"`,
		`>Open crash report</a>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("crash index missing %q:\n%s", want, page)
		}
	}

	empty := renderCrashIndexHTML(nil, false)
	if !strings.Contains(empty, "0 open crashes") || !strings.Contains(empty, "No open crashes") {
		t.Fatalf("empty crash index is not explicit:\n%s", empty)
	}
}

func TestCrashIndexHTMLEscapesCrashFields(t *testing.T) {
	page := renderCrashIndexHTML([]Crash{{
		ID:          1,
		ProjectName: `<img src=x onerror=alert(1)>`,
		TargetName:  `Fuzz<script>`,
		Stack:       `panic: <script>alert(1)</script>`,
	}}, false)
	if strings.Contains(page, "<img src=x") || strings.Contains(page, "Fuzz<script>") || strings.Contains(page, "panic: <script>") {
		t.Fatalf("crash index contains unescaped data:\n%s", page)
	}
}
