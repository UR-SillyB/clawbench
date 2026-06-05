package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	acp "github.com/coder/acp-go-sdk"

	"clawbench/internal/model"
)

// mapACPSessionUpdate converts an ACP SessionUpdate to StreamEvent(s) and
// sends them to the stream channel. Called from ClawBenchACPClient.SessionUpdate,
// which runs on the SDK's internal goroutine.
// If conn is non-nil, mode/config/thinking cache updates are applied to the connection
// so that re-emitted SSE events reflect the latest state.
func mapACPSessionUpdate(update acp.SessionUpdate, ch chan<- StreamEvent, ctx context.Context, conn *ACPConn) { //nolint:gocognit,gocyclo,revive,unparam // ACP protocol has many event types, each branch is simple; ctx position follows ACP SDK convention; ctx reserved for future use
	// Emit raw_output event for each ACP notification so the handler can
	// persist the original protocol data to ai_raw_responses for debugging.
	// This mirrors how CLIBackend collects raw stdout lines.
	if rawJSON, err := json.Marshal(update); err == nil {
		forwardACPEvent(ch, StreamEvent{Type: "raw_output", RawOutput: string(rawJSON)})
	}

	switch {
	case update.AgentMessageChunk != nil:
		// When the agent transitions from thinking to content output, emit
		// thinking_done so the frontend can stop the thinking spinner immediately.
		forwardACPEvent(ch, StreamEvent{Type: "thinking_done"})
		content := update.AgentMessageChunk.Content
		if content.Text != nil {
			forwardACPEvent(ch, StreamEvent{Type: "content", Content: content.Text.Text})
		}

	case update.AgentThoughtChunk != nil:
		content := update.AgentThoughtChunk.Content
		if content.Text != nil {
			forwardACPEvent(ch, StreamEvent{Type: "thinking", Content: content.Text.Text})
		}

	case update.ToolCall != nil:
		// When the agent transitions from thinking to tool use, emit
		// thinking_done so the frontend can stop the thinking spinner.
		forwardACPEvent(ch, StreamEvent{Type: "thinking_done"})
		tc := update.ToolCall
		event := mapACPToolCall(*tc)
		forwardACPEvent(ch, event)

	case update.ToolCallUpdate != nil:
		tcu := update.ToolCallUpdate
		event := mapACPToolCallUpdate(*tcu)
		forwardACPEvent(ch, event)

		// When a think tool completes, also emit thinking_done so the frontend
		// can stop the thinking spinner immediately — without this, the spinner
		// stays until the entire AI response finishes because thinking blocks
		// have no per-block "done" signal.
		if tcu.Kind != nil && *tcu.Kind == acp.ToolKindThink && tcu.Status != nil {
			switch *tcu.Status {
			case acp.ToolCallStatusCompleted, acp.ToolCallStatusFailed:
				forwardACPEvent(ch, StreamEvent{Type: "thinking_done"})
			}
		}

	case update.Plan != nil:
		entries := make([]PlanEntry, 0, len(update.Plan.Entries))
		for _, e := range update.Plan.Entries {
			entries = append(entries, PlanEntry{
				Content:  e.Content,
				Priority: string(e.Priority),
				Status:   string(e.Status),
			})
		}
		forwardACPEvent(ch, StreamEvent{Type: "plan_update", Plan: &PlanState{Entries: entries}})

	case update.AvailableCommandsUpdate != nil:
		cmds := update.AvailableCommandsUpdate.AvailableCommands
		slog.Info("acp: available commands update", "count", len(cmds))
		infos := make([]AvailableCommandInfo, 0, len(cmds))
		for _, c := range cmds {
			info := AvailableCommandInfo{
				Name:        c.Name,
				Description: c.Description,
			}
			if c.Input != nil && c.Input.Unstructured != nil {
				info.InputHint = c.Input.Unstructured.Hint
			}
			infos = append(infos, info)
		}
		forwardACPEvent(ch, StreamEvent{
			Type:     "commands_update",
			Commands: infos,
		})

	case update.CurrentModeUpdate != nil:
		// v1 mode update: only currentModeId; available modes were sent in session/new
		mu := update.CurrentModeUpdate
		modeState := &ModeState{
			CurrentModeID: string(mu.CurrentModeId),
		}
		forwardACPEvent(ch, StreamEvent{Type: "mode_update", Mode: modeState})
		// Update cached mode state so re-emitted SSE events reflect the new mode
		if conn != nil {
			conn.UpdateCachedCurrentMode(string(mu.CurrentModeId))
		}

	case update.ConfigOptionUpdate != nil:
		// v2 config option update: extract mode and thought_level options
		cu := update.ConfigOptionUpdate
		for _, opt := range cu.ConfigOptions {
			if opt.Select == nil {
				continue
			}
			sel := opt.Select
			if sel.Category == nil {
				continue
			}

			switch *sel.Category {
			case acp.SessionConfigOptionCategoryMode:
				configState := buildConfigOptionStateFromSelect(sel, "mode")
				forwardACPEvent(ch, StreamEvent{Type: "config_update", Config: configState})
				// Update cached mode state so re-emitted SSE events reflect the new mode
				if conn != nil {
					conn.UpdateCachedCurrentMode(string(sel.CurrentValue))
				}

			case acp.SessionConfigOptionCategoryThoughtLevel:
				effortState := buildThinkingEffortStateFromSelect(sel)
				if effortState != nil {
					forwardACPEvent(ch, StreamEvent{Type: "thinking_effort_update", ThinkingEffort: effortState})
					// Update cached thinking effort so re-emitted SSE events reflect the new level
					if conn != nil {
						conn.UpdateCachedCurrentThinkingEffort(string(sel.CurrentValue))
					}
				}

			case acp.SessionConfigOptionCategoryModel:
				modelList := buildModelListStateFromSelect(sel)
				if modelList != nil {
					forwardACPEvent(ch, StreamEvent{Type: "model_list_update", ModelList: modelList})
					if conn != nil {
						conn.SetCachedModelListState(modelList)
					}
				}
			}
		}

	case update.SessionInfoUpdate != nil:
		slog.Debug("acp: session info update")
	}
}

