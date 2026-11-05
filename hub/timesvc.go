package hub

import (
	"fmt"
	"log"
	"sync"
	"time"
)

// System time. A DS3231 module, when present, is the authority; otherwise the hub's
// own clock. The hub pushes the time to every gateway so gateways and nodes (and later
// the task scheduler) share one clock even when the internet is unavailable.

var (
	timeMu      sync.Mutex
	rtcOffset   time.Duration // rtc - hub clock at the last reading
	rtcSeenAt   time.Time
	rtcDeviceID uint32
	rtcNodeID   string
)

const rtcFresh = 5 * time.Minute

func noteRTC(node Node, epoch int64) {
	if epoch < 1_600_000_000 { // before 2020 = unset clock
		return
	}
	timeMu.Lock()
	rtcOffset = time.Unix(epoch, 0).Sub(time.Now())
	rtcSeenAt = time.Now()
	rtcDeviceID = node.DeviceID
	rtcNodeID = node.NodeID
	timeMu.Unlock()
}

// systemNow is the system clock: the RTC if it reported recently, else the hub clock.
func systemNow() (t time.Time, source string) {
	timeMu.Lock()
	defer timeMu.Unlock()
	if !rtcSeenAt.IsZero() && time.Since(rtcSeenAt) < rtcFresh {
		return time.Now().Add(rtcOffset), "rtc"
	}
	return time.Now(), "hub"
}

func timeStatus() map[string]interface{} {
	now, src := systemNow()
	timeMu.Lock()
	defer timeMu.Unlock()
	out := map[string]interface{}{"epoch": now.Unix(), "iso": now.UTC().Format(time.RFC3339), "source": src}
	if !rtcSeenAt.IsZero() {
		out["rtcNodeId"] = rtcNodeID
		out["rtcLastSeen"] = rtcSeenAt
		out["driftSeconds"] = rtcOffset.Seconds()
	}
	return out
}

// pushTimeToGateway tells a gateway the current system time (TIME:<epoch>:<source>).
func pushTimeToGateway(gatewayID string) {
	now, src := systemNow()
	if err := SendRawTextToGateway(gatewayID, fmt.Sprintf("TIME:%d:%s", now.Unix(), src)); err != nil {
		log.Printf("time push to gateway %s failed: %v", gatewayID, err)
	}
}

// StartTimeSync keeps every connected gateway's clock fresh.
func StartTimeSync() {
	go func() {
		for range time.Tick(2 * time.Minute) {
			tcpConnMu.Lock()
			ids := make([]string, 0, len(tcpConnMap))
			for id := range tcpConnMap {
				ids = append(ids, id)
			}
			tcpConnMu.Unlock()
			for _, id := range ids {
				pushTimeToGateway(id)
			}
		}
	}()
}
