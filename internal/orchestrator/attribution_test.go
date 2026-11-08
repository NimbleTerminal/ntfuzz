package orchestrator

import (
	"bytes"
	"strings"
	"testing"
)

const nt3dMotivatingStack = `panic: runtime error: slice bounds out of range [::-1]
github.com/qmuntal/gltf.(*Decoder).decodeBuffer
    /var/lib/ntfuzz/.cache/go-mod/github.com/qmuntal/gltf@v0.29.0/decoder.go:151
github.com/qmuntal/gltf.(*Decoder).Decode
    /var/lib/ntfuzz/.cache/go-mod/github.com/qmuntal/gltf@v0.29.0/decoder.go:74
nimbleterminal.dev/nt3d/mesh/gltf.Decode
    /var/lib/ntfuzz/.ntfuzz/repos/nt3d/mesh/gltf/gltf.go:108`

const nt3dLocalPanicStack = `panic: runtime error: index out of range [2] with length 2
goroutine 19 [running]:
runtime/debug.Stack()
	/usr/local/go/src/runtime/debug/stack.go:26 +0x64
testing.tRunner.func1()
	/usr/local/go/src/testing/testing.go:1632 +0x230
panic({0x1029a4b60?, 0x14000192048?})
	/usr/local/go/src/runtime/panic.go:785 +0x124
nimbleterminal.dev/nt3d/mesh/gltf.Decode(0x14000180000)
	/var/lib/ntfuzz/.ntfuzz/repos/nt3d/mesh/gltf/gltf.go:42 +0x9c
nimbleterminal.dev/nt3d/mesh/gltf.FuzzDecode.func1(0x1400015c1a0, {0x14000190000, 0x10, 0x10})
	/var/lib/ntfuzz/.ntfuzz/repos/nt3d/mesh/gltf/gltf_test.go:19 +0x58`

