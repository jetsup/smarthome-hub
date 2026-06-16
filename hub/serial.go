package hub

import (
	"encoding/binary"
	"fmt"
	"log"

	"go.bug.st/serial"
)

func StartSerialWorker(portName string) {
	mode := &serial.Mode{BaudRate: 115200}
	stream, err := serial.Open(portName, mode)
	if err != nil {
		log.Fatalf("Failed to open serial port: %v", err)
	}
	defer stream.Close()

	log.Printf("Listening for verified ESP32 Gateway packets on %s...", portName)

	oneByte := make([]byte, 1)
	payloadBuf := make([]byte, 8) // Read remaining 8 bytes of the 9-byte struct

	for {
		// Hunt for Header
		_, err := stream.Read(oneByte)
		if err != nil {
			continue
		}
		if oneByte[0] != 0xAA {
			continue
		}

		// Read the remaining 8 bytes
		bytesRead := 0
		for bytesRead < 8 {
			n, err := stream.Read(payloadBuf[bytesRead:])
			if err != nil {
				break
			}
			bytesRead += n
		}

		if bytesRead == 8 {
			// Verify Checksum
			calcXor := uint8(0xAA)
			for i := 0; i < 7; i++ {
				calcXor ^= payloadBuf[i]
			}
			receivedChecksum := payloadBuf[7]

			if calcXor != receivedChecksum {
				// Corrupt alignment packet found, drop it silently
				continue
			}

			// Parse Verified Data
			msgType := payloadBuf[0]
			deviceId := binary.LittleEndian.Uint32(payloadBuf[1:5])
			value := binary.LittleEndian.Uint16(payloadBuf[5:7])

			NetworkRegistry[deviceId] = DeviceState{
				DeviceID: deviceId,
				Value:    value,
			}
			fmt.Printf("[Verified Mesh Packet] ID: %d | Header: %02X | Type: %d | Val: %d\n", oneByte[0], deviceId, msgType, value)
		}
	}
}
