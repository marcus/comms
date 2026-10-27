package help

import (
	"os"
	"strings"
	"testing"
)

func TestInstallDocumentsTrustBeforeFormulaLoad(t *testing.T) {
	for _, path := range []string{"../../README.md", "../../docs/releasing.md"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		trust, install := strings.Index(text, "brew trust --formula marcus/tap/comms"), strings.Index(text, "brew install marcus/tap/comms")
		if trust < 0 || install < trust {
			t.Fatalf("%s must document trust before installation", path)
		}
	}
}
