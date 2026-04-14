package uuidutil

import "github.com/google/uuid"

// NewV7String returns UUIDv7 and falls back to UUIDv4 on failure.
func NewV7String() string {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return id.String()
}
