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

func TestPreviewArtifactRepositoryLifecycle(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&types.KnowledgePreviewArtifact{}))
	ctx := context.Background()
	repo := NewPreviewArtifactRepository(db)

	none, err := repo.Get(ctx, 7, "k1")
	require.NoError(t, err)
	assert.Nil(t, none)

	require.NoError(t, repo.Upsert(ctx, &types.KnowledgePreviewArtifact{
		KnowledgeID: "k1", TenantID: 7, Status: types.PreviewArtifactPending,
	}))
	pending, err := repo.Get(ctx, 7, "k1")
	require.NoError(t, err)
	require.NotNil(t, pending)
	assert.Equal(t, types.PreviewArtifactPending, pending.Status)

	require.NoError(t, repo.Upsert(ctx, &types.KnowledgePreviewArtifact{
		KnowledgeID: "k1", TenantID: 7, Status: types.PreviewArtifactReady,
		StoragePath: "local://7/k1.pdf", PageCount: 12,
	}))
	ready, err := repo.Get(ctx, 7, "k1")
	require.NoError(t, err)
	require.True(t, ready.IsReady())
	assert.Equal(t, 12, ready.PageCount)

	// Tenant isolation: another workspace never sees the row.
	other, err := repo.Get(ctx, 8, "k1")
	require.NoError(t, err)
	assert.Nil(t, other)

	require.NoError(t, repo.Delete(ctx, 7, "k1"))
	gone, err := repo.Get(ctx, 7, "k1")
	require.NoError(t, err)
	assert.Nil(t, gone)
}
