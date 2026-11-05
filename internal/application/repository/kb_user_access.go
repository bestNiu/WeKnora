package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// kbUserAccessRepository stores per-(knowledge base, user) access overrides.
//
// Rows are soft-deleted: revoking a grant keeps the row (and its timestamps)
// for audit, and re-granting revives it. The production schema enforces one
// live row per (knowledge_base_id, user_id) with a partial unique index, so a
// revoke/re-grant cycle never accumulates duplicates.
type kbUserAccessRepository struct {
	db *gorm.DB
}

// NewKBUserAccessRepository returns a repository backed by GORM.
func NewKBUserAccessRepository(db *gorm.DB) interfaces.KBUserAccessRepository {
	return &kbUserAccessRepository{db: db}
}

// ListByKB returns the live grants for one knowledge base, newest first.
func (r *kbUserAccessRepository) ListByKB(
	ctx context.Context, tenantID uint64, kbID string,
) ([]*types.KBUserAccess, error) {
	if kbID == "" {
		return nil, errors.New("kb_user_access: knowledge base id is required")
	}
	var rows []*types.KBUserAccess
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID).
		Order("created_at ASC, user_id ASC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list kb user access: %w", err)
	}
	return rows, nil
}

// GetByUserAndKBs returns the caller's live grants for the given KBs.
func (r *kbUserAccessRepository) GetByUserAndKBs(
	ctx context.Context, tenantID uint64, userID string, kbIDs []string,
) (map[string]*types.KBUserAccess, error) {
	byKB := make(map[string]*types.KBUserAccess, len(kbIDs))
	if tenantID == 0 || userID == "" || len(kbIDs) == 0 {
		return byKB, nil
	}
	var rows []*types.KBUserAccess
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND user_id = ? AND knowledge_base_id IN ?", tenantID, userID, kbIDs).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("get kb user access: %w", err)
	}
	for _, row := range rows {
		if row != nil {
			byKB[row.KnowledgeBaseID] = row
		}
	}
	return byKB, nil
}

// ReplaceForKB makes the stored grant set match grants exactly.
//
// Grants are normalised first: a user listed twice is rejected, and
// can_download implies can_view because downloading without reading would be
// incoherent. Missing users are soft-deleted; specified users are created or
// revived. The whole swap runs in one transaction so a list request never
// observes a half-applied ACL.
func (r *kbUserAccessRepository) ReplaceForKB(
	ctx context.Context, tenantID uint64, kbID, grantedBy string, grants []types.KBUserGrant,
) error {
	if kbID == "" {
		return errors.New("kb_user_access: knowledge base id is required")
	}
	normalized, err := normalizeKBUserGrants(grants)
	if err != nil {
		return err
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing []*types.KBUserAccess
		if err := tx.Unscoped().
			Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID).
			Find(&existing).Error; err != nil {
			return fmt.Errorf("load kb user access: %w", err)
		}
		byUser := make(map[string]*types.KBUserAccess, len(existing))
		for _, row := range existing {
			byUser[row.UserID] = row
		}

		want := make(map[string]types.KBUserGrant, len(normalized))
		for _, grant := range normalized {
			want[grant.UserID] = grant
		}

		now := time.Now()
		for _, row := range existing {
			if _, keep := want[row.UserID]; keep || row.DeletedAt.Valid {
				continue
			}
			if err := tx.Delete(row).Error; err != nil {
				return fmt.Errorf("revoke kb user access: %w", err)
			}
		}

		for _, grant := range normalized {
			row, ok := byUser[grant.UserID]
			if !ok {
				if err := tx.Create(&types.KBUserAccess{
					TenantID:        tenantID,
					KnowledgeBaseID: kbID,
					UserID:          grant.UserID,
					CanView:         grant.CanView,
					CanDownload:     grant.CanDownload,
					GrantedBy:       grantedBy,
					CreatedAt:       now,
					UpdatedAt:       now,
				}).Error; err != nil {
					return fmt.Errorf("create kb user access: %w", err)
				}
				continue
			}
			updates := map[string]interface{}{
				"can_view":     grant.CanView,
				"can_download": grant.CanDownload,
				"granted_by":   grantedBy,
				"updated_at":   now,
				"deleted_at":   nil,
			}
			if err := tx.Unscoped().Model(row).Updates(updates).Error; err != nil {
				return fmt.Errorf("update kb user access: %w", err)
			}
		}
		return nil
	})
}

// DeleteByKB soft-deletes every grant for a knowledge base.
func (r *kbUserAccessRepository) DeleteByKB(
	ctx context.Context, tenantID uint64, kbID string,
) error {
	if kbID == "" {
		return errors.New("kb_user_access: knowledge base id is required")
	}
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID).
		Delete(&types.KBUserAccess{}).Error
	if err != nil {
		return fmt.Errorf("delete kb user access: %w", err)
	}
	return nil
}

// normalizeKBUserGrants validates and normalises a desired grant set.
func normalizeKBUserGrants(grants []types.KBUserGrant) ([]types.KBUserGrant, error) {
	normalized := make([]types.KBUserGrant, 0, len(grants))
	seen := make(map[string]struct{}, len(grants))
	for _, grant := range grants {
		userID := strings.TrimSpace(grant.UserID)
		if userID == "" {
			return nil, errors.New("kb_user_access: user id is required")
		}
		if _, dup := seen[userID]; dup {
			return nil, fmt.Errorf("kb_user_access: duplicate user %s", userID)
		}
		seen[userID] = struct{}{}
		if grant.CanDownload {
			grant.CanView = true
		}
		// A row that grants nothing is a revoke, expressed by omission: the
		// replace pass then soft-deletes the stored row instead of keeping an
		// inert "granted nothing" record.
		if !grant.CanView && !grant.CanDownload {
			continue
		}
		grant.UserID = userID
		normalized = append(normalized, grant)
	}
	return normalized, nil
}
