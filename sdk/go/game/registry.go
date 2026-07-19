package game

import (
	"context"
	"reflect"
	"sort"
)

// Registration binds one module to its validated manifest and explicitly marks a new-session default.
type Registration struct {
	Module  ServerGameModule
	Default bool
}

type registeredModule struct {
	manifest Manifest
	module   ServerGameModule
}

// Registry is immutable after construction so session recovery always resolves an exact retained module version.
type Registry struct {
	modules  map[VersionKey]registeredModule
	defaults map[GameID]VersionKey
	keys     []VersionKey
}

// NewRegistry validates all manifests, exact keys, duplicates, and one explicit default per game ID.
func NewRegistry(registrations ...Registration) (*Registry, error) {
	if len(registrations) == 0 {
		return nil, ErrDefaultRegistration
	}
	registry := &Registry{
		modules:  make(map[VersionKey]registeredModule, len(registrations)),
		defaults: make(map[GameID]VersionKey),
		keys:     make([]VersionKey, 0, len(registrations)),
	}
	registeredGames := make(map[GameID]struct{})
	for _, registration := range registrations {
		if nilModule(registration.Module) {
			return nil, ErrInvalidManifest
		}
		manifest, err := ValidateManifest(registration.Module.Manifest())
		if err != nil {
			return nil, err
		}
		key := manifest.Key()
		if _, duplicate := registry.modules[key]; duplicate {
			return nil, ErrDuplicateRegistration
		}
		registry.modules[key] = registeredModule{manifest: manifest, module: registration.Module}
		registry.keys = append(registry.keys, key)
		registeredGames[key.GameID] = struct{}{}
		if registration.Default {
			if _, duplicate := registry.defaults[key.GameID]; duplicate {
				return nil, ErrDefaultRegistration
			}
			registry.defaults[key.GameID] = key
		}
	}
	for gameID := range registeredGames {
		if _, present := registry.defaults[gameID]; !present {
			return nil, ErrDefaultRegistration
		}
	}
	sort.Slice(registry.keys, func(left, right int) bool {
		return versionKeyText(registry.keys[left]) < versionKeyText(registry.keys[right])
	})
	return registry, nil
}

// Resolve returns only an exact module tuple and never substitutes the default or a newer compatible-looking version.
func (registry *Registry) Resolve(key VersionKey) (ServerGameModule, error) {
	if registry == nil || !key.Valid() {
		return nil, ErrVersionNotRegistered
	}
	registered, found := registry.modules[key]
	if !found {
		return nil, ErrVersionNotRegistered
	}
	return registered.module, nil
}

// Manifest returns a defensive exact-version manifest for persistence, recovery checks, or client bootstrap metadata.
func (registry *Registry) Manifest(key VersionKey) (Manifest, error) {
	if registry == nil || !key.Valid() {
		return Manifest{}, ErrVersionNotRegistered
	}
	registered, found := registry.modules[key]
	if !found {
		return Manifest{}, ErrVersionNotRegistered
	}
	return registered.manifest.Clone(), nil
}

// DefaultManifest returns the explicitly selected release used only when creating a new session.
func (registry *Registry) DefaultManifest(ctx context.Context, gameID GameID) (Manifest, error) {
	if registry == nil || ctx == nil {
		return Manifest{}, ErrGameNotRegistered
	}
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	if _, err := ParseGameID(string(gameID)); err != nil {
		return Manifest{}, ErrGameNotRegistered
	}
	key, found := registry.defaults[gameID]
	if !found {
		return Manifest{}, ErrGameNotRegistered
	}
	return registry.Manifest(key)
}

// DefaultModule returns the new-session module paired with DefaultManifest without affecting exact recovery lookup.
func (registry *Registry) DefaultModule(ctx context.Context, gameID GameID) (ServerGameModule, error) {
	manifest, err := registry.DefaultManifest(ctx, gameID)
	if err != nil {
		return nil, err
	}
	return registry.Resolve(manifest.Key())
}

// Manifests returns every retained exact version in deterministic key order and with independent theme slices.
func (registry *Registry) Manifests() []Manifest {
	if registry == nil {
		return []Manifest{}
	}
	manifests := make([]Manifest, 0, len(registry.keys))
	for _, key := range registry.keys {
		manifests = append(manifests, registry.modules[key].manifest.Clone())
	}
	return manifests
}

func nilModule(module ServerGameModule) bool {
	if module == nil {
		return true
	}
	value := reflect.ValueOf(module)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func versionKeyText(key VersionKey) string {
	return string(key.GameID) + "\x00" + string(key.Engine) + "\x00" + string(key.Protocol) + "\x00" + string(key.Client)
}
