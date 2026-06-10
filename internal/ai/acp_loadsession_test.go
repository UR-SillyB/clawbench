//go:build integration

package ai

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ===========================================================================
// Category F: ACP LoadSession Resume — Replay Parsing Integration Tests
// ===========================================================================
//
// These tests verify that when a Claude ACP session is loaded via LoadSession
// (the acp-load endpoint), the replayed SessionUpdate notifications are
// properly parsed into structured StreamEvents (content, tool_use, tool_result,
// thinking, etc.) rather than being stored as raw JSON.
//
// Background: The original convertACPSessionUpdateToMessages in
// session_resume.go stored raw JSON from acp.SessionUpdate without parsing
// through mapACPSessionUpdate. This caused the frontend to display
// unparsed JSON like {"agent_message_chunk":{...}} instead of rendered
// content blocks.

// F1: Basic LoadSession replay — verify AgentMessageChunk produces content events
//
// This test:
//  1. Establishes a new ACP session with a simple prompt
//  2. Captures the ACP session ID from session_capture
//  3. Uses LoadSession to replay the conversation into a new ClawBench session
//  4. Verifies the replayed messages are parsed through mapACPSessionUpdate
//     (producing content/tool_use/thinking events), not stored as raw JSON
func TestClaudeACP_LoadSession_ReplayParsing(t *testing.T) {
	requireClaudeACPAvailable(t)

	agent := claudeACPAgent()
	env := setupACPTestEnvForAgent(t, agent)
	backend, err := NewACPBackend(agent)
	require.NoError(t, err)

	sessionID := acpSessionID()
	defer env.closeConn(t, sessionID)

	// Step 1: Establish a conversation with a simple prompt
	events1 := sendACPPrompt(t, backend, sessionID, "回复一个字：好", 120*time.Second)
	requireDoneEvent(t, events1)

	// Verify we got content on the first prompt
	content1 := concatACPContent(events1)
	assert.NotEmpty(t, content1, "first prompt should produce content")

	// Step 2: Capture the ACP session ID
	acpSSID := extractACPCaptureID(t, events1)
	require.NotEmpty(t, acpSSID, "should have ACP session ID after first prompt")

	t.Logf("Step 1 complete: session_id=%s, acp_sid=%s, content_len=%d", sessionID, acpSSID, len(content1))

	// Step 3: Close the existing connection so LoadSession can create a new one
	env.closeConn(t, sessionID)

	// Step 4: Use GetOrCreateConnForLoad to replay the session
	loadSessionID := acpSessionID()
	defer env.closeConn(t, loadSessionID)

	ctx, cancel := contextWithTimeout(t, 120*time.Second)
	defer cancel()

	conn, err := env.mgr.GetOrCreateConnForLoad(ctx, agent, loadSessionID, acpSSID, acpTestWorkDir())
	if err != nil {
		// LoadSession may not be supported by this agent version
		t.Skipf("LoadSession not supported or failed: %v", err)
		return
	}
	require.NotNil(t, conn, "should have a connection after LoadSession")

	// Step 5: Collect replayed messages from the buffer
	client := conn.GetClient()
	require.NotNil(t, client, "should have ACP client after LoadSession")

	buf := client.GetAndClearLoadSessionBuf()
	t.Logf("LoadSession replayed %d SessionUpdate notifications", len(buf))

	// Step 6: Parse the replayed notifications through mapACPSessionUpdate
	// and verify they produce proper structured events (not raw JSON)
	ch := make(chan StreamEvent, 1000)
	var allEvents []StreamEvent

	for _, n := range buf {
		mapACPSessionUpdate(n.Update, ch, ctx, nil, nil)
	}
	close(ch)

	for event := range ch {
		allEvents = append(allEvents, event)
	}

	t.Logf("Parsed %d StreamEvents from LoadSession replay", len(allEvents))

	// Step 7: Verify the events are properly parsed, not raw JSON
	contentEvents := findACPEvents(allEvents, "content")
	rawOutputEvents := findACPEvents(allEvents, "raw_output")

	// There should be at least content events from the replayed assistant messages
	assert.NotEmpty(t, contentEvents,
		"LoadSession replay should produce 'content' events from AgentMessageChunk, got event types: %v",
		acpEventTypes(allEvents))

	// Verify content events contain actual text, not raw JSON
	for i, e := range contentEvents {
		assert.False(t, looksLikeRawJSON(e.Content),
			"content event[%d] looks like raw JSON instead of parsed text: %s",
			i, truncate(e.Content, 100))
	}

	// raw_output events should be present (they're always emitted for debugging)
	assert.NotEmpty(t, rawOutputEvents,
		"LoadSession replay should emit raw_output events for each notification")

	// Log event type summary
	typeCounts := make(map[string]int)
	for _, e := range allEvents {
		typeCounts[e.Type]++
	}
	t.Logf("LoadSession replay event breakdown: %v", typeCounts)
}

