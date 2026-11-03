package domain

import (
	"strings"
	"testing"
)

func TestReactionLabels(t *testing.T) {
	for _, label := range []string{"👍", "nothing to add", "👩🏽‍💻", strings.Repeat("a", 128)} {
		if err := ValidateReaction(label); err != nil {
			t.Errorf("valid %q: %v", label, err)
		}
	}
	for _, label := range []string{"", " ", " ok", "ok ", "a\nb", "a\x00b", string([]byte{0xff}), strings.Repeat("a", 129)} {
		if err := ValidateReaction(label); err == nil {
			t.Errorf("accepted %q", label)
		}
	}
}
