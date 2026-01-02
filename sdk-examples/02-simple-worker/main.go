// examples/02-simple-worker/main.go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/anujagrawal380/distributed-job-queue/pkg/sdk"
)

func main() {
	// Create a worker with your API key
	worker := sdk.NewWorker(
		"http://localhost:8080",
		"your-worker-api-key",
		processJob,
		sdk.WithPollInterval(2*time.Second),
		sdk.WithMaxConcurrent(5),
	)

	// Setup graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\nShutdown signal received...")
		cancel()
	}()

	// Start processing jobs
	log.Println("Worker starting...")
	if err := worker.Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("Worker error: %v", err)
	}

	log.Println("Worker stopped.")
}

// processJob handles the actual job processing
func processJob(ctx context.Context, job *sdk.LeasedJob) *sdk.JobResult {
	fmt.Printf("Processing: %s\n", job.Payload)

	// Simulate some work
	time.Sleep(2 * time.Second)

	// Simple processing logic
	if strings.Contains(job.Payload, "error") {
		return sdk.Failure(fmt.Errorf("job contains 'error' keyword"))
	}

	result := fmt.Sprintf("processed:%s:timestamp:%d", job.Payload, time.Now().Unix())
	return sdk.Success(result)
}