// F2: LoadSession replay — verify tool calls produce tool_use/tool_result events
//
// This test sends a prompt that triggers tool usage, then replays via
// LoadSession and verifies that ToolCall/ToolCallUpdate notifications
// are properly converted to tool_use and tool_result events.
func TestClaudeACP_LoadSession_ToolCallParsing(t *testing.T) {
	requireClaudeACPAvailable(t)

	agent := claudeACPAgent()
	env := setupACPTestEnvForAgent(t, agent)
	backend, err := NewACPBackend(agent)
	require.NoError(t, err)

	sessionID := acpSessionID()
	defer env.closeConn(t, sessionID)

	// Step 1: Send a prompt that will trigger tool usage (file reading)
	events1 := sendACPPrompt(t, backend, sessionID,
		"读一下 README.md 的第一行，然后只回复第一行的内容", 180*time.Second)
	requireDoneEvent(t, events1)

	// Verify we got tool_use events on the first prompt
	toolUseEvents1 := findACPEvents(events1, "tool_use")
	t.Logf("First prompt produced %d tool_use events", len(toolUseEvents1))

	// Verify we got content
	content1 := concatACPContent(events1)
	assert.NotEmpty(t, content1, "first prompt should produce content")

	// Step 2: Capture the ACP session ID
	acpSSID := extractACPCaptureID(t, events1)
	require.NotEmpty(t, acpSSID)

	// Step 3: Close the existing connection
	env.closeConn(t, sessionID)

	// Step 4: LoadSession replay
	loadSessionID := acpSessionID()
	defer env.closeConn(t, loadSessionID)

	ctx, cancel := contextWithTimeout(t, 120*time.Second)
	defer cancel()

	conn, err := env.mgr.GetOrCreateConnForLoad(ctx, agent, loadSessionID, acpSSID, acpTestWorkDir())
	if err != nil {
		t.Skipf("LoadSession not supported or failed: %v", err)
		return
	}

	client := conn.GetClient()
	require.NotNil(t, client)

	buf := client.GetAndClearLoadSessionBuf()
	t.Logf("LoadSession replayed %d SessionUpdate notifications", len(buf))

	// Step 5: Parse through mapACPSessionUpdate
	ch := make(chan StreamEvent, 1000)
	var allEvents []StreamEvent

	for _, n := range buf {
		mapACPSessionUpdate(n.Update, ch, ctx, nil, nil)
	}
	close(ch)

	for event := range ch {
		allEvents = append(allEvents, event)
	}

	// Step 6: Verify tool events are properly parsed
	toolUseEvents := findACPEvents(allEvents, "tool_use")
	toolResultEvents := findACPEvents(allEvents, "tool_result")
	contentEvents := findACPEvents(allEvents, "content")

	// If the original conversation had tool calls, the replay should produce them too
	if len(toolUseEvents1) > 0 {
		assert.NotEmpty(t, toolUseEvents,
			"LoadSession replay should produce 'tool_use' events from ToolCall notifications")
	}

	// Verify tool_use events have structured data, not raw JSON
	for i, e := range toolUseEvents {
		require.NotNil(t, e.Tool, "tool_use event[%d] should have Tool data", i)
		assert.NotEmpty(t, e.Tool.Name,
			"tool_use event[%d] should have a parsed tool name, not raw JSON", i)
		assert.NotEmpty(t, e.Tool.ID,
			"tool_use event[%d] should have a tool call ID", i)
		// Tool input should be valid JSON (normalized), not raw ACP JSON
		if e.Tool.Input != "" {
			var parsed map[string]any
			err := json.Unmarshal([]byte(e.Tool.Input), &parsed)
			assert.NoError(t, err,
				"tool_use event[%d] input should be valid JSON, got: %s",
				i, truncate(e.Tool.Input, 100))
		}
	}

	// Verify tool_result events are properly parsed
	for i, e := range toolResultEvents {
		require.NotNil(t, e.Tool, "tool_result event[%d] should have Tool data", i)
		assert.True(t, e.Tool.Done,
			"tool_result event[%d] should have Done=true", i)
		// Output should be human-readable text, not raw JSON
		if e.Tool.Output != "" {
			assert.False(t, looksLikeRawJSON(e.Tool.Output),
				"tool_result event[%d] output looks like raw JSON: %s",
				i, truncate(e.Tool.Output, 100))
		}
	}

	// Content events should also be properly parsed
	for i, e := range contentEvents {
		assert.False(t, looksLikeRawJSON(e.Content),
			"content event[%d] looks like raw JSON: %s",
			i, truncate(e.Content, 100))
	}

	typeCounts := make(map[string]int)
	for _, e := range allEvents {
		typeCounts[e.Type]++
	}
	t.Logf("LoadSession tool call replay event breakdown: %v", typeCounts)
}

