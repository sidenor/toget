package storage

import (
	"testing"

	"github.com/sidenor/toget/internal/version"
)

func TestVersionSchemaMatchesStorage(t *testing.T) {
	if version.Schema != CurrentSchemaVersion {
		t.Fatalf("version.Schema = %d, storage schema = %d", version.Schema, CurrentSchemaVersion)
	}
}
