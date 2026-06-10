package handler

import (
	"encoding/json"
	"testing"

	acp "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"clawbench/internal/ai"
)

// ---------------------------------------------------------------------------
// convertACPSessionUpdateToMessages unit tests
// ---------------------------------------------------------------------------
//
// These tests verify the behavior of convertACPSessionUpdateToMessages in
// session_resume.go. The current implementation stores raw JSON from
// acp.SessionUpdate without parsing through mapACPSessionUpdate, which
// causes the frontend to display unparsed JSON like:
//
//	{"agent_message_chunk":{"content":{"text":{"text":"Hello!"}}}}}
//
// instead of properly rendered content blocks.
//
// When the implementation is fixed to parse through mapACPSessionUpdate,
// these tests should be updated to verify the correct behavior.

// TestConvertACPSessionUpdateToMessages_AgentMessageChunk_StoresRawJSON
// verifies that the current implementation stores raw JSON for an
// AgentMessageChunk notification. This documents the bug.
func TestConvertACPSessionUpdateToMessages_AgentMessageChunk_StoresRawJSON(t *testing.T) {
	n := acp.SessionNotification{
		SessionId: acp.SessionId("test-session-1"),
		Update: acp.SessionUpdate{
			AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
				Content: acp.ContentBlock{
					Text: &acp.ContentBlockText{Text: "Hello, world!"},
				},
			},
		},
	}

	messages := convertACPSessionUpdateToMessages(n, "session-1", "claude")
	require.Len(t, messages, 1, "should produce exactly one message")

	// Current behavior: role is "assistant" and content is raw JSON
	assert.Equal(t, "assistant", messages[0].Role)
	assert.Equal(t, "session-1", messages[0].SessionID)
	assert.Equal(t, "claude", messages[0].Backend)

	// BUG: Content is raw JSON of the entire SessionUpdate, not parsed text
	content := messages[0].Content
	assert.True(t, isRawJSON(content),
		"current implementation stores raw JSON — content: %s", truncateStr(content, 100))

	// Verify the raw JSON contains the agent_message_chunk key
	assert.Contains(t, content, "agent_message_chunk",
		"raw JSON should contain the ACP notification type key")

	// Verify the actual text is buried inside the raw JSON
	assert.Contains(t, content, "Hello, world!",
		"raw JSON should contain the original text, but it's not parsed out")

	// Show what mapACPSessionUpdate WOULD produce for comparison
	ch := make(chan ai.StreamEvent, 100)
	ai.MapACPSessionUpdateForTest(n.Update, ch)
	close(ch)

	var events []ai.StreamEvent
	for e := range ch {
		events = append(events, e)
	}

	contentEvents := filterEvents(events, "content")
	require.NotEmpty(t, contentEvents,
		"mapACPSessionUpdate should produce 'content' events from AgentMessageChunk")
	assert.Equal(t, "Hello, world!", contentEvents[0].Content,
		"mapACPSessionUpdate extracts the actual text, not raw JSON")
}

// TestConvertACPSessionUpdateToMessages_ToolCall_StoresRawJSON
// verifies that ToolCall notifications are stored as raw JSON.
func TestConvertACPSessionUpdateToMessages_ToolCall_StoresRawJSON(t *testing.T) {
	n := acp.SessionNotification{
		SessionId: acp.SessionId("test-session-2"),
		Update: acp.SessionUpdate{
			ToolCall: &acp.SessionUpdateToolCall{
				ToolCallId: acp.ToolCallId("tc-read-1"),
				Title:      "Read file contents",
				Kind:       acp.ToolKindRead,
				RawInput:   map[string]any{"file_path": "/tmp/test.go"},
			},
		},
	}

	messages := convertACPSessionUpdateToMessages(n, "session-2", "claude")
	require.Len(t, messages, 1)

	content := messages[0].Content
	assert.True(t, isRawJSON(content),
		"current implementation stores raw JSON for ToolCall — content: %s",
		truncateStr(content, 100))

	// Verify the raw JSON contains tool_call key
	assert.Contains(t, content, "tool_call",
		"raw JSON should contain the ACP notification type key")

	// Show what mapACPSessionUpdate WOULD produce
	ch := make(chan ai.StreamEvent, 100)
	ai.MapACPSessionUpdateForTest(n.Update, ch)
	close(ch)

	var events []ai.StreamEvent
	for e := range ch {
		events = append(events, e)
	}

	toolUseEvents := filterEvents(events, "tool_use")
	require.NotEmpty(t, toolUseEvents,
		"mapACPSessionUpdate should produce 'tool_use' events from ToolCall")
	assert.Equal(t, "Read", toolUseEvents[0].Tool.Name,
		"mapACPSessionUpdate extracts the canonical tool name")
	assert.Contains(t, toolUseEvents[0].Tool.Input, "file_path",
		"mapACPSessionUpdate normalizes the tool input")
}

