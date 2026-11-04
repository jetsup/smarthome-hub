package hub

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type WifiCredentialRequest struct {
	SSID       string `json:"ssid" binding:"required"`
	Password   string `json:"password"`
	ConnectNow bool   `json:"connectNow"` // also tell the gateway to switch to it immediately
}

type wifiOrderRequest struct {
	IDs []uint `json:"ids" binding:"required"`
}

type UpdateGatewayRequest struct {
	Name string `json:"name" binding:"required"`
}

// wifiNet is one saved network as exchanged with the gateway.
type wifiNet struct {
	S string `json:"s"`
	P string `json:"p"`
}

// ── helpers ───────────────────────────────────────────────────────────────────

func ownedGateway(c *gin.Context) (Gateway, bool) {
	var gw Gateway
	if err := DB.Where("id = ? AND user_id = ?", c.Param("id"), c.GetUint("userID")).First(&gw).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Gateway not found"})
		return gw, false
	}
	return gw, true
}

func orderedCreds(gwID string) []WifiCredential {
	var creds []WifiCredential
	DB.Where("gateway_id = ?", gwID).Order("priority ASC, id ASC").Find(&creds)
	return creds
}

// repackPriorities renumbers a gateway's networks 0..n-1 in their current order.
func repackPriorities(gwID string) {
	for i, cr := range orderedCreds(gwID) {
		if cr.Priority != i {
			DB.Model(&WifiCredential{}).Where("id = ?", cr.ID).Update("priority", i)
		}
	}
}

// pushWifiList sends the gateway the full ordered list (index 0 = highest
// priority). The list is marked dirty until the gateway acknowledges it, so a
// gateway that was offline gets it on its next connect instead of overwriting it.
func pushWifiList(gwID string) {
	creds := orderedCreds(gwID)
	nets := make([]wifiNet, 0, len(creds))
	for _, cr := range creds {
		nets = append(nets, wifiNet{S: cr.SSID, P: cr.Password})
	}
	DB.Model(&Gateway{}).Where("id = ?", gwID).Update("wifi_dirty", true)
	b, _ := json.Marshal(map[string]any{"nets": nets})
	if err := SendRawTextToGateway(gwID, "WIFISET:"+string(b)); err != nil {
		log.Printf("WIFISET to gateway %s deferred (offline): %v", gwID, err)
	}
}

// applyGatewayCreds handles the list a gateway reports when it connects.
// If the hub has unsent changes they win; otherwise the gateway's list (and
// order) becomes the hub's copy.
func applyGatewayCreds(gatewayID string, nets []wifiNet) {
	var gw Gateway
	if DB.Select("id", "wifi_dirty").First(&gw, "id = ?", gatewayID).Error != nil {
		return
	}
	if gw.WifiDirty {
		log.Printf("Gateway %s: hub has newer Wi-Fi list — pushing it", gatewayID)
		pushWifiList(gatewayID)
		return
	}
	DB.Where("gateway_id = ?", gatewayID).Delete(&WifiCredential{})
	saved := 0
	for i, n := range nets {
		if n.S == "" {
			continue
		}
		if DB.Create(&WifiCredential{GatewayID: gatewayID, SSID: n.S, Password: n.P, Priority: i}).Error == nil {
			saved++
		}
	}
	log.Printf("Synced %d Wi-Fi credentials from gateway %s", saved, gatewayID)
}

// ── Saved networks ────────────────────────────────────────────────────────────

func ListWifiCredentials(c *gin.Context) {
	gw, ok := ownedGateway(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, orderedCreds(gw.ID))
}

