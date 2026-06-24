package version_test

import (
	"testing"

	"github.com/aleksclark/bezalel/internal/version"
)

func TestUserAgentComposition(t *testing.T) {
	want := version.Name + "/" + version.Number
	if got := version.UserAgent(); got != want {
		t.Errorf("UserAgent() = %q, want %q", got, want)
	}
}
