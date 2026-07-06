package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type result struct {
	duration time.Duration
	success  bool
	err      error
}

func main() {
	targetURL := flag.String("url", "http://localhost:8080/catalog/items", "Target URL to load test")
	apiKey := flag.String("key", "alice-secret-key", "API Key to include in X-API-Key header")
	concurrency := flag.Int("concurrency", 10, "Number of concurrent worker goroutines")
	duration := flag.Duration("duration", 5*time.Second, "Load test duration (e.g. 5s, 10s)")
	flag.Parse()

	fmt.Printf("Starting load test on %s\n", *targetURL)
	fmt.Printf("Concurrency: %d workers, Duration: %v\n\n", *concurrency, *duration)

	var totalRequests int64
	var successRequests int64
	var failedRequests int64

	resultsChan := make(chan result, 1000000)
	var wg sync.WaitGroup

	stopSignal := make(chan struct{})

	// Spawn workers
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			MaxIdleConnsPerHost: *concurrency,
			MaxIdleConns:        *concurrency,
		},
	}

	for i := 0; i < *concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stopSignal:
					return
				default:
					req, err := http.NewRequest("GET", *targetURL, nil)
					if err != nil {
						atomic.AddInt64(&failedRequests, 1)
						resultsChan <- result{success: false, err: err}
						continue
					}
					if *apiKey != "" {
						req.Header.Set("X-API-Key", *apiKey)
					}

					start := time.Now()
					resp, err := client.Do(req)
					elapsed := time.Since(start)

					atomic.AddInt64(&totalRequests, 1)
					if err != nil {
						atomic.AddInt64(&failedRequests, 1)
						resultsChan <- result{duration: elapsed, success: false, err: err}
						continue
					}

					if resp.StatusCode == http.StatusOK {
						atomic.AddInt64(&successRequests, 1)
						resultsChan <- result{duration: elapsed, success: true}
					} else {
						atomic.AddInt64(&failedRequests, 1)
						resultsChan <- result{duration: elapsed, success: false, err: fmt.Errorf("status code: %d", resp.StatusCode)}
					}
					resp.Body.Close()
				}
			}
		}()
	}

	// Wait for duration and signal workers to stop
	startTime := time.Now()
	time.Sleep(*duration)
	close(stopSignal)
	wg.Wait()
	close(resultsChan)
	actualDuration := time.Since(startTime)

	// Collect and sort latencies
	var latencies []time.Duration
	for r := range resultsChan {
		if r.success {
			latencies = append(latencies, r.duration)
		}
	}

	total := atomic.LoadInt64(&totalRequests)
	success := atomic.LoadInt64(&successRequests)
	failed := atomic.LoadInt64(&failedRequests)

	if total == 0 {
		fmt.Println("No requests were sent.")
		os.Exit(0)
	}

	throughput := float64(total) / actualDuration.Seconds()

	var mean time.Duration
	var p50, p90, p95, p99, maxLat time.Duration

	if len(latencies) > 0 {
		var sum time.Duration
		for _, l := range latencies {
			sum += l
		}
		mean = sum / time.Duration(len(latencies))

		sort.Slice(latencies, func(i, j int) bool {
			return latencies[i] < latencies[j]
		})

		p50 = getPercentile(latencies, 0.50)
		p90 = getPercentile(latencies, 0.90)
		p95 = getPercentile(latencies, 0.95)
		p99 = getPercentile(latencies, 0.99)
		maxLat = latencies[len(latencies)-1]
	}

	fmt.Println("==================================================")
	fmt.Println("                 BENCHMARK RESULTS                ")
	fmt.Println("==================================================")
	fmt.Printf("Total Requests:     %d\n", total)
	fmt.Printf("Success Count:      %d\n", success)
	fmt.Printf("Error Count:        %d\n", failed)
	fmt.Printf("Requests/sec:       %.2f\n", throughput)
	fmt.Printf("Mean Latency:       %v\n", mean)
	fmt.Printf("p50 (Median):       %v\n", p50)
	fmt.Printf("p90:                %v\n", p90)
	fmt.Printf("p95:                %v\n", p95)
	fmt.Printf("p99:                %v\n", p99)
	fmt.Printf("Max Latency:        %v\n", maxLat)
	fmt.Println("==================================================")
}

func getPercentile(latencies []time.Duration, rank float64) time.Duration {
	if len(latencies) == 0 {
		return 0
	}
	idx := int(rank * float64(len(latencies)))
	if idx >= len(latencies) {
		idx = len(latencies) - 1
	}
	return latencies[idx]
}