func TestAttributeStack(t *testing.T) {
	tests := []struct {
		name          string
		stack         string
		repoDir       string
		projectModule string
		want          StackAttribution
	}{
		{
			name:          "motivating nt3d crash blames github.com/qmuntal/gltf",
			stack:         nt3dMotivatingStack,
			repoDir:       "/var/lib/ntfuzz/.ntfuzz/repos/nt3d",
			projectModule: "nimbleterminal.dev/nt3d",
			want: StackAttribution{
				Module:  "github.com/qmuntal/gltf",
				Version: "v0.29.0",
				File:    "decoder.go",
				Line:    151,
			},
		},
		{
			name:          "motivating nt3d crash after scrubbing still blames dependency",
			stack:         ScrubStack(nt3dMotivatingStack, "/var/lib/ntfuzz/.ntfuzz/repos/nt3d", "nimbleterminal.dev/nt3d", "/var/lib/ntfuzz/.cache/go-mod"),
			repoDir:       "/var/lib/ntfuzz/.ntfuzz/repos/nt3d",
			projectModule: "nimbleterminal.dev/nt3d",
			want: StackAttribution{
				Module:  "github.com/qmuntal/gltf",
				Version: "v0.29.0",
				File:    "decoder.go",
				Line:    151,
			},
		},
		{
			name:          "project-local panic leaves dependency blame empty",
			stack:         nt3dLocalPanicStack,
			repoDir:       "/var/lib/ntfuzz/.ntfuzz/repos/nt3d",
			projectModule: "nimbleterminal.dev/nt3d",
			want:          StackAttribution{},
		},
		{
			name: "project frame before dependency frame blames project",
			stack: `panic: bad callback
nimbleterminal.dev/nt3d/mesh/gltf.customReader.Read
	/var/lib/ntfuzz/.ntfuzz/repos/nt3d/mesh/gltf/gltf.go:55
github.com/qmuntal/gltf.(*Decoder).Decode
	/var/lib/ntfuzz/.cache/go-mod/github.com/qmuntal/gltf@v0.29.0/decoder.go:74`,
			repoDir:       "/var/lib/ntfuzz/.ntfuzz/repos/nt3d",
			projectModule: "nimbleterminal.dev/nt3d",
			want:          StackAttribution{},
		},
		{
			name: "escaped uppercase module path in modcache",
			stack: `panic: unexpected EOF
github.com/BurntSushi/toml.Decode
	/go/pkg/mod/github.com/!burnt!sushi/toml@v1.4.0/decode.go:88
example.com/app.FuzzConfig
	/work/app/config_test.go:14`,
			repoDir:       "/work/app",
			projectModule: "example.com/app",
			want: StackAttribution{
				Module:  "github.com/BurntSushi/toml",
				Version: "v1.4.0",
				File:    "decode.go",
				Line:    88,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AttributeStack(tc.stack, tc.repoDir, tc.projectModule)
			if got != tc.want {
				t.Fatalf("AttributeStack() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestScrubStackAndFingerprintInvariance(t *testing.T) {
	repoDir := "/var/lib/ntfuzz/.ntfuzz/repos/nt3d"
	modulePath := "nimbleterminal.dev/nt3d"
	goModCache := "/var/lib/ntfuzz/.cache/go-mod"

	scrubbed := ScrubStack(nt3dMotivatingStack, repoDir, modulePath, goModCache)
	if strings.Contains(scrubbed, "/var/lib/ntfuzz") {
		t.Fatalf("ScrubStack left server-local path in stack:\n%s", scrubbed)
	}
	if !strings.Contains(scrubbed, "github.com/qmuntal/gltf@v0.29.0/decoder.go:151") {
		t.Fatalf("ScrubStack missing normalized dependency frame:\n%s", scrubbed)
	}
	if !strings.Contains(scrubbed, "nimbleterminal.dev/nt3d/mesh/gltf/gltf.go:108") {
		t.Fatalf("ScrubStack missing normalized project module frame:\n%s", scrubbed)
	}

	fpRaw := FingerprintStack(nt3dMotivatingStack)
	fpScrubbed := FingerprintStack(scrubbed)
	if fpRaw == "" || fpRaw != fpScrubbed {
		t.Fatalf("FingerprintStack mismatch between raw (%q) and scrubbed (%q)", fpRaw, fpScrubbed)
	}

	// Verify GOROOT frames remain untouched while project frames are scrubbed.
	scrubbedLocal := ScrubStack(nt3dLocalPanicStack, repoDir, modulePath, goModCache)
	if !strings.Contains(scrubbedLocal, "/usr/local/go/src/runtime/panic.go:785") {
		t.Fatalf("ScrubStack modified GOROOT frame:\n%s", scrubbedLocal)
	}
	if !strings.Contains(scrubbedLocal, "nimbleterminal.dev/nt3d/mesh/gltf/gltf.go:42") {
		t.Fatalf("ScrubStack did not replace repoDir with modulePath:\n%s", scrubbedLocal)
	}
	if FingerprintStack(nt3dLocalPanicStack) != FingerprintStack(scrubbedLocal) {
		t.Fatalf("FingerprintStack changed on project-local panic stack")
	}
}

func TestDependencySourceURL(t *testing.T) {
	tests := []struct {
		name    string
		module  string
		version string
		file    string
		line    int
		want    string
	}{
		{
			name:    "github v0 module",
			module:  "github.com/qmuntal/gltf",
			version: "v0.29.0",
			file:    "decoder.go",
			line:    151,
			want:    "https://github.com/qmuntal/gltf/blob/v0.29.0/decoder.go#L151",
		},
		{
			name:    "github v2 major suffix stripped",
			module:  "github.com/pelletier/go-toml/v2",
			version: "v2.2.3",
			file:    "unmarshaler.go",
			line:    42,
			want:    "https://github.com/pelletier/go-toml/blob/v2.2.3/unmarshaler.go#L42",
		},
		{
			name:    "github submodule with v3 suffix",
			module:  "github.com/org/repo/subpkg/v3",
			version: "v3.1.0",
			file:    "parse.go",
			line:    19,
			want:    "https://github.com/org/repo/blob/v3.1.0/subpkg/parse.go#L19",
		},
		{
			name:    "github pseudo-version uses commit sha",
			module:  "github.com/org/repo",
			version: "v0.0.0-20261015123456-abcdef123456",
			file:    "internal/lex.go",
			line:    77,
			want:    "https://github.com/org/repo/blob/abcdef123456/internal/lex.go#L77",
		},
		{
			name:    "gitlab module",
			module:  "gitlab.com/ group/proj",
			version: "v1.0.2",
			file:    "pkg/run.go",
			line:    10,
			want:    "https://gitlab.com/ group/proj/blob/v1.0.2/pkg/run.go#L10",
		},
		{
			name:    "non-github/gitlab uses pkg.go.dev",
			module:  "golang.org/x/net",
			version: "v0.30.0",
			file:    "html/parse.go",
			line:    210,
			want:    "https://pkg.go.dev/golang.org/x/net@v0.30.0",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DependencySourceURL(tc.module, tc.version, tc.file, tc.line); got != tc.want {
				t.Fatalf("DependencySourceURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCapCrashInput(t *testing.T) {
	small := []byte("go test fuzz v1\n[]byte(\"hello\")\n")
	capped, truncated := CapCrashInput(small)
	if truncated || !bytes.Equal(capped, small) {
		t.Fatalf("expected small input untouched, got truncated=%v len=%d", truncated, len(capped))
	}

	large := bytes.Repeat([]byte("A"), MaxCrashInputBytes+512)
	cappedLarge, truncatedLarge := CapCrashInput(large)
	if !truncatedLarge || len(cappedLarge) != MaxCrashInputBytes {
		t.Fatalf("expected large input capped at %d with truncated=true, got len=%d truncated=%v",
			MaxCrashInputBytes, len(cappedLarge), truncatedLarge)
	}
}
