package main

import (
	"github.com/mrOkk/GameParty/internal/bot"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	for _, key := range []string{"TELEGRAM_BOT_TOKEN", "TELEGRAM_WEBHOOK_SECRET", "UPSTASH_REDIS_REST_URL", "UPSTASH_REDIS_REST_TOKEN"} {
		if os.Getenv(key) == "" {
			log.Fatalf("Missing environment variable: %s", key)
		}
	}
	if !strings.HasPrefix(os.Getenv("UPSTASH_REDIS_REST_URL"), "https://") {
		log.Fatal("Redis REST URL must use HTTPS")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	handler := &bot.Server{Store: &bot.Redis{URL: os.Getenv("UPSTASH_REDIS_REST_URL"), Token: os.Getenv("UPSTASH_REDIS_REST_TOKEN"), Client: client}, Token: os.Getenv("TELEGRAM_BOT_TOKEN"), Secret: os.Getenv("TELEGRAM_WEBHOOK_SECRET"), Client: client}
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	server := &http.Server{Addr: ":" + port, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second}
	log.Printf("Listening on port %s", port)
	log.Fatal(server.ListenAndServe())
}
