package hub

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
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

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("Failed to accept gateway connection: %v", err)
			continue
		}
		log.Printf("Gateway connected from %s", conn.RemoteAddr())

		// ── Authenticate with API key ──────────────────────────────────────────
		reader := bufio.NewReader(conn)
		line, err := reader.ReadString('\n')
		if err != nil {
			log.Printf("Failed to read API key from %s: %v", conn.RemoteAddr(), err)
			conn.Close()
			continue
		}
		apiKey := strings.TrimSpace(line)

		var gw Gateway
		result := DB.Where("api_key = ?", apiKey).First(&gw)
		if result.Error != nil {
			log.Printf("Invalid API key from %s", conn.RemoteAddr())
			conn.Write([]byte("ERR: invalid api key\n"))
			conn.Close()
			continue
		}

		// Mark gateway online
		DB.Model(&gw).Update("is_online", true)
		tcpConn = conn

		LogAudit(nil, "gateway_connected", "gateway", &gw.ID, "Gateway connected from "+conn.RemoteAddr().String())

		log.Printf("Gateway %s (%s) authenticated", gw.ID, gw.Name)

		// ── Read packets ────────────────────────────────────────────────────────
		buf := make([]byte, 9)

	handleLoop:
		for {
			_, err := io.ReadFull(reader, buf)
			if err != nil {
				log.Printf("Gateway %s disconnected: %v", gw.ID, err)
				DB.Model(&gw).Update("is_online", false)
				LogAudit(nil, "gateway_disconnected", "gateway", &gw.ID, "Gateway disconnected")
				tcpConn = nil
				break handleLoop
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

			deviceID := binary.LittleEndian.Uint32(buf[2:6])
			value := binary.LittleEndian.Uint16(buf[6:8])

			NetworkRegistry[deviceID] = DeviceState{
				DeviceID: deviceID,
				Value:    value,
			}

			ReportNode(gw.ID, deviceID, value)
		}
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