// mapACPToolCall converts an ACP ToolCall start event to a StreamEvent.
func mapACPToolCall(tc acp.SessionUpdateToolCall) StreamEvent {
	tool := &ToolCall{
		Name: extractToolName(tc.Title, tc.Kind),
		ID:   string(tc.ToolCallId),
		Done: false,
	}

	// Extract raw input as JSON string, normalizing camelCase → snake_case
	if tc.RawInput != nil {
		if inputBytes, err := json.Marshal(tc.RawInput); err == nil {
			normalized, normErr := normalizeToolInput(inputBytes, map[string]string{
				"oldString": "old_string",
				"newString": "new_string",
				"dirPath":   "path",
				"cellIndex": "cell_index",
				"cellType":  "cell_type",
			})
			if normErr == nil {
				tool.Input = string(normalized)
			} else {
				tool.Input = string(inputBytes)
			}
		}
	} else if len(tc.Content) > 0 {
		// Some ACP agents (e.g., Claude) use Content blocks instead of RawInput
		// for tool calls like Terminal/Bash. Extract input from Content fields.
		input := extractInputFromContent(tc)
		if input != nil {
			if inputBytes, err := json.Marshal(input); err == nil {
				tool.Input = string(inputBytes)
			}
		}
	}

	// Fallback: for execute-kind tools with no input from RawInput or Content,
	// use the title as the command. Gemini CLI sends only title (e.g. "echo hello")
	// with no rawInput or content at all.
	if tool.Input == "" && tc.Kind == acp.ToolKindExecute && tc.Title != "" {
		input := map[string]any{"command": tc.Title}
		if inputBytes, err := json.Marshal(input); err == nil {
			tool.Input = string(inputBytes)
		}
	}

	return StreamEvent{Type: "tool_use", Tool: tool}
}

// extractInputFromContent extracts tool input parameters from ACP Content blocks.
// Terminal content blocks contain the command being executed; text content blocks
// may contain the description or command text.
func extractInputFromContent(tc acp.SessionUpdateToolCall) map[string]any {
	input := make(map[string]any)
	for _, c := range tc.Content {
		if c.Terminal != nil {
			// Terminal content — the command text is typically in the title
			// For Terminal/Bash tools, use the tool call title as the command
			if tc.Title != "" {
				input["command"] = tc.Title
			}
			return input
		}
		if c.Content != nil {
			// Text content block — extract text as description
			cb := c.Content.Content
			if cb.Text != nil && cb.Text.Text != "" {
				input["description"] = cb.Text.Text
			}
		}
	}
	if len(input) == 0 {
		return nil
	}
	return input
}

