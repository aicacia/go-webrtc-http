package main

import (
	"log"

	example "github.com/aicacia/go-webrtc-http/example"
	"github.com/joho/godotenv"
)

func main() {
	err := godotenv.Load("example/.env")
	if err != nil {
		log.Fatal("Error loading .env file")
	}
	example.InitClient()
}
