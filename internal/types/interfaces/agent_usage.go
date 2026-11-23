package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// AgentUsageRepository counts how often each agent was used.
type AgentUsageRepository interface {
	// CountByAgents returns usage for the given agents, keyed by agent ID.
	// Agents with no turns are absent from the map (callers render 0).
	// since7 / since30 are the lower bounds of the rolling windows.
	CountByAgents(
		ctx context.Context, agentIDs []string, since7, since30 time.Time,
	) (map[string]*types.AgentUsage, error)
}

// AgentUsageService is the application-layer view of agent usage counters.
type AgentUsageService interface {
	// UsageFor returns usage for the given agents, keyed by agent ID. Results
	// are cached briefly: the list page asks on every render and the counters
	// do not need to be live to the second.
	UsageFor(ctx context.Context, agentIDs []string) (map[string]*types.AgentUsage, error)
}