// extractInputFromContentUpdate extracts tool input from Content in tool_call_update events.
// Same logic as extractInputFromContent but works with SessionToolCallUpdate (Title is *string).
func extractInputFromContentUpdate(tcu acp.SessionToolCallUpdate) map[string]any {
	input := make(map[string]any)
	for _, c := range tcu.Content {
		if c.Terminal != nil {
			// Terminal content — use title as command
			if tcu.Title != nil && *tcu.Title != "" {
				input["command"] = *tcu.Title
			}
			return input
		}
		if c.Content != nil {
			cb := c.Content.Content
			if cb.Text != nil && cb.Text.Text != "" {
				input["description"] = cb.Text.Text
			}
		}
	}
	if len(input) == 0 {
		return nil
	}
	return input
}

// mapACPToolCallUpdate converts an ACP ToolCallUpdate to a StreamEvent.
func mapACPToolCallUpdate(tcu acp.SessionToolCallUpdate) StreamEvent {
	tool := &ToolCall{
		ID: string(tcu.ToolCallId),
	}

	// Map status
	if tcu.Status != nil {
		switch *tcu.Status {
		case acp.ToolCallStatusCompleted:
			tool.Done = true
			tool.Status = "success"
		case acp.ToolCallStatusFailed:
			tool.Done = true
			tool.Status = "error"
		case acp.ToolCallStatusPending, acp.ToolCallStatusInProgress:
			tool.Done = false
		}
	}

	// Extract input from RawInput or Content.
	// Claude ACP agent sends command info in tool_call_update (not in the initial tool_call),
	// e.g. raw_input={"command":"ls /tmp","description":"List /tmp contents"}.
	// We update the tool input so the frontend can display the command.
	//
	// IMPORTANT: Only extract input from updates that carry RawInput (with actual values)
	// or Title. Skip updates that only have Content text — these carry output text
	// (e.g. command results), not input. Extracting description from output text
	// would overwrite the command from a prior RawInput update.
	if tcu.RawInput != nil {
		if inputBytes, err := json.Marshal(tcu.RawInput); err == nil && string(inputBytes) != "{}" {
			normalized, normErr := normalizeToolInput(inputBytes, map[string]string{
				"oldString": "old_string",
				"newString": "new_string",
				"dirPath":   "path",
				"cellIndex": "cell_index",
				"cellType":  "cell_type",
			})
			if normErr == nil {
				tool.Input = string(normalized)
			} else {
				tool.Input = string(inputBytes)
			}
		}
	} else if tcu.Kind != nil && *tcu.Kind == acp.ToolKindExecute {
		// For execute-kind tools without RawInput, try title as command (Gemini CLI)
		// or Content terminal blocks. Do NOT extract text Content as description —
		// that carries output, not input.
		if tcu.Title != nil && *tcu.Title != "" {
			input := map[string]any{"command": *tcu.Title}
			if inputBytes, err := json.Marshal(input); err == nil {
				tool.Input = string(inputBytes)
			}
		} else {
			// Check for terminal content blocks (rare, but some agents may use them)
			for _, c := range tcu.Content {
				if c.Terminal != nil {
					input := make(map[string]any)
					if tcu.Title != nil && *tcu.Title != "" {
						input["command"] = *tcu.Title
					}
					if inputBytes, err := json.Marshal(input); err == nil && len(input) > 0 {
						tool.Input = string(inputBytes)
					}
					break
				}
			}
		}
	}

	// Also update the title/name if provided (Claude sends title in updates)
	if tcu.Title != nil && *tcu.Title != "" {
		kind := acp.ToolKindExecute // default kind for title-based name extraction
		if tcu.Kind != nil {
			kind = *tcu.Kind
		}
		tool.Name = extractToolName(*tcu.Title, kind)
	}

	// Extract human-readable output from RawOutput.
	// ACP agents return structured output (map[string]any), but the frontend
	// expects plain text like CLI mode produces. We extract the text content
	// from known keys and fall back to pretty-printed JSON.
	if tcu.RawOutput != nil {
		tool.Output = truncateToolOutput(extractACPToolOutput(tcu.RawOutput))
	}

	// Determine event type: if tool is done, emit tool_result; otherwise update tool_use
	eventType := "tool_use"
	if tool.Done {
		eventType = "tool_result"
	}

	slog.Debug("acp: tool_call_update", "tool_call_id", tool.ID, "done", tool.Done, "event_type", eventType, "has_output", tool.Output != "",
		"status", fmt.Sprintf("%v", tcu.Status), "content_count", len(tcu.Content), "title", tcu.Title,
		"raw_input", fmt.Sprintf("%v", tcu.RawInput))

	return StreamEvent{Type: eventType, Tool: tool}
}