func SaveWifiCredential(c *gin.Context) {
	gw, ok := ownedGateway(c)
	if !ok {
		return
	}
	var req WifiCredentialRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.SSID = strings.TrimSpace(req.SSID)
	if req.SSID == "" || len(req.SSID) > 32 || len(req.Password) > 63 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "SSID must be 1-32 characters and the password at most 63"})
		return
	}

	var cred WifiCredential
	if DB.Where("gateway_id = ? AND ss_id = ?", gw.ID, req.SSID).First(&cred).Error == nil {
		DB.Model(&cred).Update("password", req.Password)
		cred.Password = req.Password
	} else {
		var count int64
		DB.Model(&WifiCredential{}).Where("gateway_id = ?", gw.ID).Count(&count)
		if count >= 8 {
			c.JSON(http.StatusConflict, gin.H{"error": "A gateway can remember at most 8 networks — remove one first"})
			return
		}
		cred = WifiCredential{GatewayID: gw.ID, SSID: req.SSID, Password: req.Password, Priority: int(count)}
		if err := DB.Create(&cred).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save credential"})
			return
		}
	}

	pushWifiList(gw.ID)
	if req.ConnectNow {
		if err := SendRawTextToGateway(gw.ID, "WIFI:"+req.SSID+":"+req.Password); err != nil {
			log.Printf("Failed to forward WiFi config to gateway %s: %v", gw.ID, err)
		}
	}
	LogAudit(c, "wifi_credential_saved", "gateway", &gw.ID, "WiFi credential saved for SSID: "+req.SSID)

	// Update gateway name to SSID if not already set
	if gw.Name == "" || gw.Name == "New Gateway" {
		DB.Model(&gw).Updates(map[string]interface{}{"name": req.SSID, "updated_at": time.Now()})
	}
	c.JSON(http.StatusCreated, cred)
}

// ReorderWifiCredentials sets the priority order: ids[0] is tried first.
func ReorderWifiCredentials(c *gin.Context) {
	gw, ok := ownedGateway(c)
	if !ok {
		return
	}
	var req wifiOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	existing := orderedCreds(gw.ID)
	byID := make(map[uint]bool, len(existing))
	for _, cr := range existing {
		byID[cr.ID] = true
	}
	seen := map[uint]bool{}
	order := make([]uint, 0, len(existing))
	for _, id := range req.IDs {
		if byID[id] && !seen[id] {
			seen[id] = true
			order = append(order, id)
		}
	}
	// Anything the client did not mention keeps its relative order at the end.
	for _, cr := range existing {
		if !seen[cr.ID] {
			order = append(order, cr.ID)
		}
	}
	for i, id := range order {
		DB.Model(&WifiCredential{}).Where("id = ?", id).Update("priority", i)
	}
	pushWifiList(gw.ID)
	LogAudit(c, "wifi_priority_changed", "gateway", &gw.ID, "WiFi network priority changed")
	c.JSON(http.StatusOK, orderedCreds(gw.ID))
}

func DeleteWifiCredential(c *gin.Context) {
	gw, ok := ownedGateway(c)
	if !ok {
		return
	}
	var cred WifiCredential
	if err := DB.Where("slug = ? AND gateway_id = ?", c.Param("credId"), gw.ID).First(&cred).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Credential not found"})
		return
	}
	DB.Delete(&cred)
	repackPriorities(gw.ID)
	pushWifiList(gw.ID)
	LogAudit(c, "wifi_credential_deleted", "gateway", &gw.ID, "WiFi credential deleted for SSID: "+cred.SSID)
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

// ── Gateway name ──────────────────────────────────────────────────────────────

// UpdateGateway renames a gateway. The new name is pushed to the gateway (which
// announces it to HMIs) and HMIs pick it up from /api/hmi/gateway.
func UpdateGateway(c *gin.Context) {
	gw, ok := ownedGateway(c)
	if !ok {
		return
	}
	var req UpdateGatewayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	name := strings.Join(strings.Fields(req.Name), " ") // trim + collapse whitespace
	if name == "" || len([]rune(name)) > 40 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Name must be 1-40 characters"})
		return
	}
	old := gw.Name
	if err := DB.Model(&gw).Updates(map[string]interface{}{"name": name, "updated_at": time.Now()}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to rename gateway"})
		return
	}
	if err := SendRawTextToGateway(gw.ID, "GWNAME:"+name); err != nil {
		log.Printf("GWNAME to gateway %s deferred (offline): %v", gw.ID, err)
	}
	LogAudit(c, "gateway_renamed", "gateway", &gw.ID, "Gateway renamed: "+old+" → "+name)
	c.JSON(http.StatusOK, gin.H{"id": gw.ID, "name": name, "online": gw.IsOnline})
}
