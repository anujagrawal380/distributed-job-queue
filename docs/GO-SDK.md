# Go SDK

Client and Worker SDK for the Distributed Job Queue.

## Installation

```bash
go get github.com/anujagrawal380/distributed-job-queue/pkg/sdk
```

## Client

Submit jobs and check their status.

### Basic Usage

```go
import "github.com/anujagrawal380/distributed-job-queue/pkg/sdk"

// Create client
client := sdk.NewClient("http://localhost:8080", "your-client-api-key")

// Submit job
jobID, err := client.Submit("process-image:photo.jpg", 3)

// Get status
job, err := client.GetStatus(jobID)

// Wait for completion (blocks until done or timeout)
job, err := client.WaitForCompletion(jobID, 60*time.Second)

// Submit and wait in one call
job, err := client.SubmitAndWait("payload", 3, 60*time.Second)
```

### Options

```go
client := sdk.NewClient(
    "http://localhost:8080",
    "your-api-key",
    sdk.WithTimeout(30*time.Second),          // HTTP timeout
    sdk.WithHTTPClient(customHTTPClient),     // Custom HTTP client
)
```

### Job Status

```go
job, _ := client.GetStatus(jobID)

job.IsComplete()    // true if ACKED or DEAD
job.IsSuccessful()  // true if ACKED with no error
job.IsFailed()      // true if DEAD or ACKED with error

fmt.Println(job.State)       // READY, RUNNING, ACKED, RETRY, DEAD
fmt.Println(job.Result)      // Success result data
fmt.Println(job.ResultError) // Error message if failed
```

---

## Worker

Process jobs from the queue.

### Basic Usage

```go
// Define your processor
func processJob(ctx context.Context, job *sdk.LeasedJob) *sdk.JobResult {
    // Do work
    result := doWork(job.Payload)
    
    // Return success
    return sdk.Success(result)
    
    // Or return failure
    return sdk.Failure(fmt.Errorf("processing failed"))
}

// Create and run worker
worker := sdk.NewWorker(
    "http://localhost:8080",
    "your-worker-api-key",
    processJob,
)

ctx := context.Background()
worker.Run(ctx) // Blocks and polls for jobs
```

### Options

```go
worker := sdk.NewWorker(
    baseURL, apiKey, handler,
    sdk.WithPollInterval(2*time.Second),      // How often to check for jobs
    sdk.WithMaxConcurrent(5),                 // Process up to 5 jobs simultaneously
    sdk.WithLogger(customLogger),             // Custom logger
    sdk.WithWorkerTimeout(30*time.Second),    // HTTP timeout
)
```

### Job Processing

```go
func processJob(ctx context.Context, job *sdk.LeasedJob) *sdk.JobResult {
    // job.JobID      - Unique job identifier
    // job.Payload    - Job data to process
    // job.Attempt    - Current attempt number
    // job.LeaseUntil - When the lease expires
    
    // Check context for cancellation
    select {
    case <-ctx.Done():
        return sdk.Failure(ctx.Err())
    default:
    }
    
    // Process the job
    output, err := processData(job.Payload)
    if err != nil {
        return sdk.Failure(err)
    }
    
    return sdk.Success(output)
}
```

### Graceful Shutdown

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

// Listen for shutdown signals
sigChan := make(chan os.Signal, 1)
signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

go func() {
    <-sigChan
    cancel() // Stop worker gracefully
}()

worker.Run(ctx)
```

---

## Examples

### 1. Simple Client

Submit a job and wait for completion.

```go
client := sdk.NewClient("http://localhost:8080", "client-key")

jobID, _ := client.Submit("process:data", 3)
job, _ := client.WaitForCompletion(jobID, 60*time.Second)

if job.IsSuccessful() {
    fmt.Println("Result:", job.Result)
}
```

[Full example](../sdk-examples/01-simple-client/main.go)

### 2. Simple Worker

Process jobs with a simple handler.

```go
worker := sdk.NewWorker(
    "http://localhost:8080",
    "worker-key",
    func(ctx context.Context, job *sdk.LeasedJob) *sdk.JobResult {
        time.Sleep(2 * time.Second) // Simulate work
        return sdk.Success("done")
    },
)

worker.Run(context.Background())
```

[Full example](../sdk-examples/02-simple-worker/main.go)

### 3. Video Processing

Process video encoding jobs with structured input/output.

```go
type VideoJob struct {
    InputURL  string `json:"input_url"`
    OutputRes string `json:"output_resolution"`
}

func processVideo(ctx context.Context, job *sdk.LeasedJob) *sdk.JobResult {
    var videoJob VideoJob
    json.Unmarshal([]byte(job.Payload), &videoJob)
    
    // Process video...
    result := map[string]interface{}{
        "output_url": "https://cdn.example.com/video.mp4",
        "duration":   123.45,
    }
    
    resultJSON, _ := json.Marshal(result)
    return sdk.Success(string(resultJSON))
}
```

[Full example](../sdk-examples/03-video-processing/main.go)

### 4. Batch Submit

Submit multiple jobs concurrently and collect results.

```go
jobs := []string{"job1", "job2", "job3"}

