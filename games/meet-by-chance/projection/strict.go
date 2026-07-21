package projection

import (
	"bytes"

	game "github.com/iFTY-R/game-night/sdk/go/game"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func unmarshalStrict(payload []byte, message proto.Message) error {
	if message == nil || len(payload) > game.MaximumMessageBytes {
		return projectionError("protobuf payload exceeds replay bound")
	}
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(payload, message); err != nil || projectionHasUnknown(message.ProtoReflect()) {
		return projectionError("replay payload is malformed or contains unknown fields")
	}
	canonical, err := (proto.MarshalOptions{Deterministic: true}).Marshal(message)
	if err != nil || !bytes.Equal(canonical, payload) {
		return projectionError("replay payload is not canonical")
	}
	return nil
}

func projectionHasUnknown(message protoreflect.Message) bool {
	if len(message.GetUnknown()) != 0 {
		return true
	}
	unknown := false
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		if field.IsList() && (field.Kind() == protoreflect.MessageKind || field.Kind() == protoreflect.GroupKind) {
			list := value.List()
			for index := 0; index < list.Len(); index++ {
				if projectionHasUnknown(list.Get(index).Message()) {
					unknown = true
					return false
				}
			}
		} else if field.IsMap() && (field.MapValue().Kind() == protoreflect.MessageKind || field.MapValue().Kind() == protoreflect.GroupKind) {
			value.Map().Range(func(_ protoreflect.MapKey, nested protoreflect.Value) bool {
				if projectionHasUnknown(nested.Message()) {
					unknown = true
					return false
				}
				return true
			})
		} else if field.Kind() == protoreflect.MessageKind || field.Kind() == protoreflect.GroupKind {
			unknown = projectionHasUnknown(value.Message())
		}
		return !unknown
	})
	return unknown
}
