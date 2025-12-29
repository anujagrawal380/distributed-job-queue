package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anujagrawal380/distributed-job-queue/internal/api"
	"github.com/anujagrawal380/distributed-job-queue/internal/queue"
	redisclient "github.com/anujagrawal380/distributed-job-queue/internal/redis"
	"github.com/anujagrawal380/distributed-job-queue/internal/wal"
)

func main() {
	// Read configuration from environment
	port := getEnv("PORT", "8080")
	walDir := getEnv("WAL_DIR", "./data")
	redisURL := getEnv("REDIS_URL", "localhost:6379")
	leaseDuration := getEnvDuration("LEASE_DURATION", 30*time.Second)
	leaseCheckInterval := getEnvDuration("LEASE_CHECK_INTERVAL", 5*time.Second)

	log.Printf("Starting job queue server...")
	log.Printf("  Port: %s", port)
	log.Printf("  WAL Directory: %s", walDir)
	log.Printf("  Redis URL: %s", redisURL)
	log.Printf("  Lease Duration: %v", leaseDuration)
	log.Printf("  Lease Check Interval: %v", leaseCheckInterval)

	// Connect to Redis
	redisConfig := &redisclient.Config{
		URL:            redisURL,
		MaxRetries:     3,
		PoolSize:       10,
		MinIdleConns:   2,
		ConnectTimeout: 5 * time.Second,
		ReadTimeout:    3 * time.Second,
		WriteTimeout:   3 * time.Second,
	}

	redisClient, err := redisclient.NewClient(redisConfig)
	if err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}
	defer redisClient.Close()
	log.Printf("Connected to Redis successfully")

	// Log Redis pool stats periodically (for monitoring)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			stats := redisClient.Stats()
			log.Printf("Redis pool stats: Hits=%d Misses=%d Timeouts=%d TotalConns=%d IdleConns=%d",
				stats.Hits, stats.Misses, stats.Timeouts, stats.TotalConns, stats.IdleConns)
		}
	}()

	// Open WAL
	w, err := wal.Open(walDir)
	if err != nil {
		log.Fatalf("Failed to open WAL: %v", err)
	}
	defer w.Close()

	// Create queue core (recovers from WAL)
	core, err := queue.NewCore(w)
	if err != nil {
		log.Fatalf("Failed to create queue core: %v", err)
	}
	log.Printf("Queue core initialized (WAL recovered)")

	// Create API server
	server := api.NewServer(core, leaseDuration)

	// Register routes
	mux := http.NewServeMux()
	server.RegisterRoutes(mux)

	// Create HTTP server
	httpServer := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}

	// Start background goroutine for checking expired leases
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		ticker := time.NewTicker(leaseCheckInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				core.CheckExpiredLeases()
			case <-ctx.Done():
				return
			}
		}
	}()

	// Start HTTP server in a goroutine
	go func() {
		log.Printf("Server listening on :%s", port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down gracefully...")

	// Stop lease checker
	cancel()

	// Shutdown HTTP server with timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("Error during shutdown: %v", err)
	}

	log.Println("Server stopped")
}

// Helper: get environment variable with default
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// Helper: get duration from environment variable
func getEnvDuration(key string, defaultValue time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if duration, err := time.ParseDuration(value); err == nil {
			return duration
		}
		log.Printf("Invalid duration for %s, using default: %v", key, defaultValue)
	}
	return defaultValue
}
