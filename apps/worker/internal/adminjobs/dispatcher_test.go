package adminjobs

import (
	"context"
	"testing"
)

func TestDispatcherRunOnceSharesBatchBudgetAcrossBatchAndErasureQueues(t *testing.T) {
	processor := &stubProcessor{
		batchResults:   []bool{true},
		erasureResults: []bool{true},
	}
	dispatcher, err := New(processor, Config{Owner: "worker-a", BatchSize: 2})
	if err != nil {
		t.Fatal(err)
	}

	if err = dispatcher.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if processor.batchCalls != 1 || processor.erasureCalls != 1 {
		t.Fatalf("unexpected calls: batch=%d erasure=%d", processor.batchCalls, processor.erasureCalls)
	}
}

func TestDispatcherRunOnceStopsWhenBothQueuesAreEmpty(t *testing.T) {
	processor := &stubProcessor{
		batchResults:   []bool{false},
		erasureResults: []bool{false},
	}
	dispatcher, err := New(processor, Config{Owner: "worker-b", BatchSize: 4})
	if err != nil {
		t.Fatal(err)
	}

	if err = dispatcher.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if processor.batchCalls != 1 || processor.erasureCalls != 1 {
		t.Fatalf("unexpected calls: batch=%d erasure=%d", processor.batchCalls, processor.erasureCalls)
	}
}

type stubProcessor struct {
	batchResults   []bool
	erasureResults []bool
	batchCalls     int
	erasureCalls   int
}

func (processor *stubProcessor) ProcessNextBatchItem(context.Context, string) (bool, error) {
	result := false
	if processor.batchCalls < len(processor.batchResults) {
		result = processor.batchResults[processor.batchCalls]
	}
	processor.batchCalls++
	return result, nil
}

func (processor *stubProcessor) ProcessNextErasureJob(context.Context, string) (bool, error) {
	result := false
	if processor.erasureCalls < len(processor.erasureResults) {
		result = processor.erasureResults[processor.erasureCalls]
	}
	processor.erasureCalls++
	return result, nil
}
