package codec

import (
	"bytes"
	"testing"
)

func FuzzFrameRoundTrip(f *testing.F) {
	f.Add(uint8(1), []byte("hello ntfuzz"))
	f.Add(uint8(2), []byte{0x00, 0xff, 0x10, 0x20})

	f.Fuzz(func(t *testing.T, kind uint8, payload []byte) {
		if len(payload) > 4096 {
			return
		}
		raw, err := EncodeFrame(Frame{Kind: kind, Payload: payload})
		if err != nil {
			t.Fatalf("EncodeFrame error: %v", err)
		}
		decoded, err := DecodeFrame(raw)
		if err != nil {
			t.Fatalf("DecodeFrame error: %v", err)
		}
		if decoded.Kind != kind || !bytes.Equal(decoded.Payload, payload) {
			t.Fatalf("round-trip mismatch: got %+v", decoded)
		}
	})
}

func FuzzDecodeArbitrary(f *testing.F) {
	seed, _ := EncodeFrame(Frame{Kind: 7, Payload: []byte("seed")})
	f.Add(seed)
	f.Add([]byte{0x4E, 0x54, 0x01, 0x00, 0x02, 0xAA, 0xBB, 0x00, 0x00, 0x00, 0x00})

	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = DecodeFrame(raw)
	})
}
