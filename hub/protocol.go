package hub

import (
	"encoding/binary"
	"fmt"
	"log"
)

// Wire protocol between nodes, gateway and hub.
//
//	Fixed 9-byte frames (types 1-7):  AA type devId[4] value[2] xor
//	Variable frames   (types >= 8):  AA type devId[4] len payload[len] xor
//
// Variable frames carry multi-value sensor readings and rich actuator actions, so new
// modules never need a new frame layout: they add channels / actions to the catalog.
const (
	msgTelemetry = 1
	msgCommand   = 2
	msgDiscovery = 3
	msgScanReq   = 4
	msgProvision = 5
	msgPinCmd    = 6

	msgReadings = 8  // node -> hub: repeated {capIdx, channel, int32 value}
	msgAction   = 9  // hub  -> node: {capIdx, action, args...}
	msgTime     = 11 // gateway -> nodes: {epoch uint32, source}
	msgIdent    = 13 // node -> hub: the node's MAC address
	msgBoot     = 12 // node -> hub: "I just (re)started" - the hub restores outputs
)

func xorOf(b []byte) byte {
	var x byte
	for _, v := range b {
		x ^= v
	}
	return x
}

// packFrame builds a variable-length frame.
func packFrame(msgType byte, deviceID uint32, payload []byte) []byte {
	b := make([]byte, 0, 8+len(payload))
	b = append(b, 0xAA, msgType)
	b = binary.LittleEndian.AppendUint32(b, deviceID)
	b = append(b, byte(len(payload)))
	b = append(b, payload...)
	return append(b, xorOf(b))
}

// packAction builds an actuator action for capability slot capIdx.
func packAction(deviceID uint32, capIdx, action byte, args []byte) []byte {
	p := append([]byte{capIdx, action}, args...)
	return packFrame(msgAction, deviceID, p)
}

type reading struct {
	Cap     int
	Channel int
	Value   int32
}

// parseReadings decodes a msgReadings payload.
func parseReadings(p []byte) ([]reading, error) {
	if len(p)%6 != 0 {
		return nil, fmt.Errorf("readings payload of %d bytes is not a multiple of 6", len(p))
	}
	out := make([]reading, 0, len(p)/6)
	for i := 0; i+6 <= len(p); i += 6 {
		out = append(out, reading{Cap: int(p[i]), Channel: int(p[i+1]), Value: int32(binary.LittleEndian.Uint32(p[i+2 : i+6]))})
	}
	return out, nil
}

func sendFrameToGateway(gatewayID string, frame []byte) error {
	conn := getTCPConn(gatewayID)
	if conn == nil {
		return fmt.Errorf("gateway %s is not connected", gatewayID)
	}
	log.Printf("frame to gateway %s: type %d dev %d len %d", gatewayID, frame[1], binary.LittleEndian.Uint32(frame[2:6]), len(frame))
	_, err := conn.Write(frame)
	return err
}
