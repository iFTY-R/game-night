package game

import (
	"regexp"
)

const (
	// maximumIdentifierBytes bounds identifiers copied into registries, protocol metadata, and static asset paths.
	maximumIdentifierBytes = 64
	// maximumVersionBytes bounds exact version keys while leaving room for prerelease and build metadata.
	maximumVersionBytes = 64
	// MaximumParticipants is the SDK-wide defensive ceiling; each game declares a lower supported maximum.
	MaximumParticipants uint32 = 64
	// maximumThemeVariants prevents malformed manifests from causing unbounded registry allocations.
	maximumThemeVariants = 32
)

var (
	identifierPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
	versionPattern    = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
)

// Identifier is a canonical lowercase name used for messages, themes, timers, and other module-owned keys.
type Identifier string

// GameID is the stable product identity shared by room selection and every retained version registration.
type GameID string

// Version is a canonical semantic version used as an exact registry key; no implicit compatibility ordering is applied.
type Version string

// VersionSet pins every artifact required to create or recover one session.
type VersionSet struct {
	Engine   Version
	Protocol Version
	Client   Version
}

// VersionKey identifies exactly one retained server module and its matching protocol/client artifacts.
type VersionKey struct {
	GameID   GameID
	Engine   Version
	Protocol Version
	Client   Version
}

// ParticipantLimits defines hard engine bounds plus the range highlighted by discovery clients.
type ParticipantLimits struct {
	Minimum            uint32
	Maximum            uint32
	RecommendedMinimum uint32
	RecommendedMaximum uint32
}

// SubmissionMode describes whether legal actions are sequential, simultaneous, or phase-dependent.
type SubmissionMode string

const (
	SubmissionModeTurnBased    SubmissionMode = "turn_based"
	SubmissionModeSimultaneous SubmissionMode = "simultaneous"
	SubmissionModeMixed        SubmissionMode = "mixed"
)

// RevealPolicy defines the strongest information disclosure a game module may make after rules permit it.
type RevealPolicy string

const (
	RevealPolicyRuleControlled RevealPolicy = "rule_controlled"
	RevealPolicyPublicAtFinish RevealPolicy = "public_at_finish"
	RevealPolicyNever          RevealPolicy = "never"
)

// Capabilities declares runtime facilities and projection surfaces a module is prepared to use.
type Capabilities struct {
	Submission SubmissionMode
	Timers     bool
	Spectating bool
	Replay     bool
	Reveal     RevealPolicy
}

// TableShape is a layout preference only; it cannot change logical seat order or game rules.
type TableShape string

const (
	TableShapeAdaptive      TableShape = "adaptive"
	TableShapeCompactOval   TableShape = "compact_oval"
	TableShapeElongatedOval TableShape = "elongated_oval"
	TableShapeRoundedTable  TableShape = "rounded_table"
)

// OrientationPolicy guides responsive composition without requiring a device orientation lock.
type OrientationPolicy string

const (
	OrientationResponsive         OrientationPolicy = "responsive"
	OrientationPortraitPreferred  OrientationPolicy = "portrait_preferred"
	OrientationLandscapePreferred OrientationPolicy = "landscape_preferred"
)

// ActionDockPreference identifies the game surface edge from which the collapsible action tray grows.
type ActionDockPreference string

const (
	ActionDockSeatAnchored ActionDockPreference = "seat_anchored"
	ActionDockBottomEdge   ActionDockPreference = "bottom_edge"
)

// PresentationPreferences are safe client layout hints and never authoritative game state.
type PresentationPreferences struct {
	TableShape  TableShape
	Orientation OrientationPolicy
	ActionDock  ActionDockPreference
}

// ThemePreferences pins the default, safe fallback, and complete selectable theme ID set.
type ThemePreferences struct {
	Default  Identifier
	Fallback Identifier
	Variants []Identifier
}

// Manifest is the validated cross-runtime description of one exact game module release.
type Manifest struct {
	GameID       GameID
	Versions     VersionSet
	Participants ParticipantLimits
	Capabilities Capabilities
	Presentation PresentationPreferences
	Themes       ThemePreferences
}

// ParseIdentifier accepts only canonical lowercase identifiers safe for protocol metadata and asset paths.
func ParseIdentifier(value string) (Identifier, error) {
	if len(value) == 0 || len(value) > maximumIdentifierBytes || !identifierPattern.MatchString(value) {
		return "", ErrInvalidManifest
	}
	return Identifier(value), nil
}

// ParseGameID validates the stable game identity independently from any artifact version.
func ParseGameID(value string) (GameID, error) {
	identifier, err := ParseIdentifier(value)
	return GameID(identifier), err
}

// ParseVersion validates canonical semantic-version text without choosing compatibility or upgrade behavior.
func ParseVersion(value string) (Version, error) {
	if len(value) == 0 || len(value) > maximumVersionBytes || !versionPattern.MatchString(value) {
		return "", ErrInvalidManifest
	}
	return Version(value), nil
}

