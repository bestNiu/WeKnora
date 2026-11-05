package datasource

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
)

// TestSchedulable pins the states that keep a cron entry. "error" means the last
// run failed, not that the operator disabled the source: dropping the schedule
// there made one transient 02:00 network blip stop syncing for days.
func TestSchedulable(t *testing.T) {
	require.True(t, schedulable(types.DataSourceStatusActive))
	require.True(t, schedulable(types.DataSourceStatusError))
	require.False(t, schedulable(types.DataSourceStatusPaused))
	require.False(t, schedulable(types.DataSourceStatusDeleted))
	require.False(t, schedulable(""))
}