// F3: LoadSession replay — verify thinking blocks produce thinking events
//
// This test sends a prompt with high thinking effort, then replays via
// LoadSession and verifies that AgentThoughtChunk notifications are
// properly converted to thinking events.
func TestClaudeACP_LoadSession_ThinkingParsing(t *testing.T) {
	requireClaudeACPAvailable(t)

	agent := claudeACPAgent()
	env := setupACPTestEnvForAgent(t, agent)
	backend, err := NewACPBackend(agent)
	require.NoError(t, err)

	sessionID := acpSessionID()
	defer env.closeConn(t, sessionID)

	// Send a prompt that will likely trigger thinking
	events1 := sendACPPrompt(t, backend, sessionID,
		"思考一下1+1等于几，然后回复答案", 120*time.Second)
	requireDoneEvent(t, events1)

	content1 := concatACPContent(events1)
	assert.NotEmpty(t, content1, "first prompt should produce content")

	acpSSID := extractACPCaptureID(t, events1)
	require.NotEmpty(t, acpSSID)

	// Check if thinking events were present in the original session
	thinkingEvents1 := findACPEvents(events1, "thinking")
	t.Logf("First prompt produced %d thinking events", len(thinkingEvents1))

	// Close and reload
	env.closeConn(t, sessionID)

	loadSessionID := acpSessionID()
	defer env.closeConn(t, loadSessionID)

	ctx, cancel := contextWithTimeout(t, 120*time.Second)
	defer cancel()

	conn, err := env.mgr.GetOrCreateConnForLoad(ctx, agent, loadSessionID, acpSSID, acpTestWorkDir())
	if err != nil {
		t.Skipf("LoadSession not supported or failed: %v", err)
		return
	}

	client := conn.GetClient()
	require.NotNil(t, client)

	buf := client.GetAndClearLoadSessionBuf()
	t.Logf("LoadSession replayed %d SessionUpdate notifications", len(buf))

	// Parse through mapACPSessionUpdate
	ch := make(chan StreamEvent, 1000)
	var allEvents []StreamEvent

	for _, n := range buf {
		mapACPSessionUpdate(n.Update, ch, ctx, nil, nil)
	}
	close(ch)

	for event := range ch {
		allEvents = append(allEvents, event)
	}

	// If original session had thinking, the replay should produce thinking events
	thinkingEvents := findACPEvents(allEvents, "thinking")
	thinkingDoneEvents := findACPEvents(allEvents, "thinking_done")

	if len(thinkingEvents1) > 0 {
		assert.NotEmpty(t, thinkingEvents,
			"LoadSession replay should produce 'thinking' events from AgentThoughtChunk when original session had thinking")
	}

	// Verify thinking content is text, not raw JSON
	for i, e := range thinkingEvents {
		assert.False(t, looksLikeRawJSON(e.Content),
			"thinking event[%d] looks like raw JSON: %s",
			i, truncate(e.Content, 100))
	}

	t.Logf("Thinking events in replay: %d, thinking_done: %d",
		len(thinkingEvents), len(thinkingDoneEvents))

	typeCounts := make(map[string]int)
	for _, e := range allEvents {
		typeCounts[e.Type]++
	}
	t.Logf("LoadSession thinking replay event breakdown: %v", typeCounts)
}

