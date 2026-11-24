package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// timedOutWaitTool stands in for wait_agent: it always reports a timeout, so
// the round learned nothing.
type timedOutWaitTool struct {
	agenttools.BaseTool
}

func newTimedOutWaitTool() *timedOutWaitTool {
	return &timedOutWaitTool{BaseTool: agenttools.NewBaseTool(
		agenttools.ToolWaitAgent, "test", json.RawMessage(`{"type":"object"}`))}
}

func (t *timedOutWaitTool) Execute(context.Context, json.RawMessage) (*types.ToolResult, error) {
	return &types.ToolResult{
		Success: true,
		Output:  `{"updated":false,"timed_out":true,"interrupted":false}`,
	}, nil
}

// waitRoundCountedTool is a wait_agent that delivered an event: it must consume
// a round like any other observation.
type waitRoundCountedTool struct {
	agenttools.BaseTool
}

func newWaitRoundCountedTool() *waitRoundCountedTool {
	return &waitRoundCountedTool{BaseTool: agenttools.NewBaseTool(
		agenttools.ToolWaitAgent, "test", json.RawMessage(`{"type":"object"}`))}
}

func (t *waitRoundCountedTool) Execute(context.Context, json.RawMessage) (*types.ToolResult, error) {
	return &types.ToolResult{
		Success: true,
		Output:  `{"updated":true,"timed_out":false,"interrupted":false}`,
	}, nil
}

func waitCallResponse(id string) mockResponse {
	return mockResponse{chunks: []types.StreamResponse{{
		ResponseType: types.ResponseTypeAnswer,
		Done:         true,
		FinishReason: "tool_calls",
		ToolCalls: []types.LLMToolCall{{
			ID: id, Type: "function",
			Function: types.FunctionCall{Name: agenttools.ToolWaitAgent, Arguments: `{}`},
		}},
	}}}
}

// TestWaitRoundsDoNotConsumeIterationBudget is the regression guard for
// "parent iteration budget exhausted; remaining delegated work was cancelled".
// A parent that delegates spends whole rounds inside timed-out wait_agent
// calls; counting those against MaxIterations starved it of the rounds it
// needed to read the children's results.
func TestWaitRoundsDoNotConsumeIterationBudget(t *testing.T) {
	model := &mockChat{responses: []mockResponse{
		waitCallResponse("w1"),
		waitCallResponse("w2"),
		waitCallResponse("w3"),
		answerResponse("answer from child results"),
	}}
	engine := newTestEngine(t, model)
	engine.config.MaxIterations = 2
	registry := agenttools.NewToolRegistry()
	registry.RegisterTool(newTimedOutWaitTool())
	engine.toolRegistry = registry

	state := &types.AgentState{}
	_, err := engine.executeLoop(t.Context(), state, "task", emptyMessages(), emptyTools(), "s", "m")
	require.NoError(t, err)
	require.Equal(t, "answer from child results", state.FinalAnswer)
	require.Equal(t, 4, model.callCount,
		"three timed-out waits must not use up a two-round budget")
}

// TestWaitRoundsAreCapped stops a parent from polling forever: uncounted waits
// are bounded, after which waiting costs ordinary rounds again and the turn
// ends through the normal max-iterations fallback rather than looping.
func TestWaitRoundsAreCapped(t *testing.T) {
	// maxWaitRounds uncounted waits, then MaxIterations budgeted waits consume
	// the model responses; the max-iterations fallback then asks for a
	// synthesized answer, so the final response must carry visible text.
	responses := make([]mockResponse, 0, maxWaitRounds+3)
	for i := range maxWaitRounds + 2 {
		responses = append(responses, waitCallResponse(fmt.Sprintf("w%d", i)))
	}
	responses = append(responses, answerResponse("fallback"))
	model := &mockChat{responses: responses}

	engine := newTestEngine(t, model)
	engine.config.MaxIterations = 2
	registry := agenttools.NewToolRegistry()
	registry.RegisterTool(newTimedOutWaitTool())
	engine.toolRegistry = registry

	state := &types.AgentState{}
	_, err := engine.executeLoop(t.Context(), state, "task", emptyMessages(), emptyTools(), "s", "m")
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	// maxWaitRounds uncounted waits, then two budgeted rounds, then the
	// synthesis call the max-iterations fallback makes.
	require.Equal(t, maxWaitRounds+3, model.callCount)
}

// TestWaitRoundWithEventConsumesBudget keeps the exemption narrow: a wait that
// delivered something is a real observation and counts like any other round.
func TestWaitRoundWithEventConsumesBudget(t *testing.T) {
	model := &mockChat{responses: []mockResponse{
		waitCallResponse("w1"),
		waitCallResponse("w2"),
		answerResponse("fallback"),
	}}
	engine := newTestEngine(t, model)
	engine.config.MaxIterations = 2
	registry := agenttools.NewToolRegistry()
	registry.RegisterTool(newWaitRoundCountedTool())
	engine.toolRegistry = registry

	state := &types.AgentState{}
	_, err := engine.executeLoop(t.Context(), state, "task", emptyMessages(), emptyTools(), "s", "m")
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.Equal(t, 3, model.callCount, "two delivering waits exhaust a two-round budget")
}

// TestWaitedWithoutResultIgnoresOtherTools documents the guard's shape: only a
// round whose every call was a timed-out wait is exempt.
func TestWaitedWithoutResultIgnoresOtherTools(t *testing.T) {
	wait := types.ToolCall{
		Name:   agenttools.ToolWaitAgent,
		Result: &types.ToolResult{Success: true, Output: `{"timed_out":true}`},
	}
	other := types.ToolCall{
		Name:   agenttools.ToolSearchKnowledge,
		Result: &types.ToolResult{Success: true, Output: `[]`},
	}
	failed := types.ToolCall{
		Name:   agenttools.ToolWaitAgent,
		Result: &types.ToolResult{Success: false, Output: `{"timed_out":true}`},
	}
	require.True(t, waitedWithoutResult(types.AgentStep{ToolCalls: []types.ToolCall{wait}}))
	require.False(t, waitedWithoutResult(types.AgentStep{}))
	require.False(t, waitedWithoutResult(types.AgentStep{ToolCalls: []types.ToolCall{wait, other}}))
	require.False(t, waitedWithoutResult(types.AgentStep{ToolCalls: []types.ToolCall{failed}}))
}
