package uuidutil

import "github.com/google/uuid"

// NewV7String returns a canonical UUID string.
// It prefers UUIDv7 and falls back to a regular random UUID if v7 generation fails.
func NewV7String() string {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return id.String()
}
