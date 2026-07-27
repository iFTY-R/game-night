// Package adminjobs executes durable admin user-governance items outside the HTTP request lifecycle.
package adminjobs

import (
	"context"
	"errors"
	"strings"
)

var ErrInvalidConfig = errors.New("invalid admin batch worker configuration")

// Processor claims and completes one leased batch item. It owns domain validation and persistence semantics.
type Processor interface {
	ProcessNextBatchItem(context.Context, string) (bool, error)
	ProcessNextErasureJob(context.Context, string) (bool, error)
}

// Config bounds one poll pass so checkpoint delivery and maintenance are not starved by a large governance job.
type Config struct {
	Owner     string
	BatchSize uint32
}

// Dispatcher drains a bounded number of items from the database-backed admin batch queue.
type Dispatcher struct {
	processor Processor
	config    Config
}

// New validates the stable lease owner before the runtime starts claiming admin work.
func New(processor Processor, config Config) (*Dispatcher, error) {
	if processor == nil || strings.TrimSpace(config.Owner) == "" || len(strings.TrimSpace(config.Owner)) > 96 || config.BatchSize == 0 {
		return nil, ErrInvalidConfig
	}
	return &Dispatcher{processor: processor, config: config}, nil
}

// RunOnce processes at most BatchSize durable admin jobs across both queues and returns promptly when both are empty.
func (dispatcher *Dispatcher) RunOnce(ctx context.Context) error {
	if dispatcher == nil || dispatcher.processor == nil || ctx == nil {
		return ErrInvalidConfig
	}
	for processedCount := uint32(0); processedCount < dispatcher.config.BatchSize; {
		madeProgress := false
		processed, err := dispatcher.processor.ProcessNextBatchItem(ctx, dispatcher.config.Owner)
		if err != nil {
			return err
		}
		if processed {
			processedCount++
			madeProgress = true
		}
		if processedCount >= dispatcher.config.BatchSize {
			return nil
		}
		processed, err = dispatcher.processor.ProcessNextErasureJob(ctx, dispatcher.config.Owner)
		if err != nil {
			return err
		}
		if processed {
			processedCount++
			madeProgress = true
		}
		if !madeProgress {
			return nil
		}
	}
	return nil
}
