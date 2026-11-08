package hub

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm/clause"
)

// Readings that gateways store while the hub is unreachable (and send once it is
// back) land here, stamped with the time they were measured, not received.

type DataPoint struct {
	ID        uint      `gorm:"primaryKey" json:"-"`
	GatewayID string    `gorm:"size:16;not null;uniqueIndex:idx_dp,priority:1" json:"gatewayId"`
	DeviceID  uint32    `gorm:"not null;uniqueIndex:idx_dp,priority:2;index:idx_dp_dev" json:"deviceId"`
	Cap       uint8     `gorm:"uniqueIndex:idx_dp,priority:3" json:"cap"`
	Channel   uint8     `gorm:"uniqueIndex:idx_dp,priority:4" json:"channel"`
	At        time.Time `gorm:"not null;uniqueIndex:idx_dp,priority:5;index:idx_dp_at" json:"at"`
	Kind      uint8     `json:"kind"`
	Value     int32     `json:"value"`
	Estimated bool      `json:"estimated"` // time worked out afterwards (the gateway had no clock)
}

// What the gateway reports about itself, shown in the gateway settings.
type GatewayLogStatus struct {
	Pending    int       `json:"pending"`    // records still on the gateway
	SdCard     bool      `json:"sdCard"`     // storing on the SD card (else flash)
	MaxKB      int       `json:"maxKb"`      // space limit in effect
	Rtc        int       `json:"rtc"`        // 0 none, 1 found but not set, 2 working
	TimeSource int       `json:"timeSource"` // 0 none (uptime), 1 rtc, 2 hub, 3 internet
	Uploaded   int       `json:"uploaded"`   // records sent since the gateway started
	UpdatedAt  time.Time `json:"updatedAt"`
}

var (
	logStatMu sync.Mutex
	logStat   = map[string]GatewayLogStatus{}
)

func writeGatewayText(gatewayID, text string) error {
	conn := getTCPConn(gatewayID)
	if conn == nil {
		return fmt.Errorf("gateway %s is not connected", gatewayID)
	}
	_, err := conn.Write([]byte(text + "\n"))
	return err
}

// LOG:<seq>:<n>:<epoch>,<est>,<dev>,<cap>,<ch>,<kind>,<val>;...
func handleGatewayLog(gatewayID, line string) {
	parts := strings.SplitN(line, ":", 4)
	if len(parts) < 4 {
		return
	}
	seq := parts[1]
	rows := []DataPoint{}
	for _, rec := range strings.Split(parts[3], ";") {
		f := strings.Split(rec, ",")
		if len(f) != 7 {
			continue
		}
		ep, e1 := strconv.ParseInt(f[0], 10, 64)
		dev, e2 := strconv.ParseUint(f[2], 10, 32)
		cp, e3 := strconv.ParseUint(f[3], 10, 8)
		ch, e4 := strconv.ParseUint(f[4], 10, 8)
		kd, e5 := strconv.ParseUint(f[5], 10, 8)
		val, e6 := strconv.ParseInt(f[6], 10, 32)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || e6 != nil {
			continue
		}
		if ep < 1600000000 || ep > time.Now().Unix()+86400 {
			continue // nonsense time: drop rather than pollute the history
		}
		rows = append(rows, DataPoint{GatewayID: gatewayID, DeviceID: uint32(dev), Cap: uint8(cp), Channel: uint8(ch),
			At: time.Unix(ep, 0).UTC(), Kind: uint8(kd), Value: int32(val), Estimated: f[1] == "1"})
	}
	if len(rows) > 0 {
		// A batch that is sent again (lost acknowledgement) must not store twice.
		if err := DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error; err != nil {
			log.Printf("data log from gateway %s not stored: %v", gatewayID, err)
			return // no acknowledgement: the gateway keeps the records and retries
		}
	}
	_ = writeGatewayText(gatewayID, "LOGACK:"+seq)
}

// LOGSTAT:<pending>:<sd>:<maxKb>:<rtc>:<timeSource>:<uploaded>
func handleGatewayLogStat(gatewayID, line string) {
	f := strings.Split(strings.TrimPrefix(line, "LOGSTAT:"), ":")
	if len(f) < 6 {
		return
	}
	n := func(i int) int { v, _ := strconv.Atoi(f[i]); return v }
	logStatMu.Lock()
	logStat[gatewayID] = GatewayLogStatus{Pending: n(0), SdCard: n(1) == 1, MaxKB: n(2), Rtc: n(3), TimeSource: n(4), Uploaded: n(5), UpdatedAt: time.Now()}
	logStatMu.Unlock()
}