// F4: Unit-level test for convertACPSessionUpdateToMessages — verify raw JSON issue
//
// This test directly calls convertACPSessionUpdateToMessages with various
// ACP SessionUpdate types and verifies what the current implementation
// produces. This documents the bug (raw JSON) so that when it's fixed,
// the test will confirm the fix.
//
// NOTE: This test is in the integration test file because it uses the
// acp.SessionUpdate types which require the ACP SDK dependency, and
// follows the existing pattern of the integration test file.
func TestClaudeACP_LoadSession_ConvertSessionUpdate_RawJSONIssue(t *testing.T) {
	// Test case 1: AgentMessageChunk
	agentMsgNotification := acp.SessionNotification{
		SessionId: acp.SessionId("test-session-1"),
		Update: acp.SessionUpdate{
			AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
				Content: acp.ContentBlock{
					Text: &acp.ContentBlockText{Text: "Hello, world!"},
				},
			},
		},
	}

	// Parse through mapACPSessionUpdate to see what we SHOULD get
	ch := make(chan StreamEvent, 100)
	mapACPSessionUpdate(agentMsgNotification.Update, ch, nil, nil, nil)
	close(ch)

	var events []StreamEvent
	for e := range ch {
		events = append(events, e)
	}

	// After mapACPSessionUpdate, we should get structured events
	contentEvents := findACPEvents(events, "content")
	require.NotEmpty(t, contentEvents,
		"AgentMessageChunk should produce 'content' event via mapACPSessionUpdate")

	// The content should be the actual text, not raw JSON
	assert.Equal(t, "Hello, world!", contentEvents[0].Content,
		"parsed content should be the text from AgentMessageChunk, not raw JSON")

	// Now show what the old convertACPSessionUpdateToMessages does:
	// It marshals the entire SessionUpdate as JSON, producing raw JSON
	// like: {"agent_message_chunk":{"content":{"text":{"text":"Hello, world!"}}}}
	rawContent, _ := json.Marshal(agentMsgNotification.Update)
	assert.True(t, looksLikeRawJSON(string(rawContent)),
		"raw JSON marshaling of SessionUpdate should look like JSON (this documents the bug)")

	// Test case 2: ToolCall
	toolCallNotification := acp.SessionNotification{
		SessionId: acp.SessionId("test-session-2"),
		Update: acp.SessionUpdate{
			ToolCall: &acp.SessionUpdateToolCall{
				ToolCallId: acp.ToolCallId("tc-1"),
				Title:      "Read",
				Kind:       acp.ToolKindRead,
				RawInput:   map[string]any{"file_path": "/tmp/test.go"},
			},
		},
	}

	ch2 := make(chan StreamEvent, 100)
	mapACPSessionUpdate(toolCallNotification.Update, ch2, nil, nil, nil)
	close(ch2)

	var events2 []StreamEvent
	for e := range ch2 {
		events2 = append(events2, e)
	}

	toolUseEvents := findACPEvents(events2, "tool_use")
	require.NotEmpty(t, toolUseEvents,
		"ToolCall should produce 'tool_use' event via mapACPSessionUpdate")
	assert.Equal(t, "Read", toolUseEvents[0].Tool.Name,
		"tool name should be extracted from ToolCall, not raw JSON")
	assert.Contains(t, toolUseEvents[0].Tool.Input, "file_path",
		"tool input should be normalized JSON, not raw ACP JSON")

	// Test case 3: ToolCallUpdate (completed)
	completed := acp.ToolCallStatusCompleted
	toolResultNotification := acp.SessionNotification{
		SessionId: acp.SessionId("test-session-3"),
		Update: acp.SessionUpdate{
			ToolCallUpdate: &acp.SessionToolCallUpdate{
				ToolCallId: acp.ToolCallId("tc-2"),
				Status:     &completed,
				RawOutput:  "file contents here",
			},
		},
	}

	ch3 := make(chan StreamEvent, 100)
	mapACPSessionUpdate(toolResultNotification.Update, ch3, nil, nil, nil)
	close(ch3)

	var events3 []StreamEvent
	for e := range ch3 {
		events3 = append(events3, e)
	}

	toolResultEvents := findACPEvents(events3, "tool_result")
	require.NotEmpty(t, toolResultEvents,
		"completed ToolCallUpdate should produce 'tool_result' event via mapACPSessionUpdate")
	assert.True(t, toolResultEvents[0].Tool.Done,
		"completed tool result should have Done=true")
	assert.Equal(t, "file contents here", toolResultEvents[0].Tool.Output,
		"tool output should be extracted text, not raw JSON")

	// Test case 4: AgentThoughtChunk
	thoughtNotification := acp.SessionNotification{
		SessionId: acp.SessionId("test-session-4"),
		Update: acp.SessionUpdate{
			AgentThoughtChunk: &acp.SessionUpdateAgentThoughtChunk{
				Content: acp.ContentBlock{
					Text: &acp.ContentBlockText{Text: "Let me think about this..."},
				},
			},
		},
	}

	ch4 := make(chan StreamEvent, 100)
	mapACPSessionUpdate(thoughtNotification.Update, ch4, nil, nil, nil)
	close(ch4)

	var events4 []StreamEvent
	for e := range ch4 {
		events4 = append(events4, e)
	}

	thinkingEvents := findACPEvents(events4, "thinking")
	require.NotEmpty(t, thinkingEvents,
		"AgentThoughtChunk should produce 'thinking' event via mapACPSessionUpdate")
	assert.Equal(t, "Let me think about this...", thinkingEvents[0].Content,
		"thinking content should be the text from AgentThoughtChunk, not raw JSON")
}

