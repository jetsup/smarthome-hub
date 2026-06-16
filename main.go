package main

import (
	"net/http"
	"strconv"
	"smarthome-hub/hub"
	"github.com/gin-gonic/gin"
)

func main() {
	// Fire up the Serial Processor as a background Goroutine
	go hub.StartSerialWorker("/dev/ttyUSB0")

	// Set up the Web REST API using Gin
	r := gin.Default()

	// Endpoint to get all dynamically registered devices
	r.GET("/api/devices", func(c *gin.Context) {
		c.JSON(http.StatusOK, hub.NetworkRegistry)
	})

	// Endpoint to send a direct command to a targeted ESP32 Node
	// Pattern: POST /api/device/1225483764/control?state=1
	r.POST("/api/device/:id/control", func(c *gin.Context) {
		idStr := c.Param("id")
		stateStr := c.Query("state")

		// Parse the device identifier string into an unsigned integer
		deviceId, err := strconv.ParseUint(idStr, 10, 32)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid Device ID format"})
			return
		}

		// Parse targeted execution value (1 = ON, 0 = OFF)
		state, err := strconv.ParseUint(stateStr, 10, 16)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid state command value"})
			return
		}

		// Package up data into the packet format and push it down the wire
		// msgType 2 denotes an down-link control action command
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

	// Start serving HTTP requests
	r.Run(":9000") 
}
