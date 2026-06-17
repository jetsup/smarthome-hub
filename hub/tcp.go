package hub

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
)

var (
	tcpConnMap = make(map[string]net.Conn)
	tcpConnMu  sync.Mutex
	// fallback single TCP connection for legacy usage in this file
	tcpConn net.Conn
)

// getTCPConn returns the TCP connection for a given gateway ID.
func getTCPConn(gatewayID string) net.Conn {
	tcpConnMu.Lock()
	defer tcpConnMu.Unlock()
	return tcpConnMap[gatewayID]
}

// setTCPConn stores the TCP connection for a gateway.
func setTCPConn(gatewayID string, conn net.Conn) {
	tcpConnMu.Lock()
	defer tcpConnMu.Unlock()
	tcpConnMap[gatewayID] = conn
}

// removeTCPConn removes the TCP connection for a gateway.
func removeTCPConn(gatewayID string) {
	tcpConnMu.Lock()
	defer tcpConnMu.Unlock()
	delete(tcpConnMap, gatewayID)
}

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
		tcpConnMu.Lock()
		tcpConn = conn
		tcpConnMu.Unlock()

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
				tcpConnMu.Lock()
				tcpConn = nil
				tcpConnMu.Unlock()
				break handleLoop
			}

			if buf[0] != 0xAA {
				log.Printf("Bad header 0x%02X from gateway %s", buf[0], gw.ID)
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

			switch buf[1] {
			case 1: // Telemetry / status
				NetworkRegistry[deviceID] = DeviceState{
					DeviceID: deviceID,
					Value:    value,
				}
				ReportNode(gw.ID, deviceID, value)

			case 3: // Discovery broadcast from unprovisioned node
				log.Printf("Discovery from device %d via gateway %s", deviceID, gw.ID)
				AddDiscoveredNode(gw.ID, deviceID)
			}
		}
	}
}

func SendCommand(deviceId uint32, msgType uint8, value uint16) error {
	tcpConnMu.Lock()
	conn := tcpConn
	tcpConnMu.Unlock()

	if conn == nil {
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

	_, err := conn.Write(buf)
	return err
}

// SendProvision sends a provisioning command to the gateway with a node API key.
// Format: BB:<deviceId>:<apiKey>\n
func SendProvision(deviceId uint32, apiKey string) error {
	tcpConnMu.Lock()
	conn := tcpConn
	tcpConnMu.Unlock()

	if conn == nil {
		return fmt.Errorf("gateway is not connected")
	}

	msg := fmt.Sprintf("BB:%d:%s\n", deviceId, apiKey)
	_, err := conn.Write([]byte(msg))
	return err
}