// extractACPToolOutput converts ACP RawOutput (any) to a human-readable string.
// ACP agents return structured output (e.g. map[string]any{"result": "file contents"}),
// but the frontend expects plain text like CLI mode produces. This function extracts
// the text content from known keys and falls back to pretty-printed JSON.
func extractACPToolOutput(rawOutput any) string {
	// Direct string — already human-readable
	if s, ok := rawOutput.(string); ok {
		return s
	}

	// Boolean or number — convert directly
	switch v := rawOutput.(type) {
	case bool:
		return fmt.Sprintf("%v", v)
	case float64, float32, int, int64, int32:
		return fmt.Sprintf("%v", v)
	}

	// Map — try known content keys to extract text
	if m, ok := rawOutput.(map[string]any); ok {
		return extractMapOutput(m)
	}

	// Array — join string elements or pretty-print
	if arr, ok := rawOutput.([]any); ok {
		return extractArrayOutput(arr)
	}

	// Fallback: pretty-print as JSON
	if bytes, err := json.MarshalIndent(rawOutput, "", "  "); err == nil {
		return string(bytes)
	}
	return fmt.Sprintf("%v", rawOutput)
}

// acpOutputKeyPriority defines the order of keys to try when extracting text
// from a map[string]any tool output. Earlier keys take priority.
var acpOutputKeyPriority = []string{
	"result",  // Most common: {"result": "file contents"}
	"output",  // {"output": "command output"}
	"content", // {"content": "file content"}
	"text",    // {"text": "plain text"}
	"message", // {"message": "success"}
	"stdout",  // Bash-like: {"stdout": "...", "stderr": "..."}
}

// extractMapOutput extracts human-readable text from a map output.
func extractMapOutput(m map[string]any) string { //nolint:gocognit,gocyclo // many output format branches, each is trivial
	// Try known content keys in priority order
	for _, key := range acpOutputKeyPriority {
		if val, ok := m[key]; ok && val != nil {
			switch v := val.(type) {
			case string:
				if v != "" {
					// For Bash-like stdout, also append stderr if present
					if key == "stdout" {
						if stderr, ok2 := m["stderr"]; ok2 {
							if s, ok3 := stderr.(string); ok3 && s != "" {
								return v + "\n" + s
							}
						}
					}
					return v
				}
			case map[string]any, []any:
				// Nested structure — pretty-print it
				if bytes, err := json.MarshalIndent(v, "", "  "); err == nil {
					return string(bytes)
				}
			default:
				if fmt.Sprintf("%v", v) != "" {
					return fmt.Sprintf("%v", v)
				}
			}
		}
	}

	// Try "error" key for failed tools
	if errVal, ok := m["error"]; ok && errVal != nil {
		switch v := errVal.(type) {
		case string:
			return v
		case map[string]any:
			if msg, ok2 := v["message"]; ok2 {
				return fmt.Sprintf("%v", msg)
			}
		}
		return fmt.Sprintf("%v", errVal)
	}

	// No known key — pretty-print entire object
	if bytes, err := json.MarshalIndent(m, "", "  "); err == nil {
		return string(bytes)
	}
	return fmt.Sprintf("%v", m)
}

