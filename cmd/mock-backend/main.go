package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	serviceName := os.Getenv("SERVICE_NAME")
	if serviceName == "" {
		serviceName = "mock-service"
	}

	// Health check endpoint
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK\n"))
	})

	// Root echo endpoint reflecting headers injected by gateway
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		fmt.Fprintf(w, `{
  "service": "%s",
  "path": "%s",
  "request_id": "%s",
  "consumer": "%s"
}
`, serviceName, r.URL.Path, r.Header.Get("X-Request-ID"), r.Header.Get("X-Consumer"))
	})

	log.Printf("Starting mock backend %q on port %s", serviceName, port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
