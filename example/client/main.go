package main

import (
	"log/slog"
	"os"

	example "github.com/aicacia/go-webrtchttp/example"
	"github.com/joho/godotenv"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))
	if err := godotenv.Load("example/.env"); err != nil {
		slog.Error("Error loading .env file", "error", err)
	}
	example.InitClient()
}