// sendLogConfig tells a gateway where to keep its log and which pins its RTC / SD card use.
func sendLogConfig(gatewayID string) {
	var gw Gateway
	if DB.First(&gw, "id = ?", gatewayID).Error != nil {
		return
	}
	store, kb, cs, sda, scl := gw.LogStore, gw.LogMaxKB, gw.SdCS, gw.RtcSda, gw.RtcScl
	if kb == 0 {
		kb = 256
	}
	if cs == 0 {
		cs = 5
	}
	if sda == 0 {
		sda = 21
	}
	if scl == 0 {
		scl = 22
	}
	_ = writeGatewayText(gatewayID, fmt.Sprintf("LOGCFG:%d:%d:%d:%d:%d", store, kb, cs, sda, scl))
}

func loggingResponse(gw Gateway) gin.H {
	kb, cs, sda, scl := gw.LogMaxKB, gw.SdCS, gw.RtcSda, gw.RtcScl
	if kb == 0 {
		kb = 256
	}
	if cs == 0 {
		cs = 5
	}
	if sda == 0 {
		sda = 21
	}
	if scl == 0 {
		scl = 22
	}
	logStatMu.Lock()
	st, have := logStat[gw.ID]
	logStatMu.Unlock()
	if getTCPConn(gw.ID) == nil {
		have = false // status is only meaningful while the gateway is connected
	}
	var stored int64
	DB.Model(&DataPoint{}).Where("gateway_id = ?", gw.ID).Count(&stored)
	out := gin.H{"store": gw.LogStore, "maxKb": kb, "sdCs": cs, "rtcSda": sda, "rtcScl": scl, "storedOnHub": stored}
	if have {
		out["status"] = st
	}
	return out
}

// GET /api/gateways/:id/logging
func GetGatewayLogging(c *gin.Context) {
	gw, ok := ownedGateway(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, loggingResponse(gw))
}

// PUT /api/gateways/:id/logging  {store, maxKb, sdCs, rtcSda, rtcScl}
func SetGatewayLogging(c *gin.Context) {
	gw, ok := ownedGateway(c)
	if !ok {
		return
	}
	var req struct {
		Store  int `json:"store"`
		MaxKB  int `json:"maxKb"`
		SdCs   int `json:"sdCs"`
		RtcSda int `json:"rtcSda"`
		RtcScl int `json:"rtcScl"`
	}
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}
	if req.Store != 0 && req.Store != 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Store must be flash or SD card"})
		return
	}
	maxKB := 1024 // the gateway's flash has room for about 1 MB of log
	if req.Store == 1 {
		maxKB = 1048576
	}
	if req.MaxKB < 32 || req.MaxKB > maxKB {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Space limit must be 32 to %d KB", maxKB)})
		return
	}
	for _, p := range []struct {
		n string
		v int
	}{{"SD chip-select", req.SdCs}, {"RTC SDA", req.RtcSda}, {"RTC SCL", req.RtcScl}} {
		if p.v < 0 || p.v > 39 || (p.v >= 6 && p.v <= 11) {
			c.JSON(http.StatusBadRequest, gin.H{"error": p.n + " pin is not usable"})
			return
		}
	}
	if req.RtcSda == req.RtcScl {
		c.JSON(http.StatusBadRequest, gin.H{"error": "RTC SDA and SCL must be different pins"})
		return
	}
	if req.Store == 1 {
		for _, p := range []int{18, 19, 23, req.RtcSda, req.RtcScl} {
			if p == req.SdCs {
				c.JSON(http.StatusBadRequest, gin.H{"error": "The SD chip-select pin clashes with another pin"})
				return
			}
		}
	}
	DB.Model(&gw).Updates(map[string]interface{}{"log_store": req.Store, "log_max_kb": req.MaxKB, "sd_cs": req.SdCs, "rtc_sda": req.RtcSda, "rtc_scl": req.RtcScl})
	LogAudit(c, "gateway_logging", "gateway", &gw.ID, fmt.Sprintf("Data logging on %s: %s, %d KB", gw.Name, map[int]string{0: "flash", 1: "SD card"}[req.Store], req.MaxKB))
	sent := true
	if err := writeGatewayText(gw.ID, fmt.Sprintf("LOGCFG:%d:%d:%d:%d:%d", req.Store, req.MaxKB, req.SdCs, req.RtcSda, req.RtcScl)); err != nil {
		sent = false
	}
	DB.First(&gw, "id = ?", gw.ID)
	resp := loggingResponse(gw)
	resp["applied"] = sent
	c.JSON(http.StatusOK, resp)
}

