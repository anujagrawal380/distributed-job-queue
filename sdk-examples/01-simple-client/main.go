package main

import (
	"fmt"
	"log"
	"time"

	"github.com/anujagrawal380/distributed-job-queue/pkg/sdk"
)

func main() {
	// Create a client with your API key
	client := sdk.NewClient("http://localhost:8080", "your-client-api-key")

	// Submit a simple job
	jobID, err := client.Submit("process-image:photo.jpg", 3)
	if err != nil {
		log.Fatalf("Failed to submit job: %v", err)
	}

	fmt.Printf("Job submitted: %s\n", jobID)

	// Check job status
	job, err := client.GetStatus(jobID)
	if err != nil {
		log.Fatalf("Failed to get status: %v", err)
	}

	fmt.Printf("Job state: %s\n", job.State)

	// Wait for completion (with 60 second timeout)
	finalJob, err := client.WaitForCompletion(jobID, 60*time.Second)
	if err != nil {
		log.Fatalf("Failed to wait for completion: %v", err)
	}

	if finalJob.IsSuccessful() {
		fmt.Printf("Job completed successfully!\n")
		fmt.Printf("Result: %s\n", finalJob.Result)
	} else {
		fmt.Printf("Job failed: %s\n", finalJob.ResultError)
	}
}
