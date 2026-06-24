package version_test

import (
	"testing"

	"github.com/aleksclark/bezalel/internal/version"
)

func TestUserAgentComposition(t *testing.T) {
	want := version.Name + "/" + version.Number
	if version.UserAgent != want {
		t.Errorf("UserAgent = %q, want %q", version.UserAgent, want)
	}
}
