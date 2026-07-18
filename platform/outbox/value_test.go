package outbox

import (
	"errors"
	"strings"
	"testing"
)

func TestProtocolValueObjectsRejectUnqualifiedOrUnboundedValues(t *testing.T) {
	t.Parallel()

	invalid := []string{"", "audit", "Audit.Checkpoint", "audit checkpoint", "audit..checkpoint", strings.Repeat("a", MaximumNameLength+1) + ".x"}
	for _, value := range invalid {
		value := value
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseEventType(value); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("ParseEventType(%q) error = %v, want ErrInvalidInput", value, err)
			}
			if _, err := ParseAggregateType(value); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("ParseAggregateType(%q) error = %v, want ErrInvalidInput", value, err)
			}
			if _, err := ParseConsumerID(value); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("ParseConsumerID(%q) error = %v, want ErrInvalidInput", value, err)
			}
			if _, err := ParseErrorCode(value); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("ParseErrorCode(%q) error = %v, want ErrInvalidInput", value, err)
			}
		})
	}

	if !EventTypeAuditCheckpointPending.Valid() {
		t.Fatal("audit checkpoint event type must remain a valid protocol constant")
	}
	if !ConsumerIDAuditCheckpoint.Valid() {
		t.Fatal("audit checkpoint consumer ID must remain a valid protocol constant")
	}
}

func TestSubscriptionRejectsDuplicatesAndCopiesTypes(t *testing.T) {
	t.Parallel()

	types := []EventType{EventTypeAuditCheckpointPending, "identity.user.suspended"}
	subscription, err := NewSubscription(types...)
	if err != nil {
		t.Fatalf("NewSubscription() error = %v", err)
	}
	types[0] = "identity.user.deleted"
	if !subscription.Contains(EventTypeAuditCheckpointPending) {
		t.Fatal("constructor retained caller-owned subscription slice")
	}

	returned := subscription.Types()
	returned[0] = "identity.user.deleted"
	if !subscription.Contains(EventTypeAuditCheckpointPending) {
		t.Fatal("Types returned mutable subscription storage")
	}

	if _, err := NewSubscription(EventTypeAuditCheckpointPending, EventTypeAuditCheckpointPending); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("duplicate subscription error = %v, want ErrInvalidInput", err)
	}

	reordered, err := NewSubscription("identity.user.suspended", EventTypeAuditCheckpointPending)
	if err != nil {
		t.Fatalf("reordered NewSubscription() error = %v", err)
	}
	if got, want := reordered.Types(), subscription.Types(); got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("canonical subscription = %v, want %v", got, want)
	}
}
