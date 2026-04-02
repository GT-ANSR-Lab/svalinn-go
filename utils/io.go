package utils

import (
	"io"
	"net"
	"unsafe"
)

// Convert any structure object to a byte slice
func ToBytes[T any](v *T) []byte {
	size := unsafe.Sizeof(*v)
	return unsafe.Slice((*byte)(unsafe.Pointer(v)), int(size))
}

// Read exactly len(buf) bytes from conn into buf
func ReadFull(conn net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := conn.Read(buf[total:])
		if err != nil {
			if err == io.EOF {
				return total, io.ErrUnexpectedEOF
			}
			return total, err
		}
		total += n
	}
	return total, nil
}

// Write exactly len(buf) bytes from buf into conn
func WriteFull(conn net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := conn.Write(buf[total:])
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}
