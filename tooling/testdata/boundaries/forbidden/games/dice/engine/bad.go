package engine

import "os"

// ReadEnvForFixture deliberately violates the pure-engine boundary for integration testing.
func ReadEnvForFixture() string {
	return os.Getenv("GAME_NIGHT_BOUNDARY_FIXTURE")
}
