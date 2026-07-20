// Package module adapts the pure dice-789 rules to the platform game SDK.
//
// The adapter owns protocol envelopes, deterministic state transitions, and
// viewer-safe serialization. Persistence, clocks, authorization, and random
// sources remain runtime responsibilities.
package module

import (
	"bytes"

	"github.com/iFTY-R/game-night/games/dice-789/engine"
	game "github.com/iFTY-R/game-night/sdk/go/game"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// malformed classifies untrusted protobuf and envelope failures without
// exposing internal decoder details through the public transport.
func malformed(detail string) error {
	return &engine.RuleError{Code: engine.CodeMalformedPayload, Detail: detail}
}

// marshalDeterministic ensures retries and replay produce byte-identical
// payloads for the same logical state or event.
func marshalDeterministic(message proto.Message) ([]byte, error) {
	if message == nil {
		return nil, malformed("nil protobuf message")
	}
	return (proto.MarshalOptions{Deterministic: true}).Marshal(message)
}

// unmarshalStrict rejects unknown fields and alternate wire encodings so an
// action digest cannot represent multiple byte-level payloads.
func unmarshalStrict(payload []byte, message proto.Message) error {
	if message == nil || len(payload) > game.MaximumMessageBytes {
		return malformed("protobuf payload exceeds module bound")
	}
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(payload, message); err != nil || hasUnknown(message.ProtoReflect()) {
		return malformed("protobuf payload is malformed or contains unknown fields")
	}
	canonical, err := marshalDeterministic(message)
	if err != nil || !bytes.Equal(canonical, payload) {
		return malformed("protobuf payload is not canonical")
	}
	return nil
}

// hasUnknown walks nested messages because protobuf keeps unknown bytes at the
// exact submessage where they were decoded.
func hasUnknown(message protoreflect.Message) bool {
	if len(message.GetUnknown()) != 0 {
		return true
	}
	unknown := false
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		switch {
		case field.IsList() && (field.Kind() == protoreflect.MessageKind || field.Kind() == protoreflect.GroupKind):
			list := value.List()
			for index := 0; index < list.Len(); index++ {
				if hasUnknown(list.Get(index).Message()) {
					unknown = true
					return false
				}
			}
		case field.IsMap() && (field.MapValue().Kind() == protoreflect.MessageKind || field.MapValue().Kind() == protoreflect.GroupKind):
			value.Map().Range(func(_ protoreflect.MapKey, nested protoreflect.Value) bool {
				if hasUnknown(nested.Message()) {
					unknown = true
					return false
				}
				return true
			})
		case field.Kind() == protoreflect.MessageKind || field.Kind() == protoreflect.GroupKind:
			unknown = hasUnknown(value.Message())
		}
		return !unknown
	})
	return unknown
}