// TestConvertACPSessionUpdateToMessages_ToolCallUpdate_StoresRawJSON
// verifies that ToolCallUpdate (completed) notifications are stored as raw JSON.
func TestConvertACPSessionUpdateToMessages_ToolCallUpdate_StoresRawJSON(t *testing.T) {
	completed := acp.ToolCallStatusCompleted
	n := acp.SessionNotification{
		SessionId: acp.SessionId("test-session-3"),
		Update: acp.SessionUpdate{
			ToolCallUpdate: &acp.SessionToolCallUpdate{
				ToolCallId: acp.ToolCallId("tc-read-1"),
				Status:     &completed,
				RawOutput:  "file contents here",
			},
		},
	}

	messages := convertACPSessionUpdateToMessages(n, "session-3", "claude")
	require.Len(t, messages, 1)

	content := messages[0].Content
	assert.True(t, isRawJSON(content),
		"current implementation stores raw JSON for ToolCallUpdate — content: %s",
		truncateStr(content, 100))

	// Show what mapACPSessionUpdate WOULD produce
	ch := make(chan ai.StreamEvent, 100)
	ai.MapACPSessionUpdateForTest(n.Update, ch)
	close(ch)

	var events []ai.StreamEvent
	for e := range ch {
		events = append(events, e)
	}

	toolResultEvents := filterEvents(events, "tool_result")
	require.NotEmpty(t, toolResultEvents,
		"mapACPSessionUpdate should produce 'tool_result' events from completed ToolCallUpdate")
	assert.Equal(t, "file contents here", toolResultEvents[0].Tool.Output,
		"mapACPSessionUpdate extracts the tool output as text, not raw JSON")
}

// TestConvertACPSessionUpdateToMessages_ThinkingChunk_StoresRawJSON
// verifies that AgentThoughtChunk notifications are stored as raw JSON.
func TestConvertACPSessionUpdateToMessages_ThinkingChunk_StoresRawJSON(t *testing.T) {
	n := acp.SessionNotification{
		SessionId: acp.SessionId("test-session-4"),
		Update: acp.SessionUpdate{
			AgentThoughtChunk: &acp.SessionUpdateAgentThoughtChunk{
				Content: acp.ContentBlock{
					Text: &acp.ContentBlockText{Text: "Let me think about this..."},
				},
			},
		},
	}

	messages := convertACPSessionUpdateToMessages(n, "session-4", "claude")
	require.Len(t, messages, 1)

	content := messages[0].Content
	assert.True(t, isRawJSON(content),
		"current implementation stores raw JSON for AgentThoughtChunk — content: %s",
		truncateStr(content, 100))

	// Show what mapACPSessionUpdate WOULD produce
	ch := make(chan ai.StreamEvent, 100)
	ai.MapACPSessionUpdateForTest(n.Update, ch)
	close(ch)

	var events []ai.StreamEvent
	for e := range ch {
		events = append(events, e)
	}

	thinkingEvents := filterEvents(events, "thinking")
	require.NotEmpty(t, thinkingEvents,
		"mapACPSessionUpdate should produce 'thinking' events from AgentThoughtChunk")
	assert.Equal(t, "Let me think about this...", thinkingEvents[0].Content,
		"mapACPSessionUpdate extracts the thinking text, not raw JSON")
}

// TestConvertACPSessionUpdateToMessages_UserMessageChunk_DetectsUserRole
// verifies that UserMessageChunk notifications correctly set role to "user".
func TestConvertACPSessionUpdateToMessages_UserMessageChunk_DetectsUserRole(t *testing.T) {
	n := acp.SessionNotification{
		SessionId: acp.SessionId("test-session-5"),
		Update: acp.SessionUpdate{
			UserMessageChunk: &acp.SessionUpdateUserMessageChunk{
				Content: acp.ContentBlock{
					Text: &acp.ContentBlockText{Text: "User says hello"},
				},
			},
		},
	}

	messages := convertACPSessionUpdateToMessages(n, "session-5", "claude")
	require.Len(t, messages, 1)

	// The role detection works correctly even in the current implementation
	assert.Equal(t, "user", messages[0].Role,
		"UserMessageChunk should set role to 'user'")
}

