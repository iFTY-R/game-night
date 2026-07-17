package identity

import "net/http"

// NewForbiddenClient keeps the fixture import reachable so the boundary CLI must report it.
func NewForbiddenClient() *http.Client {
	return &http.Client{}
}
