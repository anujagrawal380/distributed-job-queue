// chaos is a load + fault-injection harness for the job queue.
//
// It submits N jobs, runs M worker goroutines that lease/ack against the
// server, and optionally kills & restarts the server process periodically
// via user-supplied shell commands. At the end it reports throughput,
// latency, and proves zero-loss durability.
//
// Example:
//
//	chaos -target http://localhost:8080 -key admin_... \
//	      -jobs 10000 -workers 16 \
//	      -kill-cmd "docker compose kill queue" \
//	      -restart-cmd "docker compose up -d queue" \
//	      -kills 20 -kill-interval 2s
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"os/exec"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type opts struct {
	target       string
	key          string
	jobs         int
	workers      int
	kills        int
	killInterval time.Duration
	killCmd      string
	restartCmd   string
	submitRate   int
	waitTimeout  time.Duration
}

func parseFlags() opts {
	var o opts
	flag.StringVar(&o.target, "target", "http://localhost:8080", "server base URL")
	flag.StringVar(&o.key, "key", "", "API key (needs jobs:submit, jobs:lease, jobs:ack, jobs:read)")
	flag.IntVar(&o.jobs, "jobs", 1000, "number of jobs to submit")
	flag.IntVar(&o.workers, "workers", 8, "number of concurrent workers")
	flag.IntVar(&o.kills, "kills", 0, "number of server kill+restart cycles to perform (0 = no chaos)")
	flag.DurationVar(&o.killInterval, "kill-interval", 3*time.Second, "delay between kills")
	flag.StringVar(&o.killCmd, "kill-cmd", "", "shell command to kill the server (run via sh -c)")
	flag.StringVar(&o.restartCmd, "restart-cmd", "", "shell command to restart the server (run via sh -c)")
	flag.IntVar(&o.submitRate, "submit-rate", 0, "max submits per second (0 = unlimited burst)")
	flag.DurationVar(&o.waitTimeout, "wait", 60*time.Second, "how long to wait for all jobs to ACK after submission")
	flag.Parse()
	if o.key == "" {
		log.Fatal("-key is required")
	}
	return o
}

type stats struct {
	submitted atomic.Int64
	acked     atomic.Int64
	failed    atomic.Int64
	leaseErr  atomic.Int64

	// latency samples, submit->ack in ms, captured under lock
	mu   sync.Mutex
	lats []float64
}

func (s *stats) record(latMs float64) {
	s.mu.Lock()
	s.lats = append(s.lats, latMs)
	s.mu.Unlock()
}

func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sorted := append([]float64(nil), xs...)
	sort.Float64s(sorted)
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}

type submitResp struct {
	JobID string `json:"job_id"`
}

type jobStatus struct {
	JobID    string `json:"job_id"`
	State    string `json:"state"`
	Attempts int    `json:"attempts"`
}

type leasedJob struct {
	JobID      string    `json:"job_id"`
	Payload    string    `json:"payload"`
	LeaseUntil time.Time `json:"lease_until"`
	Attempt    int       `json:"attempt"`
}

type client struct {
	base string
	key  string
	http *http.Client
}

func newClient(base, key string) *client {
	return &client{
		base: base,
		key:  key,
		http: &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.http.Do(req)
}

func (c *client) submit(ctx context.Context, payload string) (string, error) {
	resp, err := c.do(ctx, "POST", "/jobs", map[string]any{
		"payload": payload, "max_retries": 5,
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("submit: %d", resp.StatusCode)
	}
	var out submitResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.JobID, nil
}

func (c *client) lease(ctx context.Context) (*leasedJob, error) {
	resp, err := c.do(ctx, "POST", "/jobs/lease", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lease: %d", resp.StatusCode)
	}
	var j leasedJob
	if err := json.NewDecoder(resp.Body).Decode(&j); err != nil {
		return nil, err
	}
	return &j, nil
}

func (c *client) ack(ctx context.Context, id string) error {
	resp, err := c.do(ctx, "POST", "/jobs/"+id+"/ack", map[string]any{"result": "ok"})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ack: %d", resp.StatusCode)
	}
	return nil
}

func (c *client) status(ctx context.Context, id string) (*jobStatus, error) {
	resp, err := c.do(ctx, "GET", "/jobs/"+id, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status: %d", resp.StatusCode)
	}
	var s jobStatus
	return &s, json.NewDecoder(resp.Body).Decode(&s)
}

// tracker remembers submission time per job so we can compute end-to-end latency.
type tracker struct {
	mu      sync.Mutex
	started map[string]time.Time
}

func newTracker() *tracker { return &tracker{started: make(map[string]time.Time)} }
func (t *tracker) start(id string) {
	t.mu.Lock()
	t.started[id] = time.Now()
	t.mu.Unlock()
}
func (t *tracker) finish(id string) (time.Duration, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.started[id]
	if !ok {
		return 0, false
	}
	return time.Since(s), true
}
func (t *tracker) snapshot() map[string]time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]time.Time, len(t.started))
	for k, v := range t.started {
		out[k] = v
	}
	return out
}

