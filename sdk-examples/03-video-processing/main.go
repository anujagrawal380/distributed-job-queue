package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anujagrawal380/distributed-job-queue/pkg/sdk"
)

type VideoJob struct {
	InputURL   string `json:"input_url"`
	OutputRes  string `json:"output_resolution"`
	OutputPath string `json:"output_path"`
}

type VideoResult struct {
	OutputURL  string  `json:"output_url"`
	Duration   float64 `json:"duration_seconds"`
	Size       int64   `json:"size_bytes"`
	Resolution string  `json:"resolution"`
}

func main() {
	worker := sdk.NewWorker(
		"http://localhost:8080",
		"your-worker-api-key",
		processVideo,
		sdk.WithPollInterval(5*time.Second),
		sdk.WithMaxConcurrent(3),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		cancel()
	}()

	log.Println("Video processing worker starting...")
	if err := worker.Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("Worker error: %v", err)
	}
}

func processVideo(ctx context.Context, job *sdk.LeasedJob) *sdk.JobResult {
	// Parse video job
	var videoJob VideoJob
	if err := json.Unmarshal([]byte(job.Payload), &videoJob); err != nil {
		return sdk.Failure(fmt.Errorf("invalid job payload: %w", err))
	}

	log.Printf("Processing video: %s -> %s", videoJob.InputURL, videoJob.OutputRes)

	// Simulate video processing (in real world: ffmpeg, cloud APIs, etc.)
	time.Sleep(10 * time.Second)

	// Create result with custom data
	result := VideoResult{
		OutputURL:  fmt.Sprintf("https://cdn.example.com/videos/%s", videoJob.OutputPath),
		Duration:   123.45,
		Size:       15728640, // 15 MB
		Resolution: videoJob.OutputRes,
	}

	resultJSON, err := json.Marshal(result)
	if err != nil {
		return sdk.Failure(fmt.Errorf("failed to marshal result: %w", err))
	}

	return sdk.Success(string(resultJSON))
}
