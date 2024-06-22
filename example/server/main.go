package main

import (
	"log"
	"log/slog"
	"os"

	example "github.com/aicacia/go-webrtc-http/example"
	"github.com/joho/godotenv"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
	err := godotenv.Load("example/.env")
	if err != nil {
		log.Fatal("Error loading .env file")
	}
	example.InitServer()
}
