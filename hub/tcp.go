package hub

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	tcpConnMap = make(map[string]net.Conn)
	tcpConnMu  sync.Mutex

	ackCh   = make(map[uint32]chan struct{})
	ackChMu sync.Mutex

	announceIDMap = make(map[uint32]string)
	announceIDMu  sync.RWMutex
)

func setAnnounceIDMap(announceID uint32, gatewayID string) {
	announceIDMu.Lock()
	defer announceIDMu.Unlock()
	announceIDMap[announceID] = gatewayID
}

func getGatewayIDByAnnounceID(announceID uint32) string {
	announceIDMu.RLock()
	defer announceIDMu.RUnlock()
	return announceIDMap[announceID]
}

// expectACK registers a channel that will be closed when an ACK:cmd:<deviceID> arrives.
func expectACK(deviceID uint32) <-chan struct{} {
	ch := make(chan struct{})
	ackChMu.Lock()
	ackCh[deviceID] = ch
	ackChMu.Unlock()
	return ch
}

// signalACK closes the channel for the given deviceID, unblocking any waiter.
func signalACK(deviceID uint32) {
	ackChMu.Lock()
	ch, ok := ackCh[deviceID]
	if ok {
		delete(ackCh, deviceID)
	}
	ackChMu.Unlock()
	if ok {
		close(ch)
	}
}

func getTCPConn(gatewayID string) net.Conn {
	tcpConnMu.Lock()
	defer tcpConnMu.Unlock()
	return tcpConnMap[gatewayID]
}

func setTCPConn(gatewayID string, conn net.Conn) {
	tcpConnMu.Lock()
	defer tcpConnMu.Unlock()
	tcpConnMap[gatewayID] = conn
}

func removeTCPConn(gatewayID string) {
	tcpConnMu.Lock()
	defer tcpConnMu.Unlock()
	delete(tcpConnMap, gatewayID)
}

// clearACKsForGateway unblocks all pending ACK waiters for nodes on this gateway.
// Called when a gateway disconnects unexpectedly so DeleteGateway doesn't hang.
func clearACKsForGateway(gatewayID string) {
	var nodes []Node
	DB.Where("gateway_id = ?", gatewayID).Find(&nodes)
	ackChMu.Lock()
	for _, n := range nodes {
		if ch, ok := ackCh[n.DeviceID]; ok {
			delete(ackCh, n.DeviceID)
			close(ch)
		}
	}
	ackChMu.Unlock()
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

		if tc, ok := conn.(*net.TCPConn); ok {
			tc.SetKeepAlive(true)
			tc.SetKeepAlivePeriod(15 * time.Second)
		}
		DB.Model(&gw).Updates(map[string]any{"is_online": true, "last_seen": time.Now()})
		setTCPConn(gw.ID, conn)

		LogAudit(nil, "gateway_connected", "gateway", &gw.ID, "Gateway connected from "+conn.RemoteAddr().String())
		log.Printf("Gateway %s (%s) authenticated — API key %s", gw.ID, gw.Name, maskKey(apiKey))

		// Execute any pending actions for this gateway (e.g. queued deletions)
		executePendingActions(gw.ID)
		go resendPendingCaps(gw.ID)
		go pushTimeToGateway(gw.ID)

		go handleGatewayConnection(gw.ID, conn, reader)
	}
}

