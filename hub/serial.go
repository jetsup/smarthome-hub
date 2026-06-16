package hub

import (
	"encoding/binary"
	"fmt"
	"go.bug.st/serial" // Updated import
	"log"
)

func StartSerialWorker(portName string) {
	// Set up the connection configuration
	mode := &serial.Mode{
		BaudRate: 115200,
	}

	stream, err := serial.Open(portName, mode)
	if err != nil {
		log.Fatalf("Failed to open serial port: %v", err)
	}
	defer stream.Close()

	log.Printf("Listening for ESP32 Gateway packets on %s...", portName)

	// Buffer size matching our packed C-Struct byte payload footprint
	buf := make([]byte, 7)

	for {
		n, err := stream.Read(buf)
		if err != nil {
			log.Printf("Serial read error: %v", err)
			continue
		}

		if n == 7 { // Simple validation to ensure a full packet arrived
			msgType := buf[0]
			deviceId := binary.LittleEndian.Uint32(buf[1:5])
			value := binary.LittleEndian.Uint16(buf[5:7])

			// Dynamically register or update device state
			NetworkRegistry[deviceId] = DeviceState{
				DeviceID: deviceId,
				Value:    value,
			}
			fmt.Printf("[Mesh Packet] Type: %d | ID: %d | Val: %d\n", msgType, deviceId, value)
		}
	}
}
