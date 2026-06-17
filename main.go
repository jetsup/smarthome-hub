package main

import (
	"net/http"
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

	// Set up the Web REST API using Gin
	r := gin.Default()
	r.Use(cors.New(cors.Config{
		AllowOrigins:     []string{"http://localhost:5173", "http://127.0.0.1:5173"},
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization"},
		AllowCredentials: true,
	}))

	// ── Public routes ────────────────────────────────────────────────────────

	auth := r.Group("/api/auth")
	{
		auth.POST("/register", hub.Register)
		auth.POST("/login", hub.Login)
	}

	// ── Authenticated user routes ────────────────────────────────────────────

	api := r.Group("/api")
	api.Use(hub.AuthMiddleware())
	{
		// Gateways
		api.GET("/gateways", hub.ListGateways)
		api.POST("/gateways", hub.CreateGateway)
		api.GET("/gateways/:id", hub.GetGateway)
		api.DELETE("/gateways/:id", hub.DeleteGateway)
		api.GET("/gateways/:id/api-key", hub.GetGatewayAPIKey)
		api.GET("/gateways/:id/nodes", hub.ListNodes)

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

	// Start serving HTTP requests
	r.Run(":9000")
}
