package challenge

import (
	"bytes"
	"encoding/binary"

	"github.com/iFTY-R/game-night/platform/security"
)

const canonicalClaimsNamespace = "game-night.challenge-proof.v1"

// canonicalClaims uses length-prefixed binary fields so values cannot collide through delimiter injection.
func canonicalClaims[P security.HMACKeyPurpose](snapshot Snapshot[P]) ([]byte, error) {
	if snapshot.Binding.Validate() != nil || snapshot.ID == [16]byte{} || snapshot.Selector.ByteLength() != SelectorBytes ||
		snapshot.SecretMAC.KeyVersion == 0 || snapshot.ExpiresAt.IsZero() {
		return nil, ErrInvalidInput
	}

	var output bytes.Buffer
	writeString(&output, canonicalClaimsNamespace)
	output.Write(snapshot.ID[:])
	writeString(&output, snapshot.Selector.Value())
	writeString(&output, string(snapshot.Binding.Purpose))
	writeString(&output, string(snapshot.Binding.Audience))
	output.Write(snapshot.Binding.Origin[:])
	writeString(&output, string(snapshot.Binding.RequestFlowID))
	_ = binary.Write(&output, binary.BigEndian, snapshot.ExpiresAt.UnixMicro())
	_ = binary.Write(&output, binary.BigEndian, snapshot.SecretMAC.KeyVersion)
	if snapshot.Binding.Subject.Bound() {
		output.WriteByte(1)
		output.Write(snapshot.Binding.Subject.ID[:])
		_ = binary.Write(&output, binary.BigEndian, snapshot.Binding.Subject.Version)
		_ = binary.Write(&output, binary.BigEndian, snapshot.Binding.Subject.CredentialVersion)
	} else {
		output.WriteByte(0)
	}
	return output.Bytes(), nil
}

func writeString(output *bytes.Buffer, value string) {
	_ = binary.Write(output, binary.BigEndian, uint16(len(value)))
	output.WriteString(value)
}
