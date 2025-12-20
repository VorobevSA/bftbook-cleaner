package cleaner

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// startProgressReporter starts a goroutine that reports progress at regular intervals.
// Returns a cancel function to stop the reporter.
func startProgressReporter(
	processed *atomic.Int64,
	total int64,
	message string,
	logger *log.Logger,
) context.CancelFunc {
	progressCtx, progressCancel := context.WithCancel(context.Background())
	go func() {
		ticker := time.NewTicker(progressReportInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				current := processed.Load()
				percent := float64(current) / float64(total) * 100
				logger.Printf(message, current, total, percent)
			case <-progressCtx.Done():
				return
			}
		}
	}()
	return progressCancel
}

// startWorkers starts worker goroutines that process items from itemChan and send results to resultChan.
// Returns a WaitGroup that will be done when all workers finish.
func startWorkers[T any, R any](
	ctx context.Context,
	workerCount int,
	itemChan <-chan T,
	resultChan chan<- R,
	workerFunc func(context.Context, T) R,
) *sync.WaitGroup {
	var wg sync.WaitGroup
	if workerCount <= 0 {
		workerCount = 1
	}

	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case item, ok := <-itemChan:
					if !ok {
						return
					}
					result := workerFunc(ctx, item)
					select {
					case <-ctx.Done():
						return
					case resultChan <- result:
					}
				}
			}
		}()
	}
	return &wg
}

// feedItems sends items to itemChan in a separate goroutine.
func feedItems[T any](ctx context.Context, items []T, itemChan chan<- T) {
	go func() {
		defer close(itemChan)
		for _, item := range items {
			select {
			case <-ctx.Done():
				return
			case itemChan <- item:
			}
		}
	}()
}
