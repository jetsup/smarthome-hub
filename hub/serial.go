package hub

import (
	"encoding/binary"
	"fmt"
	"log"

	"go.bug.st/serial"
)

var SerialStream serial.Port

func StartSerialWorker(portName string) {
	mode := &serial.Mode{BaudRate: 115200}
	var err error
	SerialStream, err = serial.Open(portName, mode)
	if err != nil {
		log.Fatalf("Failed to open serial port: %v", err)
	}

	log.Printf("Listening and ready to send commands on %s...", portName)

	oneByte := make([]byte, 1)
	payloadBuf := make([]byte, 8) // Read remaining 8 bytes of the 9-byte struct

	for {
		// Hunt for Header
		_, err := SerialStream.Read(oneByte)
		if err != nil {
			continue
		}
		if oneByte[0] != 0xAA {
			continue
		}

		// Read the remaining 8 bytes
		bytesRead := 0
		for bytesRead < 8 {
			n, err := SerialStream.Read(payloadBuf[bytesRead:])
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
			if calcXor != payloadBuf[7] {
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

// SendCommand writes a structured control frame down the serial line to the ESP32 Gateway
func SendCommand(deviceId uint32, msgType uint8, value uint16) error {
	if SerialStream == nil {
		return fmt.Errorf("serial stream is not initialized")
	}

	// Build the 9-byte packet explicitly
	buf := make([]byte, 9)
	buf[0] = 0xAA // Header
	buf[1] = msgType

	// Inject device ID (Little Endian)
	binary.LittleEndian.PutUint32(buf[2:6], deviceId)
	// Inject command value (Little Endian)
	binary.LittleEndian.PutUint16(buf[6:8], value)

	// Calculate XOR Checksum over the first 8 bytes
	calcXor := uint8(0)
	for i := 0; i < 8; i++ {
		calcXor ^= buf[i]
	}
	buf[8] = calcXor // Append check byte

	// Write directly to the physical USB hardware pipe
	_, err := SerialStream.Write(buf)
	return err
}