// extractArrayOutput extracts human-readable text from an array output.
func extractArrayOutput(arr []any) string {
	// If all elements are strings, join them
	allStrings := true
	var parts []string
	for _, elem := range arr {
		if s, ok := elem.(string); ok {
			parts = append(parts, s)
		} else {
			allStrings = false
			break
		}
	}
	if allStrings && len(parts) > 0 {
		return strings.Join(parts, "\n")
	}

	// Fallback: pretty-print as JSON
	if bytes, err := json.MarshalIndent(arr, "", "  "); err == nil {
		return string(bytes)
	}
	return fmt.Sprintf("%v", arr)
}

// and input formatting. We try prefix matching first, then kind-to-canonical,
// then fall back to the title itself.
func extractToolName(title string, kind acp.ToolKind) string {
	if title != "" {
		// Fast path: case-insensitive alias lookup for single-word titles.
		// Some ACP agents send lowercase names (e.g. "bash", "terminal")
		// while the frontend expects PascalCase ("Bash").
		if !strings.Contains(title, " ") {
			if canonical, ok := acpLowerAlias[strings.ToLower(title)]; ok {
				return canonical
			}
		}
		// Try matching title against known canonical tool name prefixes.
		// Longer/more-specific prefixes must appear before shorter ones
		// (e.g. "MultiEdit" before "Edit", "WebSearch" before "Web").
		for _, p := range acpToolNamePatterns {
			if strings.HasPrefix(title, p.prefix) {
				return p.canonical
			}
		}
		// If title is a single word (no spaces), use it directly — it may already be canonical
		if !strings.Contains(title, " ") {
			return title
		}
	}
	// Map ACP ToolKind to canonical PascalCase names expected by the frontend.
	// Without this, string(kind) returns lowercase ("read", "execute", "search")
	// which won't match TOOL_ICONS in the frontend.
	if canonical, ok := acpKindToCanonical[kind]; ok {
		return canonical
	}
	return string(kind)
}

// acpLowerAlias maps lowercase single-word tool titles to canonical PascalCase names.
// Used for case-insensitive matching of titles like "bash" → "Bash", "terminal" → "Bash".
var acpLowerAlias = map[string]string{
	"bash":      "Bash",
	"terminal":  "Bash",
	"shell":     "Bash",
	"read":      "Read",
	"write":     "Write",
	"edit":      "Edit",
	"glob":      "Glob",
	"grep":      "Grep",
	"ls":        "LS",
	"list":      "LS",
	"agent":     "Agent",
	"skill":     "Skill",
}

// acpToolNamePatterns maps ACP tool title prefixes to canonical tool names.
// ACP agents send titles like "Read file contents", "Edit file", "Run command"
// but the frontend expects "Read", "Edit", "Bash" for icon/summary matching.
// Longer/more-specific prefixes MUST appear before shorter ones to avoid
// incorrect prefix matches (e.g. "WebSearch" before "Web", "MultiEdit" before "Edit").
var acpToolNamePatterns = []struct{ prefix, canonical string }{
	// Multi-word / compound tools first
	{"NotebookEdit", "NotebookEdit"},
	{"MultiEdit", "MultiEdit"},
	{"TodoWrite", "TodoWrite"},
	{"TodoRead", "TodoRead"},
	{"WebSearch", "WebSearch"},
	{"WebFetch", "WebFetch"},
	{"AskUserQuestion", "AskUserQuestion"},
	{"EnterPlanMode", "EnterPlanMode"},
	{"ExitPlanMode", "ExitPlanMode"},
	{"EnterWorktree", "EnterWorktree"},
	{"LeaveWorktree", "LeaveWorktree"},
	{"SendMessage", "SendMessage"},
	{"TaskCreate", "TaskCreate"},
	{"TaskUpdate", "TaskUpdate"},
	{"TaskList", "TaskList"},
	{"TaskGet", "TaskGet"},
	{"TaskStop", "TaskStop"},
	{"TaskOutput", "TaskOutput"},
	{"TaskCreate", "TaskCreate"},
	{"TaskUpdate", "TaskUpdate"},
	{"TaskList", "TaskList"},
	{"TaskGet", "TaskGet"},
	{"Task", "Agent"}, // ACP generic "Task" tool → Agent (sub-agent delegation)
	{"ComputerUse", "ComputerUse"},
	{"TeamCreate", "TeamCreate"},
	{"TeamDelete", "TeamDelete"},
	{"StructuredOutput", "StructuredOutput"},
	{"SkillManage", "SkillManage"},
	{"DeepThink", "DeepThink"},
	{"ImageGen", "ImageGen"},
	{"PermissionApproval", "PermissionApproval"},
	{"WeChatReply", "WeChatReply"},
	{"WeComReply", "WeComReply"},
	{"save_memory", "save_memory"},
	// Single-word tools — must come after compound prefixes above
	{"Read", "Read"},
	{"Write", "Write"},
	{"Edit", "Edit"},
	{"Bash", "Bash"},
	{"Terminal", "Bash"},
	{"Glob", "Glob"},
	{"Grep", "Grep"},
	{"LS", "LS"},
	{"List", "LS"},
	{"Agent", "Agent"},
	{"Skill", "Skill"},
	{"LSP", "LSP"},
	{"Monitor", "Monitor"},
	{"PowerShell", "PowerShell"},
	{"Git", "Git"},
}

