package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/anujagrawal380/distributed-job-queue/pkg/sdk"
)

// Custom result structures
type ProcessingMetrics struct {
	ProcessingTime time.Duration `json:"processing_time_ms"`
	InputSize      int64         `json:"input_size_bytes"`
	OutputSize     int64         `json:"output_size_bytes"`
	CPUUsage       float64       `json:"cpu_usage_percent"`
	MemoryUsage    float64       `json:"memory_usage_mb"`
}

type DataResult struct {
	Status      string            `json:"status"`
	OutputURL   string            `json:"output_url"`
	Metrics     ProcessingMetrics `json:"metrics"`
	Warnings    []string          `json:"warnings,omitempty"`
	GeneratedAt time.Time         `json:"generated_at"`
}

func main() {
	// Start worker
	go startWorker()

	// Start client
	time.Sleep(2 * time.Second)
	startClient()
}

func startWorker() {
	worker := sdk.NewWorker(
		"http://localhost:8080",
		"your-worker-api-key",
		processWithMetrics,
		sdk.WithPollInterval(2*time.Second),
	)

	ctx := context.Background()
	if err := worker.Run(ctx); err != nil {
		log.Printf("Worker stopped: %v", err)
	}
}

func processWithMetrics(ctx context.Context, job *sdk.LeasedJob) *sdk.JobResult {
	startTime := time.Now()

	// Simulate processing
	time.Sleep(3 * time.Second)

	// Create rich result data
	result := DataResult{
		Status:    "completed",
		OutputURL: fmt.Sprintf("https://storage.example.com/output/%s.zip", job.JobID),
		Metrics: ProcessingMetrics{
			ProcessingTime: time.Since(startTime),
			InputSize:      1024 * 1024, // 1 MB
			OutputSize:     512 * 1024,  // 512 KB
			CPUUsage:       45.2,
			MemoryUsage:    128.5,
		},
		Warnings: []string{
			"Input quality was low",
			"Applied auto-enhancement",
		},
		GeneratedAt: time.Now(),
	}

	resultJSON, err := json.Marshal(result)
	if err != nil {
		return sdk.Failure(err)
	}

	return sdk.Success(string(resultJSON))
}

func startClient() {
	client := sdk.NewClient("http://localhost:8080", "your-client-api-key")

	fmt.Println("Submitting job...")
	jobID, err := client.Submit("process-data:dataset-large.csv", 3)
	if err != nil {
		log.Fatalf("Submit error: %v", err)
	}

	fmt.Printf("Job submitted: %s\n", jobID)

	// Wait for completion
	job, err := client.WaitForCompletion(jobID, 60*time.Second)
	if err != nil {
		log.Fatalf("Wait error: %v", err)
	}

	if !job.IsSuccessful() {
		log.Fatalf("Job failed: %s", job.ResultError)
	}

	// Parse custom result
	var result DataResult
	if err := json.Unmarshal([]byte(job.Result), &result); err != nil {
		log.Fatalf("Failed to parse result: %v", err)
	}

	// Display rich result data
	fmt.Println("\n=== Job Results ===")
	fmt.Printf("Status: %s\n", result.Status)
	fmt.Printf("Output: %s\n", result.OutputURL)
	fmt.Printf("\nMetrics:\n")
	fmt.Printf("  Processing Time: %v\n", result.Metrics.ProcessingTime)
	fmt.Printf("  Input Size: %.2f MB\n", float64(result.Metrics.InputSize)/(1024*1024))
	fmt.Printf("  Output Size: %.2f KB\n", float64(result.Metrics.OutputSize)/1024)
	fmt.Printf("  CPU Usage: %.1f%%\n", result.Metrics.CPUUsage)
	fmt.Printf("  Memory Usage: %.1f MB\n", result.Metrics.MemoryUsage)

	if len(result.Warnings) > 0 {
		fmt.Printf("\nWarnings:\n")
		for _, w := range result.Warnings {
			fmt.Printf("%s\n", w)
		}
	}

	fmt.Printf("\nGenerated at: %s\n", result.GeneratedAt.Format(time.RFC3339))
}
