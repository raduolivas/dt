package logx_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/raduolivas/dt/backend/pkg/logx"
	"github.com/raduolivas/dt/backend/pkg/testkit"
)

// redactKeyLine matches one entry of the collector's redact list:
//   - {key: lat, action: delete}
var redactKeyLine = regexp.MustCompile(`^- \{key: ([a-z0-9_.-]+), action: delete\}$`)

// The collector deletes span and log attributes by exact key; the list it uses
// must stay equal to logx's own sensitive list (spec §4.6).
func TestCollectorRedactKeysMatchLogx(t *testing.T) {
	path := filepath.Join(testkit.RepoRoot(t), "observability/collector/redact-keys.yaml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	var got []string
	for _, line := range regexp.MustCompile(`\r?\n`).Split(string(raw), -1) {
		if line == "" || line[0] == '#' {
			continue
		}
		m := redactKeyLine.FindStringSubmatch(line)
		require.NotNil(t, m, "unexpected line in redact-keys.yaml: %q", line)
		got = append(got, m[1])
	}
	sort.Strings(got)
	require.Equal(t, logx.SensitiveKeys(), got)
}
