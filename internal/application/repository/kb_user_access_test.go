package repository

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newKBUserAccessTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&types.KBUserAccess{}))
	// Mirror the production partial unique index: one live row per (kb, user).
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_kb_user_access_live
		ON kb_user_access (knowledge_base_id, user_id) WHERE deleted_at IS NULL`).Error)
	return db
}

func TestKBUserAccessRepositoryLifecycle(t *testing.T) {
	db := newKBUserAccessTestDB(t)
	repo := NewKBUserAccessRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.ReplaceForKB(ctx, 7, "kb-1", "admin", []types.KBUserGrant{
		{UserID: "u1", CanView: true},
		{UserID: "u2", CanDownload: true},
	}))

	rows, err := repo.ListByKB(ctx, 7, "kb-1")
	require.NoError(t, err)
	require.Len(t, rows, 2)
	byUser := map[string]*types.KBUserAccess{}
	for _, row := range rows {
		byUser[row.UserID] = row
	}
	assert.True(t, byUser["u1"].CanView)
	assert.False(t, byUser["u1"].CanDownload)
	assert.True(t, byUser["u2"].CanDownload)
	assert.True(t, byUser["u2"].CanView, "download implies view")

	grants, err := repo.GetByUserAndKBs(ctx, 7, "u2", []string{"kb-1", "kb-missing"})
	require.NoError(t, err)
	require.Len(t, grants, 1)
	assert.True(t, grants["kb-1"].CanDownload)

	// Tenant isolation: another tenant cannot read the rows.
	other, err := repo.ListByKB(ctx, 8, "kb-1")
	require.NoError(t, err)
	assert.Empty(t, other)
	noGrants, err := repo.GetByUserAndKBs(ctx, 8, "u2", []string{"kb-1"})
	require.NoError(t, err)
	assert.Empty(t, noGrants)
}

func TestKBUserAccessRepositoryReplaceRevokesAndRevives(t *testing.T) {
	db := newKBUserAccessTestDB(t)
	repo := NewKBUserAccessRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.ReplaceForKB(ctx, 7, "kb-1", "admin", []types.KBUserGrant{
		{UserID: "u1", CanView: true},
		{UserID: "u2", CanDownload: true},
	}))
	before, err := repo.ListByKB(ctx, 7, "kb-1")
	require.NoError(t, err)
	var u1ID string
	for _, row := range before {
		if row.UserID == "u1" {
			u1ID = row.ID
		}
	}
	require.NotEmpty(t, u1ID)

	// u1 stays, u2 is revoked.
	require.NoError(t, repo.ReplaceForKB(ctx, 7, "kb-1", "admin", []types.KBUserGrant{
		{UserID: "u1", CanDownload: true},
	}))
	live, err := repo.ListByKB(ctx, 7, "kb-1")
	require.NoError(t, err)
	require.Len(t, live, 1)
	assert.Equal(t, "u1", live[0].UserID)
	assert.True(t, live[0].CanView)
	assert.True(t, live[0].CanDownload)

	var revoked int64
	require.NoError(t, db.Unscoped().Model(&types.KBUserAccess{}).
		Where("knowledge_base_id = ? AND user_id = ? AND deleted_at IS NOT NULL", "kb-1", "u2").
		Count(&revoked).Error)
	assert.EqualValues(t, 1, revoked, "revoked row is soft-deleted, not removed")

	// A grant that toggles every bit off is a revoke: the row disappears
	// instead of lingering as an inert record. Re-granting u2 revives the
	// same row instead of adding a duplicate.
	require.NoError(t, repo.ReplaceForKB(ctx, 7, "kb-1", "admin", []types.KBUserGrant{
		{UserID: "u1"}, {UserID: "u2", CanView: true},
	}))
	live, err = repo.ListByKB(ctx, 7, "kb-1")
	require.NoError(t, err)
	require.Len(t, live, 1)
	assert.Equal(t, "u2", live[0].UserID)
	assert.True(t, live[0].CanView)
	assert.False(t, live[0].CanDownload)
	var total int64
	require.NoError(t, db.Unscoped().Model(&types.KBUserAccess{}).
		Where("knowledge_base_id = ?", "kb-1").Count(&total).Error)
	assert.EqualValues(t, 2, total, "no duplicate rows accumulated")
	var u1Live int64
	require.NoError(t, db.Model(&types.KBUserAccess{}).
		Where("knowledge_base_id = ? AND user_id = ?", "kb-1", "u1").Count(&u1Live).Error)
	assert.EqualValues(t, 0, u1Live, "an all-off grant revokes u1")
}

func TestKBUserAccessRepositoryRejectsDuplicateLiveRows(t *testing.T) {
	db := newKBUserAccessTestDB(t)
	ctx := context.Background()

	first := &types.KBUserAccess{TenantID: 7, KnowledgeBaseID: "kb-1", UserID: "u1", CanView: true}
	require.NoError(t, db.WithContext(ctx).Create(first).Error)
	dup := &types.KBUserAccess{TenantID: 7, KnowledgeBaseID: "kb-1", UserID: "u1", CanView: true}
	require.Error(t, db.WithContext(ctx).Create(dup).Error, "partial unique index blocks a second live row")

	// After a soft delete the same identity can be created again.
	require.NoError(t, db.WithContext(ctx).Delete(first).Error)
	require.NoError(t, db.WithContext(ctx).Create(dup).Error)
}

func TestKBUserAccessRepositoryValidation(t *testing.T) {
	db := newKBUserAccessTestDB(t)
	repo := NewKBUserAccessRepository(db)
	ctx := context.Background()

	require.Error(t, repo.ReplaceForKB(ctx, 7, "", "admin", nil))
	require.Error(t, repo.ReplaceForKB(ctx, 7, "kb-1", "admin", []types.KBUserGrant{
		{UserID: "u1"}, {UserID: "u1"},
	}))
	require.Error(t, repo.ReplaceForKB(ctx, 7, "kb-1", "admin", []types.KBUserGrant{
		{UserID: "  "},
	}))
	require.NoError(t, repo.ReplaceForKB(ctx, 7, "kb-1", "admin", []types.KBUserGrant{
		{UserID: " u1 ", CanView: true},
	}))
	rows, err := repo.ListByKB(ctx, 7, "kb-1")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "u1", rows[0].UserID)
}

func TestKBUserAccessRepositoryDeleteByKB(t *testing.T) {
	db := newKBUserAccessTestDB(t)
	repo := NewKBUserAccessRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.ReplaceForKB(ctx, 7, "kb-1", "admin", []types.KBUserGrant{{UserID: "u1", CanView: true}}))
	require.NoError(t, repo.ReplaceForKB(ctx, 7, "kb-2", "admin", []types.KBUserGrant{{UserID: "u1", CanView: true}}))
	require.NoError(t, repo.DeleteByKB(ctx, 7, "kb-1"))

	rows, err := repo.ListByKB(ctx, 7, "kb-1")
	require.NoError(t, err)
	assert.Empty(t, rows)
	other, err := repo.ListByKB(ctx, 7, "kb-2")
	require.NoError(t, err)
	assert.Len(t, other, 1)
}
