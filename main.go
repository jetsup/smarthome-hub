package main

import (
	"net/http"
	"path/filepath"
	"strconv"
	"smarthome-hub/hub"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

func main() {
	// Initialize MariaDB
	hub.InitDatabase()

	// Fire up the TCP listener for the WiFi gateway as a background Goroutine
	go hub.StartTCPWorker(":9010")

	// Start the online status monitor goroutine
	hub.StartOnlineMonitor()

	// Set up the Web REST API using Gin
	r := gin.Default()
	r.Use(cors.New(cors.Config{
		AllowOrigins:     []string{"*"},
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization"},
		AllowCredentials: false,
	}))

	// ── Serve Vue SPA static files ───────────────────────────────────────────
	uiDir := "../smarthome-ui/dist"
	r.Static("/assets", filepath.Join(uiDir, "assets"))
	r.StaticFile("/favicon.ico", filepath.Join(uiDir, "favicon.ico"))

	// ── API routes ────────────────────────────────────────────────────────────

	auth := r.Group("/api/auth")
	{
		auth.POST("/register", hub.Register)
		auth.POST("/login", hub.Login)
		auth.POST("/refresh", hub.RefreshToken)
	}

	// ── Authenticated user routes ────────────────────────────────────────────

	api := r.Group("/api")
	api.Use(hub.AuthMiddleware())
	{
		api.GET("/auth/me", hub.Me)
		// Dashboard
		api.GET("/stats", hub.GetDashboardStats)
		// Gateways
		api.GET("/gateways", hub.ListGateways)
		api.POST("/gateways", hub.CreateGateway)
		api.GET("/gateways/:id", hub.GetGateway)
		api.DELETE("/gateways/:id", hub.DeleteGateway)
		api.GET("/gateways/:id/api-key", hub.GetGatewayAPIKey)
		api.GET("/gateways/:id/nodes", hub.ListNodes)

		// WiFi credentials
		api.GET("/gateways/:id/wifi", hub.ListWifiCredentials)
		api.POST("/gateways/:id/wifi", hub.SaveWifiCredential)
		api.DELETE("/gateways/:id/wifi/:credId", hub.DeleteWifiCredential)

		// Node discovery & provisioning
		api.POST("/gateways/:id/scan", hub.StartScan)
		api.GET("/gateways/:id/discovered", hub.GetDiscoveredNodes)
		api.POST("/gateways/:id/provision", hub.ProvisionNode)

		// Node disconnect
		api.DELETE("/gateways/:id/nodes/:deviceId", hub.DisconnectNode)

		// Cross-gateway node discovery
		api.POST("/nodes/scan", hub.ScanAllGateways)
		api.GET("/nodes/discovered", hub.GetAllDiscoveredNodesHandler)
		api.POST("/nodes/provision", hub.ProvisionNodeToGateway)

		// Node detail (by hex nodeId)
		api.GET("/nodes/:nodeId", hub.GetNode)
		api.GET("/nodes/by-device/:deviceId", hub.GetNodeByDeviceID)

		// Send command to node (by hex nodeId)
		api.POST("/nodes/:nodeId/command", hub.ControlNode)

		// Update node capabilities
		api.PUT("/nodes/:nodeId/capabilities", hub.UpdateNodeCapabilities)

		// Device ping (updates LastSeen)
		api.POST("/devices/:id/ping", hub.PingDevice)

		// Real-time device registry (from TCP mesh)
		api.GET("/devices", func(c *gin.Context) {
			c.JSON(http.StatusOK, hub.NetworkRegistry)
		})

		// Send command to a device via the connected gateway
		api.POST("/device/:id/control", func(c *gin.Context) {
			idStr := c.Param("id")
			stateStr := c.Query("state")

			deviceId, err := strconv.ParseUint(idStr, 10, 32)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid Device ID format"})
				return
			}

			state, err := strconv.ParseUint(stateStr, 10, 16)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid state command value"})
				return
			}

			err = hub.SendCommand(uint32(deviceId), 2, uint16(state))
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to pass packet to gateway: " + err.Error()})
				return
			}

			c.JSON(http.StatusOK, gin.H{
				"status":    "transmitted",
				"deviceId":  deviceId,
				"cmd_value": state,
			})
		})
	}

	// ── SPA catch-all: serve index.html for any unmatched route ─────────────
	r.NoRoute(func(c *gin.Context) {
		// Only return the SPA for non-API paths
		if len(c.Request.URL.Path) >= 4 && c.Request.URL.Path[:4] == "/api" {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.File(filepath.Join(uiDir, "index.html"))
	})

	// Start serving HTTP requests
	r.Run(":9000")
}