// acpKindToCanonical maps ACP ToolKind enum values to the PascalCase
// canonical names expected by the frontend TOOL_ICONS mapping.
var acpKindToCanonical = map[acp.ToolKind]string{
	acp.ToolKindRead:       "Read",
	acp.ToolKindEdit:       "Edit",
	acp.ToolKindDelete:     "Edit", // delete operations → Edit category
	acp.ToolKindMove:       "Edit", // move/rename → Edit category
	acp.ToolKindSearch:     "Grep", // search → Grep category
	acp.ToolKindExecute:    "Bash", // execute/run → Bash category
	acp.ToolKindThink:      "DeepThink",
	acp.ToolKindFetch:      "WebFetch",
	acp.ToolKindSwitchMode: "EnterPlanMode",
	acp.ToolKindOther:      "Skill", // uncategorized tools → Skill category
}

// mapACPError maps a JSON-RPC error code to a StreamEvent.
func mapACPError(code int, message string) StreamEvent {
	reason := ReasonBackendExit
	switch code {
	case -32700:
		reason = ReasonParseError
	case -32600, -32602:
		reason = ReasonParseError // invalid request/params
	case -32601:
		reason = ReasonBackendExit // method not found
	case -32603:
		reason = ReasonBackendExit // internal error
	case -32000:
		reason = ReasonRequestFailed // auth required
	case -32800:
		reason = ReasonContextCancel // request cancelled
	}
	return StreamEvent{
		Type:   "error",
		Error:  fmt.Sprintf("ACP error %d: %s", code, message),
		Reason: reason,
	}
}

// buildConfigOptionStateFromSelect builds a ConfigOptionState from an ACP SessionConfigOptionSelect.
func buildConfigOptionStateFromSelect(sel *acp.SessionConfigOptionSelect, category string) *ConfigOptionState {
	configState := &ConfigOptionState{
		ConfigID:  string(sel.Id),
		CurrentID: string(sel.CurrentValue),
	}

	optDef := ConfigOptionDef{
		ID:       string(sel.Id),
		Name:     sel.Name,
		Category: category,
	}

	mapACPSelectOptions(sel.Options, &optDef)
	configState.Options = append(configState.Options, optDef)
	return configState
}

// buildThinkingEffortStateFromSelect builds a ThinkingEffortState from an ACP thought_level config option.
func buildThinkingEffortStateFromSelect(sel *acp.SessionConfigOptionSelect) *ThinkingEffortState {
	state := &ThinkingEffortState{
		CurrentID: string(sel.CurrentValue),
	}

	if sel.Options.Ungrouped != nil {
		for _, v := range *sel.Options.Ungrouped {
			state.AvailableLevels = append(state.AvailableLevels, ThinkingEffortDef{
				ID:   string(v.Value),
				Name: v.Name,
			})
		}
	}
	if sel.Options.Grouped != nil {
		for _, g := range *sel.Options.Grouped {
			for _, v := range g.Options {
				state.AvailableLevels = append(state.AvailableLevels, ThinkingEffortDef{
					ID:   string(v.Value),
					Name: v.Name,
				})
			}
		}
	}

	if len(state.AvailableLevels) == 0 && state.CurrentID == "" {
		return nil
	}

	return state
}

