package types

// AgentUsage is one agent's usage counters.
//
// The counters come from the messages table, not from a separate counter
// table: every assistant turn records the agent it was produced by
// (messages.agent_id, indexed), so "how many times was this agent used" is a
// grouped count over rows that already exist.
//
// Scope is platform-wide rather than per-workspace: messages carry no tenant
// column, and a shared agent's turns are recorded against the agent ID
// regardless of which workspace borrowed it.
type AgentUsage struct {
	AgentID string `json:"agent_id"`
	// Total counts every assistant turn the agent ever produced.
	Total int64 `json:"total"`
	// Last7Days / Last30Days are the same count inside a rolling window.
	Last7Days  int64 `json:"last_7_days"`
	Last30Days int64 `json:"last_30_days"`
}
