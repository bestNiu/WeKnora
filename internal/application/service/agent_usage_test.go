package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

type usageRepoStub struct {
	usage map[string]*types.AgentUsage
	calls int
	err   error
}

func (s *usageRepoStub) CountByAgents(
	_ context.Context, _ []string, _, _ time.Time,
) (map[string]*types.AgentUsage, error) {
	s.calls++
	return s.usage, s.err
}

func TestAgentUsageForCountsAndZeroFills(t *testing.T) {
	repo := &usageRepoStub{usage: map[string]*types.AgentUsage{
		"a": {AgentID: "a", Total: 5, Last7Days: 2, Last30Days: 3},
	}}
	svc := NewAgentUsageService(repo)

	usage, err := svc.UsageFor(context.Background(), []string{"a", "a", "b", ""})
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if repo.calls != 1 {
		t.Fatalf("repo calls = %d, want one grouped query", repo.calls)
	}
	if usage["a"].Total != 5 || usage["a"].Last7Days != 2 {
		t.Errorf("usage[a] = %+v", usage["a"])
	}
	if usage["b"] == nil || usage["b"].Total != 0 {
		t.Errorf("an unused agent must still be reported as zero, got %+v", usage["b"])
	}
	if _, ok := usage[""]; ok {
		t.Error("blank ids must be dropped, not answered")
	}
}

// The list page asks on every render; the cache is what keeps that from
// becoming a grouped count per render.
func TestAgentUsageForServesFromCache(t *testing.T) {
	repo := &usageRepoStub{usage: map[string]*types.AgentUsage{"a": {AgentID: "a", Total: 1}}}
	svc := NewAgentUsageService(repo)

	now := time.Now()
	svc.(*agentUsageService).now = func() time.Time { return now }

	_, _ = svc.UsageFor(context.Background(), []string{"a"})
	_, _ = svc.UsageFor(context.Background(), []string{"a"})
	if repo.calls != 1 {
		t.Fatalf("repo calls = %d, want the second read served from cache", repo.calls)
	}

	// Past the TTL the counter is re-read: a stale badge for a minute is fine,
	// a stale one forever is not.
	svc.(*agentUsageService).now = func() time.Time { return now.Add(agentUsageCacheTTL + time.Second) }
	_, _ = svc.UsageFor(context.Background(), []string{"a"})
	if repo.calls != 2 {
		t.Fatalf("repo calls = %d, want a refresh after the TTL", repo.calls)
	}
}

func TestAgentUsageForSurfacesRepoError(t *testing.T) {
	repo := &usageRepoStub{err: errors.New("boom")}
	svc := NewAgentUsageService(repo)
	if _, err := svc.UsageFor(context.Background(), []string{"a"}); err == nil {
		t.Fatal("expected the repository error to surface")
	}
}