func handleGatewayConnection(gatewayID string, conn net.Conn, reader *bufio.Reader) {
	defer func() {
		// Only mark offline if we're still the active connection for this gateway.
		// A newer connection may have already taken over after a reconnect.
		if current := getTCPConn(gatewayID); current == conn {
			removeTCPConn(gatewayID)
			DB.Model(&Gateway{}).Where("id = ?", gatewayID).Update("is_online", false)
			LogAudit(nil, "gateway_disconnected", "gateway", &gatewayID, "Gateway disconnected")
			// Unblock any DeleteGateway that's waiting on ACKs from this gateway
			clearACKsForGateway(gatewayID)
		}
	}()

	for {
		// Peek at first byte to distinguish packet types
		peek, err := reader.Peek(1)
		if err != nil {
			log.Printf("Gateway %s disconnected: %v", gatewayID, err)
			return
		}

		if peek[0] == 0xAA {
			// ESP-NOW packet: 9 bytes for the classic types, or a length-prefixed frame for type >= 8.
			head := make([]byte, 2)
			if _, err := io.ReadFull(reader, head); err != nil {
				log.Printf("Gateway %s read error: %v", gatewayID, err)
				return
			}
			if head[1] >= 8 {
				rest := make([]byte, 5) // devId[4] + len
				if _, err := io.ReadFull(reader, rest); err != nil {
					return
				}
				plen := int(rest[4])
				payload := make([]byte, plen+1) // + xor
				if _, err := io.ReadFull(reader, payload); err != nil {
					return
				}
				frame := append(append(append([]byte{}, head...), rest...), payload[:plen]...)
				if xorOf(frame) != payload[plen] {
					log.Printf("Checksum mismatch (type %d) from gateway %s", head[1], gatewayID)
					continue
				}
				touchGateway(gatewayID)
				deviceID := binary.LittleEndian.Uint32(rest[0:4])
				handleVariableFrame(gatewayID, head[1], deviceID, payload[:plen])
				continue
			}
			buf := make([]byte, 9)
			copy(buf, head)
			if _, err := io.ReadFull(reader, buf[2:]); err != nil {
				log.Printf("Gateway %s read error: %v", gatewayID, err)
				return
			}

			if buf[0] != 0xAA {
				log.Printf("Bad header 0x%02X from gateway %s", buf[0], gatewayID)
				continue
			}

			calcXor := uint8(0)
			for i := 0; i < 8; i++ {
				calcXor ^= buf[i]
			}
			if calcXor != buf[8] {
				log.Printf("Checksum mismatch from gateway %s", gatewayID)
				continue
			}

			deviceID := binary.LittleEndian.Uint32(buf[2:6])
			value := binary.LittleEndian.Uint16(buf[6:8])

			// Update gateway last_seen (throttled: telemetry can arrive many times a second)
			touchGateway(gatewayID)

			switch buf[1] {
			case 1: // Telemetry
				registrySet(DeviceState{DeviceID: deviceID, Value: value})
				ReportNode(gatewayID, deviceID, value)

			case 3: // Discovery
				deviceType := uint8(0)
				if len(buf) >= 8 {
					deviceType = uint8(buf[6]) // value low byte → device type
				}
				log.Printf("Discovery: device %d type %d via gateway %s", deviceID, deviceType, gatewayID)
				AddDiscoveredNode(gatewayID, deviceID)
				// If we already have a pending provision for this device, update its type
				addPendingDeviceType(deviceID, deviceType)

			default:
				log.Printf("Unknown msgType %d from device %d via gateway %s", buf[1], deviceID, gatewayID)
			}
		} else {
			// Text message (ACK, etc.) — read until newline
			line, err := reader.ReadString('\n')
			if err != nil {
				log.Printf("Gateway %s read error (text): %v", gatewayID, err)
				return
			}
			line = strings.TrimSpace(line)
			// Update gateway last_seen on any incoming message
			DB.Model(&Gateway{}).Where("id = ?", gatewayID).Update("last_seen", time.Now())
			log.Printf("TEXT from gateway %s: %s", gatewayID, line)

			// Parse ACK messages and signal any waiting goroutines
			if strings.HasPrefix(line, "ACK:") {
				parts := strings.SplitN(line, ":", 3)
				if len(parts) >= 3 {
					ackType := parts[1]
					ackDevice := parts[2]
					log.Printf("ACK %s for device %s from gateway %s", ackType, ackDevice, gatewayID)
					if ackType == "cmd" {
						if devID, err := strconv.ParseUint(ackDevice, 10, 32); err == nil {
							signalACK(uint32(devID))
						}
					}
				}
			}

			// Parse announce ID from gateway — GWID:<decimal_id>
			if strings.HasPrefix(line, "GWID:") {
				idStr := strings.TrimPrefix(line, "GWID:")
				if id, err := strconv.ParseUint(idStr, 10, 32); err == nil {
					setAnnounceIDMap(uint32(id), gatewayID)
					log.Printf("Gateway %s announce ID: %d", gatewayID, id)

					// Tell the gateway its real name so it announces itself
					// correctly to HMIs (they match gateways by name).
					var gwRow Gateway
					if DB.Select("name").First(&gwRow, "id = ?", gatewayID).Error == nil && gwRow.Name != "" {
						clean := strings.NewReplacer("\n", " ", "\r", " ").Replace(gwRow.Name)
						if err := SendRawTextToGateway(gatewayID, "GWNAME:"+clean); err != nil {
							log.Printf("GWNAME to gateway %s failed: %v", gatewayID, err)
						}
					}
				}
			}

			// Credential sync from gateway. New firmware sends JSON (safe for any
			// SSID characters): CREDSJ:{"nets":[{"s":"ssid","p":"pass"},...]} in
			// priority order. Old firmware sends CREDS:<n>:<ssid>:<pass>:...
			if strings.HasPrefix(line, "CREDSJ:") {
				var payload struct {
					Nets []wifiNet `json:"nets"`
				}
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "CREDSJ:")), &payload); err == nil {
					applyGatewayCreds(gatewayID, payload.Nets)
				} else {
					log.Printf("Gateway %s sent bad CREDSJ: %v", gatewayID, err)
				}
			} else if strings.HasPrefix(line, "CREDS:") {
				parts := strings.SplitN(line, ":", 4)
				if len(parts) >= 2 {
					rest := ""
					if len(parts) >= 4 {
						rest = parts[3]
					} else if len(parts) >= 3 {
						rest = parts[2]
					}
					count, err := strconv.Atoi(parts[1])
					if err == nil && count > 0 && rest != "" {
						pairs := strings.Split(rest, ":")
						var nets []wifiNet
						for i := 0; i+1 < len(pairs) && i/2 < count; i += 2 {
							nets = append(nets, wifiNet{S: pairs[i], P: pairs[i+1]})
						}
						applyGatewayCreds(gatewayID, nets)
					}
				}
			}

			// The gateway applied the list we pushed: the hub copy is now in sync.
			if strings.HasPrefix(line, "ACK:wifiset") {
				DB.Model(&Gateway{}).Where("id = ?", gatewayID).Update("wifi_dirty", false)
			}
		}
	}
}

