package utils

import (
	"encoding/binary"
	"unsafe"
)

type UintType interface {
	~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uint
}

func MakeChunk[T UintType](v T) []byte {
	switch n := unsafe.Sizeof(v); n {
	case 1:
		return []byte{byte(v)}
	case 2:
		b := make([]byte, 2)
		binary.LittleEndian.PutUint16(b, uint16(v))
		return b
	case 4:
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, uint32(v))
		return b
	case 8:
		b := make([]byte, 8)
		binary.LittleEndian.PutUint64(b, uint64(v))
		return b
	}
	panic("unreachable")
}