func main() {
	o := parseFlags()
	c := newClient(o.target, o.key)
	s := &stats{}
	trk := newTracker()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log.Printf("chaos: target=%s jobs=%d workers=%d kills=%d",
		o.target, o.jobs, o.workers, o.kills)

	// Workers: keep leasing and acking until all jobs acked or ctx done.
	var workersWG sync.WaitGroup
	for i := 0; i < o.workers; i++ {
		workersWG.Add(1)
		go func() {
			defer workersWG.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				j, err := c.lease(ctx)
				if err != nil {
					s.leaseErr.Add(1)
					time.Sleep(100 * time.Millisecond)
					continue
				}
				if j == nil {
					time.Sleep(50 * time.Millisecond)
					continue
				}
				if err := c.ack(ctx, j.JobID); err != nil {
					s.failed.Add(1)
					continue
				}
				if d, ok := trk.finish(j.JobID); ok {
					s.record(float64(d.Milliseconds()))
				}
				s.acked.Add(1)
			}
		}()
	}

	// Submitter: fire all jobs.
	submitStart := time.Now()
	var submitWG sync.WaitGroup
	submitWG.Add(1)
	go func() {
		defer submitWG.Done()
		var throttle <-chan time.Time
		if o.submitRate > 0 {
			t := time.NewTicker(time.Second / time.Duration(o.submitRate))
			defer t.Stop()
			throttle = t.C
		}
		for i := 0; i < o.jobs; i++ {
			if throttle != nil {
				<-throttle
			}
			id, err := c.submit(ctx, fmt.Sprintf("job-%d", i))
			if err != nil {
				s.failed.Add(1)
				// retry a few times on transient server death
				for r := 0; r < 20 && err != nil; r++ {
					time.Sleep(500 * time.Millisecond)
					id, err = c.submit(ctx, fmt.Sprintf("job-%d", i))
				}
				if err != nil {
					continue
				}
			}
			trk.start(id)
			s.submitted.Add(1)
		}
	}()

	// Chaos: kill+restart on a schedule.
	var chaosWG sync.WaitGroup
	if o.kills > 0 && o.killCmd != "" && o.restartCmd != "" {
		chaosWG.Add(1)
		go func() {
			defer chaosWG.Done()
			for i := 0; i < o.kills; i++ {
				select {
				case <-ctx.Done():
					return
				case <-time.After(o.killInterval + jitter(o.killInterval/4)):
				}
				log.Printf("chaos [%d/%d]: killing server", i+1, o.kills)
				runShell(o.killCmd)
				time.Sleep(300 * time.Millisecond)
				log.Printf("chaos [%d/%d]: restarting server", i+1, o.kills)
				runShell(o.restartCmd)
				waitHealthy(ctx, c)
			}
			log.Printf("chaos: done killing")
		}()
	}

	// Progress logger.
	progressDone := make(chan struct{})
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-progressDone:
				return
			case <-t.C:
				log.Printf("progress: submitted=%d acked=%d failed=%d lease_err=%d",
					s.submitted.Load(), s.acked.Load(), s.failed.Load(), s.leaseErr.Load())
			}
		}
	}()

	submitWG.Wait()
	log.Printf("submission done in %v, waiting up to %v for acks",
		time.Since(submitStart).Round(time.Millisecond), o.waitTimeout)

	// Wait for convergence.
	deadline := time.Now().Add(o.waitTimeout)
	for time.Now().Before(deadline) {
		if s.acked.Load() >= s.submitted.Load() {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	chaosWG.Wait()
	cancel()
	workersWG.Wait()
	close(progressDone)

	// Durability check: count ACKED across all tracked IDs.
	pending := 0
	if s.acked.Load() < s.submitted.Load() {
		snap := trk.snapshot()
		ackedIDs := int64(0)
		for id := range snap {
			st, err := c.status(context.Background(), id)
			if err == nil && st.State == "ACKED" {
				ackedIDs++
			}
		}
		pending = int(s.submitted.Load() - ackedIDs)
	}

	total := time.Since(submitStart)
	s.mu.Lock()
	lats := s.lats
	s.mu.Unlock()

	fmt.Println()
	fmt.Println("────── chaos report ──────")
	fmt.Printf("  submitted     : %d\n", s.submitted.Load())
	fmt.Printf("  acked         : %d\n", s.acked.Load())
	fmt.Printf("  lost          : %d\n", pending)
	fmt.Printf("  submit errors : %d\n", s.failed.Load())
	fmt.Printf("  lease errors  : %d (expected during kills)\n", s.leaseErr.Load())
	fmt.Printf("  kills         : %d\n", o.kills)
	fmt.Printf("  wall time     : %v\n", total.Round(time.Millisecond))
	if total > 0 {
		fmt.Printf("  throughput    : %.0f jobs/s\n", float64(s.acked.Load())/total.Seconds())
	}
	fmt.Printf("  latency p50   : %.1f ms\n", percentile(lats, 0.50))
	fmt.Printf("  latency p95   : %.1f ms\n", percentile(lats, 0.95))
	fmt.Printf("  latency p99   : %.1f ms\n", percentile(lats, 0.99))
	if pending == 0 {
		fmt.Println("  result        : ZERO LOSS ✓")
	} else {
		fmt.Printf("  result        : %d jobs unaccounted\n", pending)
	}
}

func runShell(cmd string) {
	c := exec.Command("sh", "-c", cmd)
	if out, err := c.CombinedOutput(); err != nil {
		log.Printf("shell err: %v: %s", err, string(out))
	}
}

func waitHealthy(ctx context.Context, c *client) {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return
		}
		resp, err := http.Get(c.base + "/health")
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			return
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(d)))
}
