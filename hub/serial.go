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

	log.Printf("Listening for synchronized ESP32 Gateway packets on %s...", portName)

	oneByte := make([]byte, 1)
	payloadBuf := make([]byte, 7) // Remaining 7 bytes of our 8-byte struct

	for {
		//Hunt for the Magic Byte (0xAA)
		_, err := stream.Read(oneByte)
		if err != nil {
			continue
		}
		if oneByte[0] != 0xAA {
			continue // Drop byte and keep hunting
		}

		//Read the remaining 7 bytes sequentially
		bytesRead := 0
		for bytesRead < 7 {
			n, err := stream.Read(payloadBuf[bytesRead:])
			if err != nil {
				break
			}
			bytesRead += n
		}

		//Parse only if we successfully grabbed a full frame
		if bytesRead == 7 {
			msgType := payloadBuf[0]
			deviceId := binary.LittleEndian.Uint32(payloadBuf[1:5])
			value := binary.LittleEndian.Uint16(payloadBuf[5:7])

			// Update device state cleanly
			NetworkRegistry[deviceId] = DeviceState{
				DeviceID: deviceId,
				Value:    value,
			}
			fmt.Printf("[Synced Mesh Packet] ID: %d | Type: %d | Val: %d\n", deviceId, msgType, value)
		}
	}
}
