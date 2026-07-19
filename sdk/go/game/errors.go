package game

import "errors"

var (
	// ErrInvalidManifest rejects incomplete, non-canonical, or internally inconsistent game metadata.
	ErrInvalidManifest = errors.New("invalid game manifest")
	// ErrInvalidContract rejects malformed runtime messages, snapshots, deterministic inputs, or viewer context.
	ErrInvalidContract = errors.New("invalid game contract value")
	// ErrDuplicateRegistration rejects two modules claiming the same exact version tuple.
	ErrDuplicateRegistration = errors.New("duplicate game module registration")
	// ErrDefaultRegistration rejects missing or ambiguous default versions for a registered game ID.
	ErrDefaultRegistration = errors.New("invalid default game module registration")
	// ErrGameNotRegistered reports a game ID absent from the immutable build registry.
	ErrGameNotRegistered = errors.New("game is not registered")
	// ErrVersionNotRegistered reports an unavailable exact version tuple without substituting a newer module.
	ErrVersionNotRegistered = errors.New("game version is not registered")
)
