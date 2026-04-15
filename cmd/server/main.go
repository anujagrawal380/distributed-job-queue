package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/anujagrawal380/distributed-job-queue/internal/api"
	"github.com/anujagrawal380/distributed-job-queue/internal/auth"
	"github.com/anujagrawal380/distributed-job-queue/internal/cluster"
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

	// Create auth store
	authStore := auth.NewRedisStore(redisClient.GetClient())
	log.Printf("Auth store initialized")

	// Seed development keys (for DEV testing ONLY)
	ctx := context.Background()
	if err := auth.SeedDevKeys(ctx, authStore); err != nil {
		log.Fatalf("Failed to seed dev keys: %v", err)
	}

	// Build backend — cluster mode if CLUSTER_MODE=true, else local WAL-backed core.
	var (
		backend api.JobBackend
		leader  api.LeaderInfo
		onTick  func()
		cleanup func()
	)

	if getEnv("CLUSTER_MODE", "false") == "true" {
		backend, leader, onTick, cleanup = startClusterMode(walDir, leaseDuration)
	} else {
		w, err := wal.Open(walDir)
		if err != nil {
			log.Fatalf("Failed to open WAL: %v", err)
		}
		core, err := queue.NewCore(w)
		if err != nil {
			log.Fatalf("Failed to create queue core: %v", err)
		}
		log.Printf("Queue core initialized (WAL recovered)")
		backend = core
		leader = nil
		onTick = core.CheckExpiredLeases
		cleanup = func() { w.Close() }
	}
	defer cleanup()

	// Create API server
	server := api.NewServer(backend, leader, leaseDuration, authStore)

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
				onTick()
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

// startClusterMode bootstraps the Raft node and returns a cluster-backed
// backend. Env vars:
//   NODE_ID         unique id within the cluster, e.g. "node1"
//   RAFT_ADDR       TCP address this node binds for Raft, e.g. "0.0.0.0:7000"
//   PEERS           comma-separated "id@raft-addr", full cluster membership
//   HTTP_PEERS      comma-separated "id@http-url", for leader redirects
//   BOOTSTRAP       "true" on the one node that seeds the cluster on first boot
func startClusterMode(dataDir string, leaseDur time.Duration) (api.JobBackend, api.LeaderInfo, func(), func()) {
	nodeID := mustEnv("NODE_ID")
	raftAddr := mustEnv("RAFT_ADDR")
	peersSpec := mustEnv("PEERS")
	httpPeersSpec := mustEnv("HTTP_PEERS")
	bootstrap := getEnv("BOOTSTRAP", "false") == "true"

	peers := parsePeers(peersSpec)
	httpAddrs := parseHTTPPeers(httpPeersSpec)

	bus := queue.NewEventBus()
	fsm := cluster.NewFSM(bus)

	node, err := cluster.NewNode(cluster.NodeConfig{
		NodeID:    nodeID,
		RaftAddr:  raftAddr,
		DataDir:   dataDir,
		Bootstrap: bootstrap,
		Peers:     peers,
	}, fsm)
	if err != nil {
		log.Fatalf("Failed to start raft node: %v", err)
	}
	log.Printf("Raft node started: id=%s addr=%s bootstrap=%v peers=%d", nodeID, raftAddr, bootstrap, len(peers))

	srv := cluster.NewServer(node, bus, httpAddrs)
	_ = leaseDur
	return srv, srv, srv.CheckExpiredLeases, func() {
		if err := node.Shutdown(); err != nil {
			log.Printf("raft shutdown: %v", err)
		}
	}
}

// parsePeers parses "id1@host:port,id2@host:port" into cluster.Peer slices.
func parsePeers(s string) []cluster.Peer {
	out := make([]cluster.Peer, 0)
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		at := strings.IndexByte(p, '@')
		if at < 0 {
			log.Fatalf("PEERS entry missing '@': %q", p)
		}
		out = append(out, cluster.Peer{ID: p[:at], Addr: p[at+1:]})
	}
	return out
}

// parseHTTPPeers parses "id1@url1,id2@url2" into a map.
func parseHTTPPeers(s string) map[string]string {
	out := make(map[string]string)
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		at := strings.IndexByte(p, '@')
		if at < 0 {
			log.Fatalf("HTTP_PEERS entry missing '@': %q", p)
		}
		out[p[:at]] = p[at+1:]
	}
	return out
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required env var %s is not set", key)
	}
	return v
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