// mapACPConfigOptionUpdate converts an ACP SessionConfigOptionUpdate to a ConfigOptionState.
// Returns nil if the update doesn't contain mode-relevant information.
//
// Deprecated: Use the per-category extraction in mapACPSessionUpdate instead.
//
//nolint:unused // kept as reference for future config option mapping
func mapACPConfigOptionUpdate(cu *acp.SessionConfigOptionUpdate) *ConfigOptionState {
	if cu == nil || len(cu.ConfigOptions) == 0 {
		return nil
	}

	// Look for mode-relevant config options
	for _, opt := range cu.ConfigOptions {
		// SessionConfigOption is a union: .Select or .Boolean
		if opt.Select == nil {
			continue
		}
		sel := opt.Select

		// Check if this is a mode-related config option
		if sel.Category != nil && *sel.Category == acp.SessionConfigOptionCategoryMode {
			configState := &ConfigOptionState{
				ConfigID:  string(sel.Id),
				CurrentID: string(sel.CurrentValue),
			}

			optDef := ConfigOptionDef{
				ID:       string(sel.Id),
				Name:     sel.Name,
				Category: "mode",
			}

			// Extract option values from SessionConfigSelectOptions
			mapACPSelectOptions(sel.Options, &optDef)

			configState.Options = append(configState.Options, optDef)
			return configState
		}
	}

	return nil
}

// mapACPSelectOptions extracts ConfigOptionValue entries from ACP SessionConfigSelectOptions.
func mapACPSelectOptions(opts acp.SessionConfigSelectOptions, optDef *ConfigOptionDef) {
	if opts.Ungrouped != nil {
		for _, v := range *opts.Ungrouped {
			optDef.Values = append(optDef.Values, ConfigOptionValue{
				ID:   string(v.Value),
				Name: v.Name,
			})
		}
	}
	if opts.Grouped != nil {
		for _, g := range *opts.Grouped {
			for _, v := range g.Options {
				optDef.Values = append(optDef.Values, ConfigOptionValue{
					ID:   string(v.Value),
					Name: v.Name,
				})
			}
		}
	}
}

// extractACPModeState extracts ModeState from an ACP NewSessionResponse.
// Returns nil if no modes are available.
func extractACPModeState(sessResp *acp.NewSessionResponse) *ModeState {
	if sessResp == nil {
		return nil
	}
	return extractModeStateFromModes(sessResp.Modes)
}

// extractACPModeStateFromLoad extracts ModeState from a LoadSessionResponse.
func extractACPModeStateFromLoad(loadResp *acp.LoadSessionResponse) *ModeState {
	if loadResp == nil {
		return nil
	}
	return extractModeStateFromModes(loadResp.Modes)
}

func extractModeStateFromModes(modes *acp.SessionModeState) *ModeState {
	if modes == nil {
		return nil
	}

	modeState := &ModeState{
		CurrentModeID: string(modes.CurrentModeId),
	}

	for _, m := range modes.AvailableModes {
		modeState.AvailableModes = append(modeState.AvailableModes, ModeDef{
			ID:   string(m.Id),
			Name: m.Name,
		})
	}

	if len(modeState.AvailableModes) == 0 && modeState.CurrentModeID == "" {
		return nil
	}

	return modeState
}

// extractACPConfigOptions extracts mode-relevant ConfigOptionState from an ACP NewSessionResponse.
// Returns nil if no mode-relevant config options are available.
func extractACPConfigOptions(sessResp *acp.NewSessionResponse) *ConfigOptionState {
	if sessResp == nil {
		return nil
	}
	return extractConfigOptionsFromOpts(sessResp.ConfigOptions)
}

// extractACPConfigOptionsFromLoad extracts mode-relevant ConfigOptionState from a LoadSessionResponse.
func extractACPConfigOptionsFromLoad(loadResp *acp.LoadSessionResponse) *ConfigOptionState {
	if loadResp == nil {
		return nil
	}
	return extractConfigOptionsFromOpts(loadResp.ConfigOptions)
}