for _, payload := range jobs {
    go func(p string) {
        jobID, _ := client.Submit(p, 3)
        job, _ := client.WaitForCompletion(jobID, 60*time.Second)
        // Handle result...
    }(payload)
}
```

[Full example](../sdk-examples/04-batch-submit/main.go)

### 5. Custom Results

Return structured results with metrics and metadata.

```go
type DataResult struct {
    Status    string            `json:"status"`
    OutputURL string            `json:"output_url"`
    Metrics   ProcessingMetrics `json:"metrics"`
}

func processWithMetrics(ctx context.Context, job *sdk.LeasedJob) *sdk.JobResult {
    result := DataResult{
        Status:    "completed",
        OutputURL: "https://storage.example.com/output.zip",
        Metrics: ProcessingMetrics{
            ProcessingTime: time.Since(start),
            CPUUsage:      45.2,
        },
    }
    
    resultJSON, _ := json.Marshal(result)
    return sdk.Success(string(resultJSON))
}
```

[Full example](../sdk-examples/05-custom-results/main.go)

---

## Error Handling

### Client Errors

```go
jobID, err := client.Submit(payload, 3)
if err != nil {
    // Network error, validation error, or server error
    log.Printf("Submit failed: %v", err)
}

job, err := client.GetStatus(jobID)
if err != nil {
    // Job not found or network error
    log.Printf("Status check failed: %v", err)
}
```

### Worker Errors

When a worker returns `sdk.Failure()`, the job will:
- Retry up to `max_retries` times
- Move to `DEAD` state if all retries exhausted
- Store the error message in `result_error` field

```go
func processJob(ctx context.Context, job *sdk.LeasedJob) *sdk.JobResult {
    if err := validateInput(job.Payload); err != nil {
        // Job will retry
        return sdk.Failure(err)
    }
    
    // Process...
    return sdk.Success("done")
}
```

---

## Best Practices

### Client

- **Reuse clients**: Create one client and reuse it for multiple requests
- **Set timeouts**: Use `WithTimeout()` for long-running operations
- **Handle errors**: Check errors on submit and status checks
- **Use context**: Use `*WithContext()` methods for cancellation support

### Worker

- **Idempotent jobs**: Design jobs to be safely retried
- **Lease duration**: Ensure work completes before lease expires (server configurable)
- **Concurrency**: Set `WithMaxConcurrent()` based on resource availability
- **Graceful shutdown**: Always handle context cancellation
- **Structured data**: Use JSON for complex payloads and results
- **Error reporting**: Return descriptive errors for debugging

### Structured Payloads

Use JSON for complex job data:

```go
// Client side
type JobInput struct {
    URL    string   `json:"url"`
    Options map[string]string `json:"options"`
}

input := JobInput{URL: "https://example.com", Options: map[string]string{"quality": "high"}}
payload, _ := json.Marshal(input)
jobID, _ := client.Submit(string(payload), 3)

// Worker side
func process(ctx context.Context, job *sdk.LeasedJob) *sdk.JobResult {
    var input JobInput
    if err := json.Unmarshal([]byte(job.Payload), &input); err != nil {
        return sdk.Failure(err)
    }
    
    // Process using input.URL and input.Options
    return sdk.Success("done")
}
```

---

## API Reference

### Client Methods

| Method | Description |
|--------|-------------|
| `Submit(payload, maxRetries)` | Submit a new job |
| `SubmitWithContext(ctx, ...)` | Submit with context support |
| `GetStatus(jobID)` | Get current job status |
| `GetStatusWithContext(ctx, jobID)` | Get status with context |
| `WaitForCompletion(jobID, timeout)` | Poll until job completes |
| `WaitForCompletionWithContext(ctx, ...)` | Wait with context |
| `SubmitAndWait(payload, maxRetries, timeout)` | Submit and wait in one call |

### Worker Methods

| Method | Description |
|--------|-------------|
| `Run(ctx)` | Start worker loop (blocks) |

### Job States

| State | Description |
|-------|-------------|
| `READY` | Waiting to be leased |
| `RUNNING` | Currently leased by a worker |
| `ACKED` | Completed (success or failure) |
| `RETRY` | Lease expired, will retry |
| `DEAD` | Exhausted all retries |

### Job Fields

| Field | Type | Description |
|-------|------|-------------|
| `JobID` | `string` | Unique identifier |
| `Payload` | `string` | Job data |
| `State` | `JobState` | Current state |
| `Attempts` | `int` | Number of attempts |
| `MaxRetries` | `int` | Maximum retry count |
| `CreatedAt` | `time.Time` | Creation timestamp |
| `LeaseUntil` | `*time.Time` | Lease expiration (if running) |
| `Result` | `string` | Success result data |
| `ResultError` | `string` | Error message if failed |
