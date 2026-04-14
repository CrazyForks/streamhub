package uuidutil

import (
	"testing"

	"github.com/google/uuid"
)

func TestNewV7String(t *testing.T) {
	value := NewV7String()
	parsed, err := uuid.Parse(value)
	if err != nil {
		t.Fatalf("uuid.Parse(%q) error = %v", value, err)
	}
	if parsed.Version() != 7 && parsed.Version() != 4 {
		t.Fatalf("expected UUIDv7 or fallback UUIDv4, got v%d", parsed.Version())
	}
}