// GET /api/nodes/:nodeId/history?cap=&channel=&hours=24&limit=500
func GetNodeHistory(c *gin.Context) {
	var node Node
	if err := DB.Joins("JOIN gateways ON gateways.id = nodes.gateway_id").
		Where("nodes.node_id = ? AND gateways.user_id = ?", c.Param("nodeId"), c.GetUint("userID")).
		First(&node).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Node not found"})
		return
	}
	capIdx, err1 := strconv.Atoi(c.Query("cap"))
	ch, _ := strconv.Atoi(c.DefaultQuery("channel", "0"))
	if err1 != nil || capIdx < 0 || capIdx > 255 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cap is required"})
		return
	}
	hours, _ := strconv.Atoi(c.DefaultQuery("hours", "24"))
	if hours < 1 || hours > 24*365 {
		hours = 24
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "500"))
	if limit < 1 || limit > 5000 {
		limit = 500
	}
	scale := 1.0
	unit := ""
	caps := parseCapabilitiesConfig(node.CapabilitiesConfig)
	if capIdx < len(caps) {
		if ct := capTypeByKey(caps[capIdx].Type); ct != nil {
			if chd := ct.channel(ch); chd != nil {
				unit = chd.Unit
				if chd.Scale != 0 {
					scale = chd.Scale
				}
			}
		}
	}
	start := time.Now().Add(-time.Duration(hours) * time.Hour)
	q := DB.Where("gateway_id = ? AND device_id = ? AND cap = ? AND channel = ? AND at > ?", node.GatewayID, node.DeviceID, capIdx, ch, start)

	type pt struct {
		T         int64    `json:"t"`
		V         float64  `json:"v"`
		Lo        *float64 `json:"lo,omitempty"` // lowest / highest value inside a bucket
		Hi        *float64 `json:"hi,omitempty"`
		Estimated bool     `json:"estimated,omitempty"`
	}

	// buckets=N: average the whole window into N time slices, so a week of data is still one chart.
	if buckets, _ := strconv.Atoi(c.Query("buckets")); buckets >= 2 {
		if buckets > 600 {
			buckets = 600
		}
		var rows []DataPoint
		q.Order("at ASC").Limit(200000).Find(&rows)
		span := time.Since(start).Seconds()
		type acc struct {
			sum, lo, hi float64
			n           int
			est         bool
		}
		slots := make([]acc, buckets)
		for _, r := range rows {
			i := int(r.At.Sub(start).Seconds() / span * float64(buckets))
			if i < 0 || i >= buckets {
				continue
			}
			v := float64(r.Value) / scale
			a := &slots[i]
			if a.n == 0 || v < a.lo {
				a.lo = v
			}
			if a.n == 0 || v > a.hi {
				a.hi = v
			}
			a.sum += v
			a.n++
			a.est = a.est || r.Estimated
		}
		out := make([]pt, 0, buckets)
		for i, a := range slots {
			if a.n == 0 {
				continue
			}
			lo, hi := a.lo, a.hi
			mid := start.Add(time.Duration((float64(i) + 0.5) / float64(buckets) * span * float64(time.Second)))
			out = append(out, pt{T: mid.Unix(), V: a.sum / float64(a.n), Lo: &lo, Hi: &hi, Estimated: a.est})
		}
		c.JSON(http.StatusOK, gin.H{"unit": unit, "points": out, "from": start.Unix(), "to": time.Now().Unix()})
		return
	}

	var rows []DataPoint
	q.Order("at DESC").Limit(limit).Find(&rows)
	out := make([]pt, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		out = append(out, pt{T: rows[i].At.Unix(), V: float64(rows[i].Value) / scale, Estimated: rows[i].Estimated})
	}
	c.JSON(http.StatusOK, gin.H{"unit": unit, "points": out, "from": start.Unix(), "to": time.Now().Unix()})
}

// StartDataLogPruner keeps the history table from growing without limit.
func StartDataLogPruner() {
	go func() {
		for range time.Tick(6 * time.Hour) {
			DB.Where("at < ?", time.Now().AddDate(0, 0, -365)).Delete(&DataPoint{})
		}
	}()
}