func extractConfigOptionsFromOpts(opts []acp.SessionConfigOption) *ConfigOptionState {
	if len(opts) == 0 {
		return nil
	}

	for _, opt := range opts {
		if opt.Select == nil {
			continue
		}
		sel := opt.Select

		if sel.Category != nil && *sel.Category == acp.SessionConfigOptionCategoryMode {
			configState := &ConfigOptionState{
				ConfigID:  string(sel.Id),
				CurrentID: string(sel.CurrentValue),
			}

			optDef := ConfigOptionDef{
				ID:       string(sel.Id),
				Name:     sel.Name,
				Category: "mode",
			}

			mapACPSelectOptions(sel.Options, &optDef)

			configState.Options = append(configState.Options, optDef)
			return configState
		}
	}

	return nil
}

// extractACPThinkingEffort extracts ThinkingEffortState from an ACP NewSessionResponse.
// Looks for config options with Category "thought_level". Returns nil if none found.
func extractACPThinkingEffort(sessResp *acp.NewSessionResponse) *ThinkingEffortState {
	if sessResp == nil {
		return nil
	}
	return extractThinkingEffortFromOpts(sessResp.ConfigOptions)
}

// extractACPThinkingEffortFromLoad extracts ThinkingEffortState from a LoadSessionResponse.
func extractACPThinkingEffortFromLoad(loadResp *acp.LoadSessionResponse) *ThinkingEffortState {
	if loadResp == nil {
		return nil
	}
	return extractThinkingEffortFromOpts(loadResp.ConfigOptions)
}

func extractThinkingEffortFromOpts(opts []acp.SessionConfigOption) *ThinkingEffortState {
	if len(opts) == 0 {
		return nil
	}

	for _, opt := range opts {
		if opt.Select == nil {
			continue
		}
		sel := opt.Select

		if sel.Category != nil && *sel.Category == acp.SessionConfigOptionCategoryThoughtLevel {
			return buildThinkingEffortStateFromSelect(sel)
		}
	}

	return nil
}

// buildModelListStateFromSelect builds a ModelListState from an ACP SessionConfigOptionSelect
// with Category "model". Returns nil if no models are available.
func buildModelListStateFromSelect(sel *acp.SessionConfigOptionSelect) *ModelListState {
	state := &ModelListState{
		CurrentModelID: string(sel.CurrentValue),
	}

	if sel.Options.Ungrouped != nil {
		for _, v := range *sel.Options.Ungrouped {
			state.Models = append(state.Models, model.AgentModel{
				ID:   string(v.Value),
				Name: v.Name,
			})
		}
	}
	if sel.Options.Grouped != nil {
		for _, g := range *sel.Options.Grouped {
			for _, v := range g.Options {
				state.Models = append(state.Models, model.AgentModel{
					ID:   string(v.Value),
					Name: v.Name,
				})
			}
		}
	}

	if len(state.Models) == 0 && state.CurrentModelID == "" {
		return nil
	}

	return state
}

// extractACPModelList extracts ModelListState from an ACP NewSessionResponse.
// Looks for config options with Category "model". Returns nil if none found.
func extractACPModelList(sessResp *acp.NewSessionResponse) *ModelListState {
	if sessResp == nil {
		return nil
	}
	return extractModelListFromOpts(sessResp.ConfigOptions)
}

// extractACPModelListFromLoad extracts ModelListState from a LoadSessionResponse.
func extractACPModelListFromLoad(loadResp *acp.LoadSessionResponse) *ModelListState {
	if loadResp == nil {
		return nil
	}
	return extractModelListFromOpts(loadResp.ConfigOptions)
}

func extractModelListFromOpts(opts []acp.SessionConfigOption) *ModelListState {
	if len(opts) == 0 {
		return nil
	}

	for _, opt := range opts {
		if opt.Select == nil {
			continue
		}
		sel := opt.Select

		if sel.Category != nil && *sel.Category == acp.SessionConfigOptionCategoryModel {
			return buildModelListStateFromSelect(sel)
		}
	}

	return nil
}

// forwardACPEvent sends a StreamEvent to the channel with non-blocking send.
// Used by ACP event mapping to avoid blocking the SDK's internal goroutine.
func forwardACPEvent(ch chan<- StreamEvent, event StreamEvent) {
	select {
	case ch <- event:
	default:
		// Channel full, drop event (same as CLIBackend pattern)
		slog.Warn("acp: stream channel full, dropping event", "type", event.Type)
	}
}