// SendCommand broadcasts a command packet to all connected gateways.
func SendCommand(deviceId uint32, msgType uint8, value uint16) error {
	buf := packCommand(deviceId, msgType, value)

	tcpConnMu.Lock()
	defer tcpConnMu.Unlock()

	if len(tcpConnMap) == 0 {
		return fmt.Errorf("no gateways connected")
	}

	log.Printf("SendCommand broadcast: device %d type %d value %d to %d gateway(s)", deviceId, msgType, value, len(tcpConnMap))
	var lastErr error
	for gid, conn := range tcpConnMap {
		if _, err := conn.Write(buf); err != nil {
			log.Printf("SendCommand to gateway %s failed: %v", gid, err)
			lastErr = err
		}
	}
	return lastErr
}

// SendCommandToGateway sends a command packet to a specific gateway.
func SendCommandToGateway(gatewayID string, deviceId uint32, msgType uint8, value uint16) error {
	conn := getTCPConn(gatewayID)
	if conn == nil {
		return fmt.Errorf("gateway %s is not connected", gatewayID)
	}
	buf := packCommand(deviceId, msgType, value)
	log.Printf("SendCommand to gateway %s: device %d type %d value %d", gatewayID, deviceId, msgType, value)
	_, err := conn.Write(buf)
	return err
}

