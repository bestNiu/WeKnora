package chatpipeline

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRepairJSONPayload pins the malformations table-heavy chunks produce. Every
// case must end up parseable and keep the same number of entities.
func TestRepairJSONPayload(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		valid bool // already parseable
	}{
		{"missing outer bracket", `[{"entity":"A"},{"entity":"B"}`, false},
		{"trailing comma before array close", `[{"entity":"A"},]`, false},
		{"trailing comma before object close", `{"a":1,}`, false},
		{"unclosed inner array", `[{"entity":"A","attrs":["x","y"}]`, false},
		{"already valid", `[{"entity":"A"}]`, true},
		{"nested arrays intact", `[{"a":[1,2]},{"b":2}]`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.valid {
				require.True(t, json.Valid([]byte(tc.in)))
				_, changed := repairJSONPayload(tc.in)
				require.False(t, changed, "valid payload must not be rewritten")
				return
			}
			repaired, changed := repairJSONPayload(tc.in)
			require.True(t, changed)
			require.Truef(t, json.Valid([]byte(repaired)), "repaired payload must parse: %s", repaired)
		})
	}
}
