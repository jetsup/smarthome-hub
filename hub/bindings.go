package hub

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

type BindingRequest struct {
	SourceDeviceID uint32 `json:"sourceDeviceId" binding:"required"`
	SourcePin      uint8  `json:"sourcePin" binding:"required"`
	TargetDeviceID uint32 `json:"targetDeviceId" binding:"required"`
	TargetPin      uint8  `json:"targetPin" binding:"required"`
}

type BindingResponse struct {
	ID             uint      `json:"id"`
	GatewayID      string    `json:"gatewayId"`
	SourceDeviceID uint32    `json:"sourceDeviceId"`
	SourcePin      uint8     `json:"sourcePin"`
	TargetDeviceID uint32    `json:"targetDeviceId"`
	TargetPin      uint8     `json:"targetPin"`
	CreatedAt      time.Time `json:"createdAt"`
}

func ListBindings(c *gin.Context) {
	gatewayID := c.Param("id")
	userID := c.GetUint("userID")

	var gw Gateway
	if err := DB.Where("id = ? AND user_id = ?", gatewayID, userID).First(&gw).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Gateway not found"})
		return
	}

	var bindings []CapabilityBinding
	DB.Where("gateway_id = ?", gatewayID).Order("created_at desc").Find(&bindings)

	resp := make([]BindingResponse, len(bindings))
	for i, b := range bindings {
		resp[i] = BindingResponse{
			ID:             b.ID,
			GatewayID:      b.GatewayID,
			SourceDeviceID: b.SourceDeviceID,
			SourcePin:      b.SourcePin,
			TargetDeviceID: b.TargetDeviceID,
			TargetPin:      b.TargetPin,
			CreatedAt:      b.CreatedAt,
		}
	}
	c.JSON(http.StatusOK, resp)
}

func CreateBinding(c *gin.Context) {
	gatewayID := c.Param("id")
	userID := c.GetUint("userID")

	var gw Gateway
	if err := DB.Where("id = ? AND user_id = ?", gatewayID, userID).First(&gw).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Gateway not found"})
		return
	}

	var req BindingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	binding := CapabilityBinding{
		GatewayID:      gatewayID,
		SourceDeviceID: req.SourceDeviceID,
		SourcePin:      req.SourcePin,
		TargetDeviceID: req.TargetDeviceID,
		TargetPin:      req.TargetPin,
	}

	if err := DB.Create(&binding).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create binding"})
		return
	}

	c.JSON(http.StatusCreated, BindingResponse{
		ID:             binding.ID,
		GatewayID:      binding.GatewayID,
		SourceDeviceID: binding.SourceDeviceID,
		SourcePin:      binding.SourcePin,
		TargetDeviceID: binding.TargetDeviceID,
		TargetPin:      binding.TargetPin,
		CreatedAt:      binding.CreatedAt,
	})
}

func DeleteBinding(c *gin.Context) {
	bindingID := c.Param("bindingId")
	userID := c.GetUint("userID")

	var binding CapabilityBinding
	if err := DB.First(&binding, bindingID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Binding not found"})
		return
	}

	var gw Gateway
	if err := DB.Where("id = ? AND user_id = ?", binding.GatewayID, userID).First(&gw).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Gateway not found"})
		return
	}

	DB.Delete(&binding)
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

// processBindings checks for capability bindings that originate from the given
// device+pin and forwards the telemetry value to each target via MSG_PIN_CMD.
func processBindings(gatewayID string, deviceID uint32, value uint16) {
	var bindings []CapabilityBinding
	DB.Where("gateway_id = ? AND source_device_id = ?", gatewayID, deviceID).Find(&bindings)
	for _, b := range bindings {
		b := b
		go func() {
			SendPinCommandToGateway(gatewayID, b.TargetDeviceID, b.TargetPin, value)
		}()
	}
}
