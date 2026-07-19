package game

import (
	"errors"
	"testing"
)

func TestValidateManifestCanonicalizesRecommendedRangeAndDefendsThemes(t *testing.T) {
	manifest := validManifest("dice", "1.0.0")
	validated, err := ValidateManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if validated.Participants.RecommendedMinimum != 2 || validated.Participants.RecommendedMaximum != 9 {
		t.Fatalf("participants = %+v", validated.Participants)
	}
	manifest.Themes.Variants[0] = "mutated"
	if validated.Themes.Variants[0] != "classic" {
		t.Fatalf("validated themes retained caller slice: %v", validated.Themes.Variants)
	}
}

func TestValidateManifestRejectsInvalidIdentityVersionsCapabilitiesAndThemes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{name: "game ID", mutate: func(value *Manifest) { value.GameID = "Dice Game" }},
		{name: "version", mutate: func(value *Manifest) { value.Versions.Engine = "latest" }},
		{name: "participant range", mutate: func(value *Manifest) { value.Participants.Maximum = 1 }},
		{name: "submission", mutate: func(value *Manifest) { value.Capabilities.Submission = "unknown" }},
		{name: "table shape", mutate: func(value *Manifest) { value.Presentation.TableShape = "circle" }},
		{name: "duplicate theme", mutate: func(value *Manifest) { value.Themes.Variants = []Identifier{"classic", "classic"} }},
		{name: "missing default", mutate: func(value *Manifest) { value.Themes.Variants = []Identifier{"safe"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validManifest("dice", "1.0.0")
			test.mutate(&manifest)
			if _, err := ValidateManifest(manifest); !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("validation error = %v", err)
			}
		})
	}
}

func TestVersionKeyRequiresExactCanonicalTuple(t *testing.T) {
	manifest, err := ValidateManifest(validManifest("three-rounds", "2.1.0-rc.1+build.7"))
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Key().Valid() {
		t.Fatalf("key = %+v", manifest.Key())
	}
	invalid := manifest.Key()
	invalid.Protocol = "1"
	if invalid.Valid() {
		t.Fatalf("invalid key accepted: %+v", invalid)
	}
}

func validManifest(gameID GameID, engineVersion Version) Manifest {
	return Manifest{
		GameID:       gameID,
		Versions:     VersionSet{Engine: engineVersion, Protocol: "1.0.0", Client: "1.0.0"},
		Participants: ParticipantLimits{Minimum: 2, Maximum: 9},
		Capabilities: Capabilities{
			Submission: SubmissionModeTurnBased, Timers: true, Spectating: true, Replay: true, Reveal: RevealPolicyRuleControlled,
		},
		Presentation: PresentationPreferences{
			TableShape: TableShapeAdaptive, Orientation: OrientationResponsive, ActionDock: ActionDockSeatAnchored,
		},
		Themes: ThemePreferences{
			Default: "classic", Fallback: "safe", Variants: []Identifier{"classic", "safe"},
		},
	}
}
