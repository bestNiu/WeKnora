package repository

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// agentUsageRepository counts agent turns in the messages table.
type agentUsageRepository struct {
	db *gorm.DB
}

// NewAgentUsageRepository builds the repository.
func NewAgentUsageRepository(db *gorm.DB) interfaces.AgentUsageRepository {
	return &agentUsageRepository{db: db}
}

// agentUsageRow is the scan target for the grouped count.
type agentUsageRow struct {
	AgentID    string `gorm:"column:agent_id"`
	Total      int64  `gorm:"column:total"`
	Last7Days  int64  `gorm:"column:last7"`
	Last30Days int64  `gorm:"column:last30"`
}

// CountByAgents groups assistant turns by agent. One query for a whole page:
// messages.agent_id is indexed, and the role filter keeps user turns out (a
// turn is attributed to the agent that answered it).
func (r *agentUsageRepository) CountByAgents(
	ctx context.Context, agentIDs []string, since7, since30 time.Time,
) (map[string]*types.AgentUsage, error) {
	usage := make(map[string]*types.AgentUsage, len(agentIDs))
	if len(agentIDs) == 0 {
		return usage, nil
	}
	var rows []agentUsageRow
	err := r.db.WithContext(ctx).
		Model(&types.Message{}).
		Select(`agent_id,
			COUNT(*) AS total,
			SUM(CASE WHEN created_at >= ? THEN 1 ELSE 0 END) AS last7,
			SUM(CASE WHEN created_at >= ? THEN 1 ELSE 0 END) AS last30`, since7, since30).
		Where("agent_id IN ? AND role = ?", agentIDs, "assistant").
		Group("agent_id").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		usage[row.AgentID] = &types.AgentUsage{
			AgentID:    row.AgentID,
			Total:      row.Total,
			Last7Days:  row.Last7Days,
			Last30Days: row.Last30Days,
		}
	}
	return usage, nil
}
