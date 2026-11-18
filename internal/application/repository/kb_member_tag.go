package repository

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// kbMemberTagRepository stores operator-assigned member labels per KB.
type kbMemberTagRepository struct {
	db *gorm.DB
}

// NewKBMemberTagRepository builds the repository.
func NewKBMemberTagRepository(db *gorm.DB) interfaces.KBMemberTagRepository {
	return &kbMemberTagRepository{db: db}
}

// ListByKB returns every label row of one knowledge base.
func (r *kbMemberTagRepository) ListByKB(
	ctx context.Context, tenantID uint64, kbID string,
) ([]*types.KBMemberTag, error) {
	if kbID == "" {
		return nil, nil
	}
	var rows []*types.KBMemberTag
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID).
		Order("tag ASC, user_id ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ListSummaries returns the distinct labels plus how many members carry each.
func (r *kbMemberTagRepository) ListSummaries(
	ctx context.Context, tenantID uint64, kbID string,
) ([]types.KBMemberTagSummary, error) {
	if kbID == "" {
		return nil, nil
	}
	var rows []types.KBMemberTagSummary
	if err := r.db.WithContext(ctx).
		Model(&types.KBMemberTag{}).
		Select("tag, COUNT(*) AS count").
		Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID).
		Group("tag").
		Order("count DESC, tag ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ReplaceForMember sets a member's label set on one KB. It rewrites the set
// wholesale (delete-then-insert, two statements) rather than diffing per row:
// a member carries at most KBMemberTagMaxPerMember labels, so the diff would
// only add queries, not save any.
func (r *kbMemberTagRepository) ReplaceForMember(
	ctx context.Context, tenantID uint64, kbID, userID string, tags []string,
) error {
	if kbID == "" || userID == "" {
		return errors.New("kb_member_tags: knowledge base and user are required")
	}
	desired := normalizeTags(tags)
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("tenant_id = ? AND knowledge_base_id = ? AND user_id = ?",
			tenantID, kbID, userID).Delete(&types.KBMemberTag{}).Error; err != nil {
			return err
		}
		if len(desired) == 0 {
			return nil
		}
		now := time.Now()
		rows := make([]*types.KBMemberTag, 0, len(desired))
		for _, tag := range desired {
			rows = append(rows, &types.KBMemberTag{
				ID: uuid.NewString(), TenantID: tenantID, KnowledgeBaseID: kbID,
				UserID: userID, Tag: tag, CreatedAt: now, UpdatedAt: now,
			})
		}
		return tx.Create(&rows).Error
	})
}

// normalizeTags trims, de-duplicates and caps a label set. It is shared by the
// write path and the request validator so both agree on what "the same label"
// means.
func normalizeTags(tags []string) []string {
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, raw := range tags {
		tag := strings.TrimSpace(raw)
		if tag == "" {
			continue
		}
		runes := []rune(tag)
		if len(runes) > types.KBMemberTagMaxLength {
			tag = strings.TrimSpace(string(runes[:types.KBMemberTagMaxLength]))
		}
		key := strings.ToLower(tag)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, tag)
		if len(out) >= types.KBMemberTagMaxPerMember {
			break
		}
	}
	sort.Strings(out)
	return out
}