// TestConvertACPSessionUpdateToMessages_NonUserMessage_DefaultsToAssistant
// verifies that non-UserMessageChunk notifications default to "assistant" role.
func TestConvertACPSessionUpdateToMessages_NonUserMessage_DefaultsToAssistant(t *testing.T) {
	tests := []struct {
		name string
		n    acp.SessionNotification
	}{
		{
			name: "AgentMessageChunk",
			n: acp.SessionNotification{
				SessionId: acp.SessionId("test"),
				Update: acp.SessionUpdate{
					AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
						Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "hi"}},
					},
				},
			},
		},
		{
			name: "ToolCall",
			n: acp.SessionNotification{
				SessionId: acp.SessionId("test"),
				Update: acp.SessionUpdate{
					ToolCall: &acp.SessionUpdateToolCall{
						ToolCallId: "tc-1",
						Title:      "Bash",
						Kind:       acp.ToolKindExecute,
					},
				},
			},
		},
		{
			name: "ToolCallUpdate",
			n: acp.SessionNotification{
				SessionId: acp.SessionId("test"),
				Update: acp.SessionUpdate{
					ToolCallUpdate: &acp.SessionToolCallUpdate{
						ToolCallId: "tc-1",
					},
				},
			},
		},
		{
			name: "AgentThoughtChunk",
			n: acp.SessionNotification{
				SessionId: acp.SessionId("test"),
				Update: acp.SessionUpdate{
					AgentThoughtChunk: &acp.SessionUpdateAgentThoughtChunk{
						Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "thinking"}},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messages := convertACPSessionUpdateToMessages(tt.n, "session", "claude")
			require.Len(t, messages, 1)
			assert.Equal(t, "assistant", messages[0].Role,
				"non-UserMessageChunk notifications should default to 'assistant' role")
		})
	}
}

// TestConvertACPSessionUpdateToMessages_PersistenceFormat_WrapsRawJSONInTextBlock
// verifies that the ServeACPLoadSession handler wraps the raw JSON content
// inside a text block format for persistence to chat_history.
// This produces: {"blocks": [{"type": "text", "text": "{\"agent_message_chunk\":..."}]}
// which the frontend renders as raw JSON text instead of parsed blocks.
func TestConvertACPSessionUpdateToMessages_PersistenceFormat_WrapsRawJSONInTextBlock(t *testing.T) {
	n := acp.SessionNotification{
		SessionId: acp.SessionId("test-session"),
		Update: acp.SessionUpdate{
			AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
				Content: acp.ContentBlock{
					Text: &acp.ContentBlockText{Text: "Hello!"},
				},
			},
		},
	}

	messages := convertACPSessionUpdateToMessages(n, "session", "claude")
	require.Len(t, messages, 1)

	// Simulate what ServeACPLoadSession does when persisting the message
	contentJSON, _ := json.Marshal(map[string]any{
		"blocks": []map[string]any{{"type": "text", "text": messages[0].Content}},
	})

	// The persisted content wraps raw JSON inside a text block
	var persisted map[string]any
	err := json.Unmarshal(contentJSON, &persisted)
	require.NoError(t, err)

	blocks := persisted["blocks"].([]any)
	require.Len(t, blocks, 1)

	block := blocks[0].(map[string]any)
	assert.Equal(t, "text", block["type"])

	// BUG: The text content is raw JSON instead of parsed text
	textContent := block["text"].(string)
	assert.True(t, isRawJSON(textContent),
		"persisted text block contains raw JSON — frontend shows this as unparsed text")

	// What it SHOULD look like after the fix:
	// {"blocks": [{"type": "text", "text": "Hello!"}, {"type": "tool_use", ...}]}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// isRawJSON checks if a string starts with { or " indicating raw JSON.
func isRawJSON(s string) bool {
	s = truncateStr(s, 1)
	return s == "{" || s == `"`
}

// truncateStr truncates a string to maxLen characters.
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

// filterEvents returns StreamEvents matching the given type.
func filterEvents(events []ai.StreamEvent, eventType string) []ai.StreamEvent {
	var matched []ai.StreamEvent
	for _, e := range events {
		if e.Type == eventType {
			matched = append(matched, e)
		}
	}
	return matched
}
