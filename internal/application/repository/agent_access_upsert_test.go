package repository

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// The member panel pages over workspace members and submits only the rows the
// operator touched. UpsertForAgent therefore has to be incremental: an absent
// user means "untouched", and only an explicit can_view=false revokes. These
// tests pin that, because a regression silently drops other people's access.

func newAgentAccessDB(t *testing.T, name string) (*gorm.DB, *agentAccessRepository) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// The partial unique index from migration 000136 is not modelled by GORM,
	// so a plain (agent, user) unique index stands in for it here.
	if err := db.AutoMigrate(&types.AgentUserAccess{}, &types.AgentAccessPolicy{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db, NewAgentAccessRepository(db).(*agentAccessRepository)
}

func liveGrants(t *testing.T, db *gorm.DB, agentID string) map[string]bool {
	t.Helper()
	var rows []*types.AgentUserAccess
	if err := db.Where("agent_id = ?", agentID).Find(&rows).Error; err != nil {
		t.Fatalf("load grants: %v", err)
	}
	grants := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row.CanView {
			grants[row.UserID] = true
		}
	}
	return grants
}

func TestUpsertForAgentLeavesUntouchedMembersAlone(t *testing.T) {
	db, repo := newAgentAccessDB(t, "agent-access-upsert")
	ctx := context.Background()
	if err := repo.UpsertForAgent(ctx, 7, "agent-1", "admin-1", []types.AgentUserGrant{
		{UserID: "alice", CanView: true},
		{UserID: "bob", CanView: true},
	}); err != nil {
		t.Fatalf("seed grants: %v", err)
	}

	// The whole point: a page-sized payload must not revoke the rest.
	if err := repo.UpsertForAgent(ctx, 7, "agent-1", "admin-1", []types.AgentUserGrant{
		{UserID: "carol", CanView: true},
	}); err != nil {
		t.Fatalf("incremental upsert: %v", err)
	}

	grants := liveGrants(t, db, "agent-1")
	for _, user := range []string{"alice", "bob", "carol"} {
		if !grants[user] {
			t.Errorf("%s must keep access after an unrelated incremental write", user)
		}
	}
}

func TestUpsertForAgentRevokesOnlyExplicitFalse(t *testing.T) {
	db, repo := newAgentAccessDB(t, "agent-access-revoke")
	ctx := context.Background()
	if err := repo.UpsertForAgent(ctx, 7, "agent-1", "admin-1", []types.AgentUserGrant{
		{UserID: "alice", CanView: true},
		{UserID: "bob", CanView: true},
	}); err != nil {
		t.Fatalf("seed grants: %v", err)
	}

	if err := repo.UpsertForAgent(ctx, 7, "agent-1", "admin-1", []types.AgentUserGrant{
		{UserID: "alice", CanView: false},
	}); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	grants := liveGrants(t, db, "agent-1")
	if grants["alice"] {
		t.Error("an explicit can_view=false must revoke the grant")
	}
	if !grants["bob"] {
		t.Error("revoking alice must not touch bob")
	}
}

// Revoke then re-grant must revive the same row in place, so the audit trail
// (who granted it, when) stays linear instead of accumulating duplicates.
func TestUpsertForAgentRevivesTheSameRow(t *testing.T) {
	db, repo := newAgentAccessDB(t, "agent-access-revive")
	ctx := context.Background()
	if err := repo.UpsertForAgent(ctx, 7, "agent-1", "admin-1", []types.AgentUserGrant{
		{UserID: "alice", CanView: true},
	}); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	var first types.AgentUserAccess
	if err := db.Unscoped().Where("agent_id = ? AND user_id = ?", "agent-1", "alice").First(&first).Error; err != nil {
		t.Fatalf("load grant: %v", err)
	}

	if err := repo.UpsertForAgent(ctx, 7, "agent-1", "admin-1", []types.AgentUserGrant{
		{UserID: "alice", CanView: false},
	}); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := repo.UpsertForAgent(ctx, 7, "agent-1", "admin-2", []types.AgentUserGrant{
		{UserID: "alice", CanView: true},
	}); err != nil {
		t.Fatalf("re-grant: %v", err)
	}

	var rows []*types.AgentUserAccess
	if err := db.Unscoped().Where("agent_id = ? AND user_id = ?", "agent-1", "alice").Find(&rows).Error; err != nil {
		t.Fatalf("load grants: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one revived row, got %d", len(rows))
	}
	if rows[0].DeletedAt.Valid {
		t.Error("re-granting must clear the soft-delete marker")
	}
	if rows[0].GrantedBy != "admin-2" {
		t.Errorf("granted_by = %q, want the latest granter", rows[0].GrantedBy)
	}
}

// Empty payloads are a no-op rather than a mass revoke.
func TestUpsertForAgentEmptyPayloadIsNoOp(t *testing.T) {
	db, repo := newAgentAccessDB(t, "agent-access-empty")
	ctx := context.Background()
	if err := repo.UpsertForAgent(ctx, 7, "agent-1", "admin-1", []types.AgentUserGrant{
		{UserID: "alice", CanView: true},
	}); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	if err := repo.UpsertForAgent(ctx, 7, "agent-1", "admin-1", nil); err != nil {
		t.Fatalf("empty upsert: %v", err)
	}
	if !liveGrants(t, db, "agent-1")["alice"] {
		t.Error("an empty payload must not revoke anything")
	}
}