package main

import (
	"net/http"
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

	// Start serving HTTP requests
	r.Run(":9000") 
}
