package main

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/anujagrawal380/distributed-job-queue/pkg/sdk"
)

type ImageJob struct {
	ImageURL string `json:"image_url"`
	Filter   string `json:"filter"`
}

func main() {
	client := sdk.NewClient(
		"http://localhost:8080",
		"your-client-api-key",
		sdk.WithTimeout(10*time.Second),
	)

	// Batch of jobs to submit
	jobs := []ImageJob{
		{ImageURL: "https://example.com/img1.jpg", Filter: "grayscale"},
		{ImageURL: "https://example.com/img2.jpg", Filter: "sepia"},
		{ImageURL: "https://example.com/img3.jpg", Filter: "blur"},
		{ImageURL: "https://example.com/img4.jpg", Filter: "sharpen"},
		{ImageURL: "https://example.com/img5.jpg", Filter: "vintage"},
	}

	fmt.Printf("Submitting %d jobs...\n", len(jobs))

	// Submit all jobs concurrently
	jobIDs := make([]string, len(jobs))
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i, job := range jobs {
		wg.Add(1)
		go func(idx int, j ImageJob) {
			defer wg.Done()

			payload, _ := json.Marshal(j)
			jobID, err := client.Submit(string(payload), 3)
			if err != nil {
				log.Printf("Failed to submit job %d: %v", idx, err)
				return
			}

			mu.Lock()
			jobIDs[idx] = jobID
			mu.Unlock()

			fmt.Printf("Job %d submitted: %s\n", idx+1, jobID)
		}(i, job)
	}

	wg.Wait()

	// Wait for all jobs to complete
	fmt.Println("\nWaiting for jobs to complete...")
	results := make([]*sdk.Job, len(jobIDs))

	for i, jobID := range jobIDs {
		if jobID == "" {
			continue
		}

		wg.Add(1)
		go func(idx int, jID string) {
			defer wg.Done()

			job, err := client.WaitForCompletion(jID, 60*time.Second)
			if err != nil {
				log.Printf("Job %s timeout: %v", jID, err)
				return
			}

			mu.Lock()
			results[idx] = job
			mu.Unlock()
		}(i, jobID)
	}

	wg.Wait()

	// Print summary
	fmt.Println("\n=== Results Summary ===")
	successful := 0
	failed := 0

	for i, job := range results {
		if job == nil {
			continue
		}

		if job.IsSuccessful() {
			successful++
			fmt.Printf("Job %d: %s\n", i+1, job.Result)
		} else {
			failed++
			fmt.Printf("Job %d: %s\n", i+1, job.ResultError)
		}
	}

	fmt.Printf("\nTotal: %d | Success: %d | Failed: %d\n", len(jobs), successful, failed)
}
