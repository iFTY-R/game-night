package gameruntime

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestSessionNotificationRoundTripAndStrictParsing(t *testing.T) {
	notification := SessionNotification{SessionID: uuid.New(), StateVersion: 7}
	payload, err := MarshalSessionNotification(notification)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := ParseSessionNotification(payload)
	if err != nil || restored != notification {
		t.Fatalf("restored=%+v error=%v", restored, err)
	}
	for _, payload := range [][]byte{
		nil,
		[]byte(`{"sessionId":"` + notification.SessionID.String() + `","stateVersion":0}`),
		[]byte(`{"sessionId":"` + notification.SessionID.String() + `","stateVersion":7,"state":"secret"}`),
		append(append([]byte(nil), payload...), []byte(` {}`)...),
	} {
		if _, err := ParseSessionNotification(payload); !errors.Is(err, ErrInvalidSessionInput) {
			t.Fatalf("payload %q error = %v", payload, err)
		}
	}
}