// Valid reports whether both hard and optional recommended participant ranges are internally consistent.
func (limits ParticipantLimits) Valid() bool {
	if limits.Minimum == 0 || limits.Maximum < limits.Minimum || limits.Maximum > MaximumParticipants {
		return false
	}
	if limits.RecommendedMinimum == 0 && limits.RecommendedMaximum == 0 {
		return true
	}
	return limits.RecommendedMinimum >= limits.Minimum && limits.RecommendedMaximum >= limits.RecommendedMinimum &&
		limits.RecommendedMaximum <= limits.Maximum
}

// Key returns the exact immutable registry coordinates declared by this manifest.
func (manifest Manifest) Key() VersionKey {
	return VersionKey{
		GameID: manifest.GameID, Engine: manifest.Versions.Engine,
		Protocol: manifest.Versions.Protocol, Client: manifest.Versions.Client,
	}
}

// Clone returns a defensive manifest copy so registry-owned theme slices cannot be mutated by modules or callers.
func (manifest Manifest) Clone() Manifest {
	manifest.Themes.Variants = append([]Identifier(nil), manifest.Themes.Variants...)
	return manifest
}

// ValidateManifest validates and canonicalizes one module declaration without retaining caller-owned slices.
func ValidateManifest(manifest Manifest) (Manifest, error) {
	if _, err := ParseGameID(string(manifest.GameID)); err != nil {
		return Manifest{}, ErrInvalidManifest
	}
	for _, version := range []Version{manifest.Versions.Engine, manifest.Versions.Protocol, manifest.Versions.Client} {
		if _, err := ParseVersion(string(version)); err != nil {
			return Manifest{}, ErrInvalidManifest
		}
	}
	if !manifest.Participants.Valid() || !manifest.Capabilities.Valid() || !manifest.Presentation.Valid() {
		return Manifest{}, ErrInvalidManifest
	}
	if manifest.Participants.RecommendedMinimum == 0 {
		manifest.Participants.RecommendedMinimum = manifest.Participants.Minimum
		manifest.Participants.RecommendedMaximum = manifest.Participants.Maximum
	}
	themes, err := validateThemePreferences(manifest.Themes)
	if err != nil {
		return Manifest{}, err
	}
	manifest.Themes = themes
	return manifest.Clone(), nil
}

// Valid reports whether a complete exact version tuple can be used as a registry lookup key.
func (key VersionKey) Valid() bool {
	_, gameErr := ParseGameID(string(key.GameID))
	_, engineErr := ParseVersion(string(key.Engine))
	_, protocolErr := ParseVersion(string(key.Protocol))
	_, clientErr := ParseVersion(string(key.Client))
	return gameErr == nil && engineErr == nil && protocolErr == nil && clientErr == nil
}

// Valid rejects unspecified submission or reveal behavior before a runtime exposes module capabilities.
func (capabilities Capabilities) Valid() bool {
	submissionValid := capabilities.Submission == SubmissionModeTurnBased || capabilities.Submission == SubmissionModeSimultaneous ||
		capabilities.Submission == SubmissionModeMixed
	revealValid := capabilities.Reveal == RevealPolicyRuleControlled || capabilities.Reveal == RevealPolicyPublicAtFinish ||
		capabilities.Reveal == RevealPolicyNever
	return submissionValid && revealValid
}

// Valid rejects presentation hints unknown to the common responsive table surface.
func (preferences PresentationPreferences) Valid() bool {
	tableValid := preferences.TableShape == TableShapeAdaptive || preferences.TableShape == TableShapeCompactOval ||
		preferences.TableShape == TableShapeElongatedOval || preferences.TableShape == TableShapeRoundedTable
	orientationValid := preferences.Orientation == OrientationResponsive || preferences.Orientation == OrientationPortraitPreferred ||
		preferences.Orientation == OrientationLandscapePreferred
	dockValid := preferences.ActionDock == ActionDockSeatAnchored || preferences.ActionDock == ActionDockBottomEdge
	return tableValid && orientationValid && dockValid
}

func validateThemePreferences(preferences ThemePreferences) (ThemePreferences, error) {
	if _, err := ParseIdentifier(string(preferences.Default)); err != nil {
		return ThemePreferences{}, ErrInvalidManifest
	}
	if _, err := ParseIdentifier(string(preferences.Fallback)); err != nil {
		return ThemePreferences{}, ErrInvalidManifest
	}
	if len(preferences.Variants) == 0 || len(preferences.Variants) > maximumThemeVariants {
		return ThemePreferences{}, ErrInvalidManifest
	}
	variants := append([]Identifier(nil), preferences.Variants...)
	seen := make(map[Identifier]struct{}, len(variants))
	for _, variant := range variants {
		if _, err := ParseIdentifier(string(variant)); err != nil {
			return ThemePreferences{}, ErrInvalidManifest
		}
		if _, duplicate := seen[variant]; duplicate {
			return ThemePreferences{}, ErrInvalidManifest
		}
		seen[variant] = struct{}{}
	}
	if _, present := seen[preferences.Default]; !present {
		return ThemePreferences{}, ErrInvalidManifest
	}
	preferences.Variants = variants
	return preferences, nil
}
