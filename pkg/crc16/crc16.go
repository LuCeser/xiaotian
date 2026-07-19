// Package crc16 implements CRC-16 algorithms.
// This provides the CRC-16-CCITT variant as specified in ITU-T V.41
// for use in the WebSocket protocol.
package crc16

const (
	// CCITT is the CRC-16-CCITT polynomial (0x1021)
	CCITT = 0x1021
)

// Checksum computes the CRC-16-CCITT checksum of data.
// Polynomial: 0x1021, Initial: 0xFFFF, Reflected: No, Final XOR: 0x0000
func Checksum(data []byte) uint16 {
	crc := uint16(0xFFFF) // Initial value
	for _, b := range data {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ CCITT
			} else {
				crc = crc << 1
			}
		}
	}
	return crc
}
