package codec

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
)

const frameMagic = 0x4E54 // "NT"

// Frame represents a framed binary payload.
type Frame struct {
	Kind    uint8
	Payload []byte
}

// EncodeFrame serializes a Frame with magic header, kind, length, payload, and CRC32 checksum.
func EncodeFrame(f Frame) ([]byte, error) {
	if len(f.Payload) > 0xFFFF {
		return nil, errors.New("payload too large")
	}
	buf := make([]byte, 2+1+2+len(f.Payload)+4)
	binary.BigEndian.PutUint16(buf[0:2], frameMagic)
	buf[2] = f.Kind
	binary.BigEndian.PutUint16(buf[3:5], uint16(len(f.Payload)))
	copy(buf[5:], f.Payload)
	sum := crc32.ChecksumIEEE(buf[:5+len(f.Payload)])
	binary.BigEndian.PutUint32(buf[5+len(f.Payload):], sum)
	return buf, nil
}

// DecodeFrame parses and validates a binary frame.
func DecodeFrame(data []byte) (Frame, error) {
	if len(data) < 9 {
		return Frame{}, errors.New("frame too short")
	}
	magic := binary.BigEndian.Uint16(data[0:2])
	if magic != frameMagic {
		return Frame{}, errors.New("invalid magic")
	}
	kind := data[2]
	length := int(binary.BigEndian.Uint16(data[3:5]))
	if len(data) != 5+length+4 {
		return Frame{}, errors.New("length mismatch")
	}
	wantCRC := binary.BigEndian.Uint32(data[5+length:])
	gotCRC := crc32.ChecksumIEEE(data[:5+length])
	if wantCRC != gotCRC {
		return Frame{}, errors.New("crc mismatch")
	}
	payload := make([]byte, length)
	copy(payload, data[5:5+length])
	return Frame{Kind: kind, Payload: payload}, nil
}
