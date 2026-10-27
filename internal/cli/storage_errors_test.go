package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/marcus/comms/internal/app"
)

type opaqueDriverError int

func (e opaqueDriverError) Error() string {
	return "SQLITE_CONSTRAINT: UNIQUE constraint failed: private_table.private_column (2067)"
}
func (e opaqueDriverError) Code() int { return int(e) }

func TestCLIStorageErrorsNeverExposeDriverText(t *testing.T) {
	for _, code := range []int{19, 275, 787, 1299, 1555, 1811, 2067, 2579, 3091, 11} {
		for _, jsonOutput := range []bool{false, true} {
			for _, err := range []error{opaqueDriverError(code), fmt.Errorf("%w: %w", app.ErrConflict, opaqueDriverError(code))} {
				var out, stderr bytes.Buffer
				if exit := fail(Env{Stdout: &out, Stderr: &stderr}, jsonOutput, err); exit == 0 || out.Len() != 0 {
					t.Fatalf("exit=%d out=%s", exit, &out)
				}
				text := strings.ToLower(stderr.String())
				for _, forbidden := range []string{"sqlite", "unique constraint", "private_table", "private_column", "2067"} {
					if strings.Contains(text, forbidden) {
						t.Fatalf("driver data leaked: %s", text)
					}
				}
			}
		}
	}
}
