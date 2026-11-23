package service

import (
	"context"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// agentUsageCacheTTL bounds how stale a rendered counter can be. The list page
// asks on every render; the counters only need to be roughly current.
const agentUsageCacheTTL = 60 * time.Second

// agentUsageService counts agent turns and caches the result per agent.
type agentUsageService struct {
	repo interfaces.AgentUsageRepository
	ttl  time.Duration
	now  func() time.Time

	mu    sync.Mutex
	cache map[string]agentUsageEntry
}

type agentUsageEntry struct {
	usage     types.AgentUsage
	expiresAt time.Time
}

// NewAgentUsageService builds the service.
func NewAgentUsageService(repo interfaces.AgentUsageRepository) interfaces.AgentUsageService {
	return &agentUsageService{
		repo:  repo,
		ttl:   agentUsageCacheTTL,
		now:   time.Now,
		cache: make(map[string]agentUsageEntry),
	}
}

// UsageFor returns usage for the given agents. Cached entries are served until
// they expire; everything else is counted in one grouped query.
//
// A miss for an agent that has never been used is cached too: "no rows" is the
// steady state for a brand-new agent and must not re-query on every render.
func (s *agentUsageService) UsageFor(
	ctx context.Context, agentIDs []string,
) (map[string]*types.AgentUsage, error) {
	usage := make(map[string]*types.AgentUsage, len(agentIDs))
	if s == nil || s.repo == nil || len(agentIDs) == 0 {
		return usage, nil
	}

	now := s.now()
	misses := make([]string, 0, len(agentIDs))
	seen := make(map[string]struct{}, len(agentIDs))

	s.mu.Lock()
	for _, id := range agentIDs {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		if entry, ok := s.cache[id]; ok && now.Before(entry.expiresAt) {
			cached := entry.usage
			usage[id] = &cached
			continue
		}
		misses = append(misses, id)
	}
	s.mu.Unlock()

	if len(misses) == 0 {
		return usage, nil
	}

	since30 := now.AddDate(0, 0, -30)
	since7 := now.AddDate(0, 0, -7)
	counted, err := s.repo.CountByAgents(ctx, misses, since7, since30)
	if err != nil {
		return nil, err
	}

	expiresAt := now.Add(s.ttl)
	s.mu.Lock()
	for _, id := range misses {
		entry := types.AgentUsage{AgentID: id, Total: 0}
		if found, ok := counted[id]; ok && found != nil {
			entry = *found
		}
		usage[id] = &entry
		s.cache[id] = agentUsageEntry{usage: entry, expiresAt: expiresAt}
	}
	// Keep the cache from growing without bound on a long-lived process: drop
	// expired entries while the lock is already held.
	if len(s.cache) > 4096 {
		for id, entry := range s.cache {
			if !now.Before(entry.expiresAt) {
				delete(s.cache, id)
			}
		}
	}
	s.mu.Unlock()

	return usage, nil
}