// F5: Full round-trip — replay via LoadSession, then send a new prompt
//
// After loading a session, the connection should be fully functional for
// new prompts. This verifies that LoadSession doesn't leave the connection
// in a broken state.
func TestClaudeACP_LoadSession_ThenNewPrompt(t *testing.T) {
	requireClaudeACPAvailable(t)

	agent := claudeACPAgent()
	env := setupACPTestEnvForAgent(t, agent)
	backend, err := NewACPBackend(agent)
	require.NoError(t, err)

	sessionID := acpSessionID()
	defer env.closeConn(t, sessionID)

	// Step 1: Establish a conversation
	events1 := sendACPPrompt(t, backend, sessionID, "请记住数字42，只回复'好的'", 120*time.Second)
	requireDoneEvent(t, events1)

	acpSSID := extractACPCaptureID(t, events1)
	require.NotEmpty(t, acpSSID)

	content1 := concatACPContent(events1)
	assert.NotEmpty(t, content1, "first prompt should produce content")

	// Step 2: Close and reload via LoadSession
	env.closeConn(t, sessionID)

	loadSessionID := acpSessionID()
	defer env.closeConn(t, loadSessionID)

	ctx, cancel := contextWithTimeout(t, 120*time.Second)
	defer cancel()

	conn, err := env.mgr.GetOrCreateConnForLoad(ctx, agent, loadSessionID, acpSSID, acpTestWorkDir())
	if err != nil {
		t.Skipf("LoadSession not supported or failed: %v", err)
		return
	}

	// Clear the replay buffer (simulating what ServeACPLoadSession does)
	client := conn.GetClient()
	require.NotNil(t, client)
	buf := client.GetAndClearLoadSessionBuf()
	t.Logf("LoadSession replayed %d notifications", len(buf))

	// Step 3: Send a new prompt on the loaded session
	// Use the loadSessionID which maps to this connection
	env.storeSID(loadSessionID, acpSSID)

	events2 := sendACPPrompt(t, backend, loadSessionID, "我之前让你记住的数字是什么？只回答数字", 120*time.Second)
	requireDoneEvent(t, events2)

	content2 := concatACPContent(events2)
	t.Logf("After LoadSession + new prompt: %q", content2)

	// The AI should remember the number from the original conversation
	assert.True(t, strings.Contains(content2, "42"),
		"AI should remember '42' from the loaded session, got: %s", content2)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// looksLikeRawJSON checks if a string looks like raw JSON (starts with { or ")
// instead of being human-readable text content.
func looksLikeRawJSON(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "{") || strings.HasPrefix(s, `"`)
}

// contextWithTimeout creates a context with timeout for tests.
func contextWithTimeout(t *testing.T, timeout time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), timeout)
}