// SendPinCommandToGateway sends a per-pin command to a specific gateway.
// Uses msgType=6 with pinIndex and value encoded in the 9-byte packet:
//
//	byte 6 = (pinIndex << 4) | (value >> 8)
//	byte 7 = value & 0xFF
func SendPinCommandToGateway(gatewayID string, deviceId uint32, pinIndex uint8, value uint16) error {
	rememberOutput(gatewayID, deviceId, pinIndex, value) // so a restarted node can be put back as it was
	conn := getTCPConn(gatewayID)
	if conn == nil {
		return fmt.Errorf("gateway %s is not connected", gatewayID)
	}
	buf := packPinCommand(deviceId, pinIndex, value)
	log.Printf("SendPinCommand to gateway %s: device %d pin %d value %d", gatewayID, deviceId, pinIndex, value)
	_, err := conn.Write(buf)
	return err
}

// SendProvision sends a BB: provisioning command to a specific gateway.
// Format: BB:deviceId:apiKey:gatewayId:deviceType:nodeName:capCount:type1:pin1:extra1:label1:...\n
func SendProvision(gatewayID string, deviceId uint32, apiKey string, deviceType uint8, nodeName string, caps []CapabilityConfig) error {
	conn := getTCPConn(gatewayID)
	if conn == nil {
		return fmt.Errorf("gateway %s is not connected", gatewayID)
	}
	capsStr := compactCapabilities(caps)
	msg := fmt.Sprintf("BB:%d:%s:%s:%d:%s:%s\n", deviceId, apiKey, gatewayID, deviceType, nodeName, capsStr)
	log.Printf("SendProvision to gateway %s: device %d key %s type %d name=%s caps=%s", gatewayID, deviceId, maskKey(apiKey), deviceType, nodeName, capsStr)
	_, err := conn.Write([]byte(msg))
	return err
}

// SendRawTextToGateway sends a raw text command to a specific gateway.
// Format: any text string terminated by \n (the function adds the \n).
func SendRawTextToGateway(gatewayID string, text string) error {
	conn := getTCPConn(gatewayID)
	if conn == nil {
		return fmt.Errorf("gateway %s is not connected", gatewayID)
	}
	msg := text + "\n"
	log.Printf("SendRawText to gateway %s: %s", gatewayID, text)
	_, err := conn.Write([]byte(msg))
	return err
}

func packCommand(deviceId uint32, msgType uint8, value uint16) []byte {
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
	return buf
}

func packPinCommand(deviceId uint32, pin uint8, value uint16) []byte {
	buf := make([]byte, 9)
	buf[0] = 0xAA
	buf[1] = 6 // MSG_PIN_CMD
	binary.LittleEndian.PutUint32(buf[2:6], deviceId)
	buf[6] = pin
	// Wire format carries one byte (matches the node's 8-bit PWM); clamp, don't wrap.
	if value > 255 {
		value = 255
	}
	buf[7] = uint8(value)
	calcXor := uint8(0)
	for i := 0; i < 8; i++ {
		calcXor ^= buf[i]
	}
	buf[8] = calcXor
	return buf
}

var (
	gwTouch   = map[string]time.Time{}
	gwTouchMu sync.Mutex
)

// touchGateway records gateway liveness at most once every 2 seconds.
func touchGateway(gatewayID string) {
	gwTouchMu.Lock()
	last := gwTouch[gatewayID]
	due := time.Since(last) >= 2*time.Second
	if due {
		gwTouch[gatewayID] = time.Now()
	}
	gwTouchMu.Unlock()
	if due {
		DB.Model(&Gateway{}).Where("id = ?", gatewayID).Update("last_seen", time.Now())
	}
}
