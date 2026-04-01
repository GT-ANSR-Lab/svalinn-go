package main

import (
	"flag"
	"fmt"
	"math"
	"runtime"
	"sync"
	"time"
)

func cpuBusyWork(iterations int) float64 {
	x := 1.0
	for i := 0; i < iterations; i++ {
		x = math.Sin(x) + math.Cos(x)
	}
	return x
}

func main() {
	numGoroutines := flag.Int("n", 1000, "number of goroutines to spawn")
	workIterations := flag.Int("work", 50000, "iterations of busy work per goroutine")
	monitorInterval := flag.Duration("interval", 10*time.Millisecond, "queue delay monitor interval")
	flag.Parse()

	fmt.Printf("GOMAXPROCS=%d, goroutines=%d, work=%d, monitor_interval=%v\n",
		runtime.GOMAXPROCS(0), *numGoroutines, *workIterations, *monitorInterval)

	// Monitor goroutine: periodically print queue delay.
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(*monitorInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				maxDelay, avgDelay := runtime.QueueDelay()
				fmt.Printf("[monitor] max_queue_delay=%12dns (%9.3fms)  avg_queue_delay=%12dns (%9.3fms)\n",
					maxDelay, float64(maxDelay)/1e6,
					avgDelay, float64(avgDelay)/1e6)
			case <-done:
				return
			}
		}
	}()

	// Spawn goroutines that do CPU-bound work.
	var wg sync.WaitGroup
	wg.Add(*numGoroutines)
	start := time.Now()
	for i := 0; i < *numGoroutines; i++ {
		go func() {
			defer wg.Done()
			cpuBusyWork(*workIterations)
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	// Final reading.
	maxDelay, avgDelay := runtime.QueueDelay()
	fmt.Printf("\n[final]   max_queue_delay=%12dns (%9.3fms)  avg_queue_delay=%12dns (%9.3fms)\n",
		maxDelay, float64(maxDelay)/1e6,
		avgDelay, float64(avgDelay)/1e6)
	fmt.Printf("completed %d goroutines in %v\n", *numGoroutines, elapsed)

	close(done)
}
