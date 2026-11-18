package service

import (
	"context"
	"errors"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// kbMemberTagService manages operator-assigned member labels on a KB.
type kbMemberTagService struct {
	repo interfaces.KBMemberTagRepository
}

// NewKBMemberTagService builds the service.
func NewKBMemberTagService(repo interfaces.KBMemberTagRepository) interfaces.KBMemberTagService {
	return &kbMemberTagService{repo: repo}
}

// ListSummaries returns the distinct labels with member counts.
func (s *kbMemberTagService) ListSummaries(
	ctx context.Context, tenantID uint64, kbID string,
) ([]types.KBMemberTagSummary, error) {
	if s == nil || s.repo == nil || tenantID == 0 || kbID == "" {
		return nil, nil
	}
	return s.repo.ListSummaries(ctx, tenantID, kbID)
}

// TagsByUser groups labels by member for the requested page of members.
func (s *kbMemberTagService) TagsByUser(
	ctx context.Context, tenantID uint64, kbID string, userIDs []string,
) (map[string][]string, error) {
	out := make(map[string][]string, len(userIDs))
	if s == nil || s.repo == nil || len(userIDs) == 0 {
		return out, nil
	}
	rows, err := s.repo.ListByKB(ctx, tenantID, kbID)
	if err != nil {
		return nil, err
	}
	page := make(map[string]struct{}, len(userIDs))
	for _, id := range userIDs {
		page[id] = struct{}{}
	}
	for _, row := range rows {
		if row == nil {
			continue
		}
		if _, ok := page[row.UserID]; !ok {
			continue
		}
		out[row.UserID] = append(out[row.UserID], row.Tag)
	}
	return out, nil
}

// SetMemberTags replaces one member's labels.
func (s *kbMemberTagService) SetMemberTags(
	ctx context.Context, tenantID uint64, kbID, userID string, tags []string,
) error {
	if s == nil || s.repo == nil {
		return errors.New("kb member tag service is unavailable")
	}
	if tenantID == 0 || strings.TrimSpace(kbID) == "" || strings.TrimSpace(userID) == "" {
		return errors.New("kb member tags: tenant, knowledge base and user are required")
	}
	return s.repo.ReplaceForMember(ctx, tenantID, kbID, userID, tags)
}

// MembersWithTag returns the members carrying a label.
func (s *kbMemberTagService) MembersWithTag(
	ctx context.Context, tenantID uint64, kbID, tag string,
) ([]string, error) {
	tag = strings.TrimSpace(tag)
	if s == nil || s.repo == nil || tag == "" {
		return nil, nil
	}
	rows, err := s.repo.ListByKB(ctx, tenantID, kbID)
	if err != nil {
		return nil, err
	}
	lowered := strings.ToLower(tag)
	out := make([]string, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if row == nil || strings.ToLower(row.Tag) != lowered {
			continue
		}
		if _, dup := seen[row.UserID]; dup {
			continue
		}
		seen[row.UserID] = struct{}{}
		out = append(out, row.UserID)
	}
	return out, nil
}
