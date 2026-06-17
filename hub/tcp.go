package hub

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
)

var (
	tcpConn net.Conn
)

func StartTCPWorker(addr string) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Failed to listen on TCP %s: %v", addr, err)
	}
	defer listener.Close()

	log.Printf("TCP server listening on %s", addr)

	conn, err := listener.Accept()
	if err != nil {
		log.Fatalf("Failed to accept gateway connection: %v", err)
	}
	tcpConn = conn
	log.Printf("Gateway connected from %s", conn.RemoteAddr())

	buf := make([]byte, 9)

	for {
		_, err := io.ReadFull(conn, buf)
		if err != nil {
			log.Printf("Gateway disconnected: %v. Waiting for reconnection...", err)
			tcpConn = nil
			conn, err = listener.Accept()
			if err != nil {
				log.Printf("Failed to accept new gateway: %v", err)
				continue
			}
			tcpConn = conn
			log.Printf("Gateway reconnected from %s", conn.RemoteAddr())
			continue
		}

		if buf[0] != 0xAA {
			continue
		}

		calcXor := uint8(0)
		for i := 0; i < 8; i++ {
			calcXor ^= buf[i]
		}
		if calcXor != buf[8] {
			continue
		}

		msgType := buf[1]
		deviceId := binary.LittleEndian.Uint32(buf[2:6])
		value := binary.LittleEndian.Uint16(buf[6:8])

		NetworkRegistry[deviceId] = DeviceState{
			DeviceID: deviceId,
			Value:    value,
		}

		fmt.Printf("[Verified Mesh Packet] ID: %d | Type: %d | Val: %d\n", deviceId, msgType, value)
	}
}

func SendCommand(deviceId uint32, msgType uint8, value uint16) error {
	if tcpConn == nil {
		return fmt.Errorf("gateway is not connected")
	}

	buf := make([]byte, 9)
	buf[0] = 0xAA
	buf[1] = msgType
	binary.LittleEndian.PutUint32(buf[2:6], deviceId)
	binary.LittleEndian.PutUint16(buf[6:8], value)

	calcXor := uint8(0)
	for i := 0; i < 8; i++ {
		calcXor ^= buf[i]
	}
	buf[8] = calcXor

	_, err := tcpConn.Write(buf)
	return err
}
