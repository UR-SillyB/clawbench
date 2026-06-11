package ai

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"clawbench/internal/model"
)

// ---------------------------------------------------------------------------
// decodeExitCode
// ---------------------------------------------------------------------------

func TestRefactor_DecodeExitCode(t *testing.T) {
	tests := []struct {
		code int
		want string
	}{
		{0, ""},
		{1, "general error"},
		{2, ""},
		{126, "permission denied / not executable"},
		{127, "command not found"},
		{128, "invalid exit argument"},
		{129, "SIGHUP"},
		{130, "SIGINT (Ctrl+C)"},
		{137, "SIGKILL (possible OOM killer)"},
		{139, "SIGSEGV (segmentation fault)"},
		{141, "SIGPIPE (broken pipe)"},
		{143, "SIGTERM"},
		{131, "signal 3"}, // 131 - 128 = 3 (SIGQUIT on most Unix)
		{200, "signal 72"},
		{125, ""}, // < 128, not a known code
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("code_%d", tc.code), func(t *testing.T) {
			got := decodeExitCode(tc.code)
			assert.Equal(t, tc.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// crashDiagnostics.String()
// ---------------------------------------------------------------------------

func TestRefactor_CrashDiagnostics_String(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		d := crashDiagnostics{}
		assert.Equal(t, "", d.String())
	})

	t.Run("exit_code_only", func(t *testing.T) {
		d := crashDiagnostics{ExitCode: 1}
		s := d.String()
		assert.Contains(t, s, "exit_code=1")
		assert.Contains(t, s, "general error")
	})

	t.Run("exit_code_with_signal", func(t *testing.T) {
		d := crashDiagnostics{ExitCode: 137, Signal: "SIGKILL"}
		s := d.String()
		assert.Contains(t, s, "exit_code=137")
		assert.Contains(t, s, "SIGKILL")
		// Signal takes precedence over decodeExitCode
		assert.NotContains(t, s, "possible OOM killer")
	})

	t.Run("exit_code_unknown_signal", func(t *testing.T) {
		d := crashDiagnostics{ExitCode: 150}
		s := d.String()
		assert.Contains(t, s, "exit_code=150")
		assert.Contains(t, s, "signal 22") // 150 - 128
	})

	t.Run("all_fields", func(t *testing.T) {
		d := crashDiagnostics{
			ExitCode:   139,
			Uptime:     5 * time.Minute,
			ParentPID:  1234,
			VMRSSKB:    2048,
			FDCount:    42,
			StderrTail: "FATAL ERROR",
		}
		s := d.String()
		assert.Contains(t, s, "exit_code=139")
		assert.Contains(t, s, "SIGSEGV")
		assert.Contains(t, s, "uptime=5m0s")
		assert.Contains(t, s, "ppid=1234")
		assert.Contains(t, s, "rss=2MB")
		assert.Contains(t, s, "fds=42")
		assert.Contains(t, s, "stderr: FATAL ERROR")
	})

	t.Run("zero_exit_code_omits_exit", func(t *testing.T) {
		d := crashDiagnostics{Uptime: 10 * time.Second, StderrTail: "some error"}
		s := d.String()
		assert.NotContains(t, s, "exit_code")
		assert.Contains(t, s, "uptime=10s")
		assert.Contains(t, s, "stderr: some error")
	})
}

// ---------------------------------------------------------------------------
// isPeerDisconnectMsg
// ---------------------------------------------------------------------------

func TestRefactor_IsPeerDisconnectMsg(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		{"peer disconnected", true},
		{"error: peer disconnected during prompt", true},
		{"write tcp: broken pipe", true},
		{"connection reset by peer", false},
		{"timeout waiting for response", false},
		{"", false},
	}
	for _, tc := range tests {
		t.Run(tc.msg, func(t *testing.T) {
			assert.Equal(t, tc.want, isPeerDisconnectMsg(tc.msg))
		})
	}
}

// ---------------------------------------------------------------------------
// isACPPeerDisconnected
// ---------------------------------------------------------------------------

func TestRefactor_IsACPPeerDisconnected(t *testing.T) {
	t.Run("plain_error_with_disconnect_msg", func(t *testing.T) {
		err := fmt.Errorf("peer disconnected during write")
		assert.True(t, isACPPeerDisconnected(err))
	})

	t.Run("plain_error_with_broken_pipe", func(t *testing.T) {
		err := fmt.Errorf("write tcp 127.0.0.1:1234->127.0.0.1:5678: broken pipe")
		assert.True(t, isACPPeerDisconnected(err))
	})

	t.Run("plain_error_unrelated", func(t *testing.T) {
		err := fmt.Errorf("some other error")
		assert.False(t, isACPPeerDisconnected(err))
	})

	t.Run("request_error_code_minus32603_with_disconnect_in_data", func(t *testing.T) {
		reqErr := &acp.RequestError{
			Code:    -32603,
			Message: "Internal error",
			Data: map[string]any{
				"error": "peer disconnected",
			},
		}
		assert.True(t, isACPPeerDisconnected(reqErr))
	})

	t.Run("request_error_code_minus32603_no_disconnect_data", func(t *testing.T) {
		reqErr := &acp.RequestError{
			Code:    -32603,
			Message: "Internal error",
			Data: map[string]any{
				"error": "something else",
			},
		}
		assert.False(t, isACPPeerDisconnected(reqErr))
	})

	t.Run("request_error_wrong_code", func(t *testing.T) {
		reqErr := &acp.RequestError{
			Code:    -32000,
			Message: "peer disconnected",
		}
		assert.False(t, isACPPeerDisconnected(reqErr))
	})

	t.Run("request_error_code_minus32603_with_broken_pipe_in_message", func(t *testing.T) {
		reqErr := &acp.RequestError{
			Code:    -32603,
			Message: "broken pipe on write",
		}
		assert.True(t, isACPPeerDisconnected(reqErr))
	})
}

// ---------------------------------------------------------------------------
// isUnknownConfigOption
// ---------------------------------------------------------------------------

func TestRefactor_IsUnknownConfigOption(t *testing.T) {
	t.Run("plain_error", func(t *testing.T) {
		err := fmt.Errorf("Unknown config option: foo")
		assert.True(t, isUnknownConfigOption(err))
	})

	t.Run("plain_error_unrelated", func(t *testing.T) {
		err := fmt.Errorf("permission denied")
		assert.False(t, isUnknownConfigOption(err))
	})

	t.Run("request_error_with_details", func(t *testing.T) {
		reqErr := &acp.RequestError{
			Code:    -32602,
			Message: "Invalid params",
			Data: map[string]any{
				"details": "Unknown config option: thinkingEffort",
			},
		}
		assert.True(t, isUnknownConfigOption(reqErr))
	})

	t.Run("request_error_without_matching_details", func(t *testing.T) {
		reqErr := &acp.RequestError{
			Code:    -32602,
			Message: "Invalid params",
			Data: map[string]any{
				"details": "invalid value",
			},
		}
		assert.False(t, isUnknownConfigOption(reqErr))
	})
}

// ---------------------------------------------------------------------------
// IsACPResourceNotFound
// ---------------------------------------------------------------------------

func TestRefactor_IsACPResourceNotFound(t *testing.T) {
	t.Run("plain_error", func(t *testing.T) {
		err := fmt.Errorf("Resource not found: session xyz")
		assert.True(t, IsACPResourceNotFound(err))
	})

	t.Run("plain_error_unrelated", func(t *testing.T) {
		err := fmt.Errorf("something else")
		assert.False(t, IsACPResourceNotFound(err))
	})

	t.Run("request_error_code_minus32002", func(t *testing.T) {
		reqErr := &acp.RequestError{
			Code:    -32002,
			Message: "Resource not found",
		}
		assert.True(t, IsACPResourceNotFound(reqErr))
	})

	t.Run("request_error_wrong_code", func(t *testing.T) {
		reqErr := &acp.RequestError{
			Code:    -32603,
			Message: "Internal error",
		}
		assert.False(t, IsACPResourceNotFound(reqErr))
	})

	t.Run("request_error_code_minus32002_wrong_message", func(t *testing.T) {
		reqErr := &acp.RequestError{
			Code:    -32002,
			Message: "Something else",
		}
		assert.False(t, IsACPResourceNotFound(reqErr))
	})
}

// ---------------------------------------------------------------------------
// configKilledConnectionError
// ---------------------------------------------------------------------------

func TestRefactor_ConfigKilledConnectionError(t *testing.T) {
	t.Run("basic_error", func(t *testing.T) {
		err := &configKilledConnectionError{configID: "model", value: "gpt-4"}
		assert.Equal(t, "model", err.ConfigID())
		assert.Equal(t, "gpt-4", err.Value())
		assert.Contains(t, err.Error(), "acp: set_config_option(model) killed connection")
		assert.Contains(t, err.Error(), "value=gpt-4")
	})

	t.Run("no_value", func(t *testing.T) {
		err := &configKilledConnectionError{configID: "mode"}
		assert.Contains(t, err.Error(), "acp: set_config_option(mode) killed connection")
		assert.NotContains(t, err.Error(), "value=")
	})

	t.Run("with_diagnostics", func(t *testing.T) {
		err := &configKilledConnectionError{
			configID: "thinkingEffort",
			value:    "high",
			diag:     crashDiagnostics{ExitCode: 139, Signal: "SIGSEGV"},
		}
		s := err.Error()
		assert.Contains(t, s, "thinkingEffort")
		assert.Contains(t, s, "high")
		assert.Contains(t, s, "exit_code=139")
		assert.Contains(t, s, "SIGSEGV")
	})

	t.Run("isConfigKilledConnection", func(t *testing.T) {
		err := errConfigKilledConnection("mode", "code")
		assert.True(t, isConfigKilledConnection(err))
		assert.False(t, isConfigKilledConnection(fmt.Errorf("other error")))
	})

	t.Run("errors_as", func(t *testing.T) {
		err := errConfigKilledConnectionWithDiag("model", "gpt-4", crashDiagnostics{ExitCode: 1})
		var target *configKilledConnectionError
		assert.True(t, errors.As(err, &target))
		assert.Equal(t, "model", target.ConfigID())
	})
}

// ---------------------------------------------------------------------------
// extractToolName
// ---------------------------------------------------------------------------

func TestRefactor_ExtractToolName(t *testing.T) {
	t.Run("toolCallID_prefix_match", func(t *testing.T) {
		assert.Equal(t, "Read", extractToolName("", acp.ToolKindRead, "read_file-1234-5"))
		assert.Equal(t, "Bash", extractToolName("", acp.ToolKindExecute, "run_shell_command-1234-5"))
		assert.Equal(t, "LS", extractToolName("", acp.ToolKindOther, "list_directory-1234-5"))
		assert.Equal(t, "Glob", extractToolName("", acp.ToolKindOther, "glob-1234-5"))
		assert.Equal(t, "AskUserQuestion", extractToolName("", acp.ToolKindOther, "ask-uuid-123"))
	})

	t.Run("toolCallID_no_dash", func(t *testing.T) {
		// No dash → no prefix extraction
		assert.Equal(t, "Bash", extractToolName("Bash", acp.ToolKindExecute, "nodash"))
	})

	t.Run("toolCallID_unknown_prefix", func(t *testing.T) {
		// Unknown prefix falls through to title/alias matching
		assert.Equal(t, "Bash", extractToolName("Bash", acp.ToolKindExecute, "unknown_prefix-123"))
	})

	t.Run("lowercase_alias", func(t *testing.T) {
		assert.Equal(t, "Bash", extractToolName("bash", acp.ToolKindExecute))
		assert.Equal(t, "Bash", extractToolName("terminal", acp.ToolKindExecute))
		assert.Equal(t, "Bash", extractToolName("shell", acp.ToolKindExecute))
		assert.Equal(t, "Read", extractToolName("read", acp.ToolKindRead))
		assert.Equal(t, "Write", extractToolName("write", acp.ToolKindEdit))
		assert.Equal(t, "Edit", extractToolName("edit", acp.ToolKindEdit))
		assert.Equal(t, "Glob", extractToolName("glob", acp.ToolKindOther))
		assert.Equal(t, "Grep", extractToolName("grep", acp.ToolKindSearch))
		assert.Equal(t, "LS", extractToolName("ls", acp.ToolKindOther))
		assert.Equal(t, "LS", extractToolName("list", acp.ToolKindOther))
	})

	t.Run("prefix_patterns", func(t *testing.T) {
		assert.Equal(t, "MultiEdit", extractToolName("MultiEdit file", acp.ToolKindEdit))
		assert.Equal(t, "NotebookEdit", extractToolName("NotebookEdit cell", acp.ToolKindEdit))
		assert.Equal(t, "WebSearch", extractToolName("WebSearch query", acp.ToolKindFetch))
		assert.Equal(t, "WebFetch", extractToolName("WebFetch url", acp.ToolKindFetch))
		assert.Equal(t, "AskUserQuestion", extractToolName("AskUserQuestion about", acp.ToolKindOther))
		assert.Equal(t, "TodoWrite", extractToolName("TodoWrite list", acp.ToolKindOther))
	})

	t.Run("single_word_passthrough", func(t *testing.T) {
		// Single word without space/dot/slash passes through
		assert.Equal(t, "CustomTool", extractToolName("CustomTool", acp.ToolKindOther))
	})

	t.Run("agent_subtype_mapping", func(t *testing.T) {
		// Known agent subtypes map to "Agent"
		assert.Equal(t, "Agent", extractToolName("Explore", acp.ToolKindOther))
		assert.Equal(t, "Agent", extractToolName("Plan", acp.ToolKindOther))
		assert.Equal(t, "Agent", extractToolName("explore", acp.ToolKindOther))
		assert.Equal(t, "Agent", extractToolName("general-purpose", acp.ToolKindOther))
	})

	t.Run("file_path_falls_through_to_kind", func(t *testing.T) {
		// Titles with dots/slashes are not canonical tool names → fall to kind mapping
		assert.Equal(t, "Read", extractToolName("README.md", acp.ToolKindRead))
		assert.Equal(t, "Grep", extractToolName("cmd/server", acp.ToolKindSearch))
	})

	t.Run("kind_fallback", func(t *testing.T) {
		assert.Equal(t, "Read", extractToolName("", acp.ToolKindRead))
		assert.Equal(t, "Edit", extractToolName("", acp.ToolKindEdit))
		assert.Equal(t, "Edit", extractToolName("", acp.ToolKindDelete))
		assert.Equal(t, "Edit", extractToolName("", acp.ToolKindMove))
		assert.Equal(t, "Grep", extractToolName("", acp.ToolKindSearch))
		assert.Equal(t, "Bash", extractToolName("", acp.ToolKindExecute))
		assert.Equal(t, "DeepThink", extractToolName("", acp.ToolKindThink))
		assert.Equal(t, "WebFetch", extractToolName("", acp.ToolKindFetch))
		assert.Equal(t, "EnterPlanMode", extractToolName("", acp.ToolKindSwitchMode))
		assert.Equal(t, "Skill", extractToolName("", acp.ToolKindOther))
	})

	t.Run("empty_everything", func(t *testing.T) {
		// Unknown kind falls through to string(kind)
		result := extractToolName("", acp.ToolKind("custom"))
		assert.Equal(t, "custom", result)
	})
}

// ---------------------------------------------------------------------------
// acpIsAgentSubtype
// ---------------------------------------------------------------------------

func TestRefactor_AcpIsAgentSubtype(t *testing.T) {
	assert.True(t, acpIsAgentSubtype("explore"))
	assert.True(t, acpIsAgentSubtype("Explore"))
	assert.True(t, acpIsAgentSubtype("PLAN"))
	assert.True(t, acpIsAgentSubtype("general-purpose"))
	assert.True(t, acpIsAgentSubtype("fork"))
	assert.False(t, acpIsAgentSubtype("Read"))
	assert.False(t, acpIsAgentSubtype("Bash"))
}

// ---------------------------------------------------------------------------
// extractACPModeState / extractACPModeStateFromResume
// ---------------------------------------------------------------------------

func TestRefactor_ExtractACPModeState(t *testing.T) {
	t.Run("nil_response", func(t *testing.T) {
		assert.Nil(t, extractACPModeState(nil))
	})

	t.Run("nil_modes", func(t *testing.T) {
		resp := &acp.NewSessionResponse{}
		assert.Nil(t, extractACPModeState(resp))
	})

	t.Run("with_modes", func(t *testing.T) {
		modeCat := acp.SessionConfigOptionCategoryMode
		resp := &acp.NewSessionResponse{
			Modes: &acp.SessionModeState{
				CurrentModeId: "code",
				AvailableModes: []acp.SessionMode{
					{Id: "ask", Name: "Ask"},
					{Id: "code", Name: "Code"},
				},
			},
			ConfigOptions: []acp.SessionConfigOption{
				{
					Select: &acp.SessionConfigOptionSelect{
						Category:     &modeCat,
						Id:           "mode",
						Name:         "Mode",
						CurrentValue: "code",
						Options:      acp.SessionConfigSelectOptions{},
					},
				},
			},
		}
		ms := extractACPModeState(resp)
		require.NotNil(t, ms)
		assert.Equal(t, "code", ms.CurrentModeID)
		require.Len(t, ms.AvailableModes, 2)
		assert.Equal(t, "ask", ms.AvailableModes[0].ID)
		assert.Equal(t, "Code", ms.AvailableModes[1].Name)
	})

	t.Run("empty_modes_returns_nil", func(t *testing.T) {
		resp := &acp.NewSessionResponse{
			Modes: &acp.SessionModeState{},
		}
		assert.Nil(t, extractACPModeState(resp))
	})
}

func TestRefactor_ExtractACPModeStateFromResume(t *testing.T) {
	t.Run("nil_response", func(t *testing.T) {
		assert.Nil(t, extractACPModeStateFromResume(nil))
	})

	t.Run("with_modes", func(t *testing.T) {
		resp := &acp.ResumeSessionResponse{
			Modes: &acp.SessionModeState{
				CurrentModeId: "architect",
				AvailableModes: []acp.SessionMode{
					{Id: "architect", Name: "Architect"},
				},
			},
		}
		ms := extractACPModeStateFromResume(resp)
		require.NotNil(t, ms)
		assert.Equal(t, "architect", ms.CurrentModeID)
	})
}

// ---------------------------------------------------------------------------
// extractACPConfigOptions / extractACPConfigOptionsFromResume
// ---------------------------------------------------------------------------

func TestRefactor_ExtractACPConfigOptions(t *testing.T) {
	t.Run("nil_response", func(t *testing.T) {
		assert.Nil(t, extractACPConfigOptions(nil))
	})

	t.Run("empty_config_options", func(t *testing.T) {
		resp := &acp.NewSessionResponse{}
		assert.Nil(t, extractACPConfigOptions(resp))
	})

	t.Run("no_mode_category", func(t *testing.T) {
		modelCat := acp.SessionConfigOptionCategoryModel
		resp := &acp.NewSessionResponse{
			ConfigOptions: []acp.SessionConfigOption{
				{
					Select: &acp.SessionConfigOptionSelect{
						Category: &modelCat,
						Id:       "model",
						Name:     "Model",
					},
				},
			},
		}
		assert.Nil(t, extractACPConfigOptions(resp))
	})

	t.Run("with_mode_category", func(t *testing.T) {
		modeCat := acp.SessionConfigOptionCategoryMode
		opts := acp.SessionConfigSelectOptions{
			Ungrouped: &acp.SessionConfigSelectOptionsUngrouped{
				{Value: "ask", Name: "Ask"},
				{Value: "code", Name: "Code"},
			},
		}
		resp := &acp.NewSessionResponse{
			ConfigOptions: []acp.SessionConfigOption{
				{
					Select: &acp.SessionConfigOptionSelect{
						Category:     &modeCat,
						Id:           "mode",
						Name:         "Mode",
						CurrentValue: "code",
						Options:      opts,
					},
				},
			},
		}
		cs := extractACPConfigOptions(resp)
		require.NotNil(t, cs)
		assert.Equal(t, "mode", cs.ConfigID)
		assert.Equal(t, "code", cs.CurrentID)
		require.Len(t, cs.Options, 1)
		assert.Equal(t, "mode", cs.Options[0].Category)
		require.Len(t, cs.Options[0].Values, 2)
		assert.Equal(t, "ask", cs.Options[0].Values[0].ID)
		assert.Equal(t, "Code", cs.Options[0].Values[1].Name)
	})

	t.Run("no_select_field", func(t *testing.T) {
		// ConfigOption with Boolean instead of Select should be skipped
		resp := &acp.NewSessionResponse{
			ConfigOptions: []acp.SessionConfigOption{
				{
					Boolean: &acp.SessionConfigOptionBoolean{
						Id:           "autoApprove",
						Name:         "Auto Approve",
						CurrentValue: true,
					},
				},
			},
		}
		assert.Nil(t, extractACPConfigOptions(resp))
	})
}

func TestRefactor_ExtractACPConfigOptionsFromResume(t *testing.T) {
	t.Run("nil_response", func(t *testing.T) {
		assert.Nil(t, extractACPConfigOptionsFromResume(nil))
	})

	t.Run("with_mode_category", func(t *testing.T) {
		modeCat := acp.SessionConfigOptionCategoryMode
		resp := &acp.ResumeSessionResponse{
			ConfigOptions: []acp.SessionConfigOption{
				{
					Select: &acp.SessionConfigOptionSelect{
						Category:     &modeCat,
						Id:           "mode",
						Name:         "Mode",
						CurrentValue: "ask",
						Options:      acp.SessionConfigSelectOptions{},
					},
				},
			},
		}
		cs := extractACPConfigOptionsFromResume(resp)
		require.NotNil(t, cs)
		assert.Equal(t, "ask", cs.CurrentID)
	})
}

// ---------------------------------------------------------------------------
// extractACPThinkingEffort / extractACPThinkingEffortFromResume
// ---------------------------------------------------------------------------

func TestRefactor_ExtractACPThinkingEffort(t *testing.T) {
	t.Run("nil_response", func(t *testing.T) {
		assert.Nil(t, extractACPThinkingEffort(nil))
	})

	t.Run("no_thought_level_category", func(t *testing.T) {
		modeCat := acp.SessionConfigOptionCategoryMode
		resp := &acp.NewSessionResponse{
			ConfigOptions: []acp.SessionConfigOption{
				{
					Select: &acp.SessionConfigOptionSelect{
						Category: &modeCat,
					},
				},
			},
		}
		assert.Nil(t, extractACPThinkingEffort(resp))
	})

	t.Run("with_thought_level_ungrouped", func(t *testing.T) {
		thoughtCat := acp.SessionConfigOptionCategoryThoughtLevel
		opts := acp.SessionConfigSelectOptions{
			Ungrouped: &acp.SessionConfigSelectOptionsUngrouped{
				{Value: "low", Name: "Low"},
				{Value: "medium", Name: "Medium"},
				{Value: "high", Name: "High"},
			},
		}
		resp := &acp.NewSessionResponse{
			ConfigOptions: []acp.SessionConfigOption{
				{
					Select: &acp.SessionConfigOptionSelect{
						Category:     &thoughtCat,
						Id:           "thinkingEffort",
						Name:         "Thinking Effort",
						CurrentValue: "medium",
						Options:      opts,
					},
				},
			},
		}
		state := extractACPThinkingEffort(resp)
		require.NotNil(t, state)
		assert.Equal(t, "medium", state.CurrentID)
		require.Len(t, state.AvailableLevels, 3)
		assert.Equal(t, "low", state.AvailableLevels[0].ID)
		assert.Equal(t, "High", state.AvailableLevels[2].Name)
	})

	t.Run("with_thought_level_grouped", func(t *testing.T) {
		thoughtCat := acp.SessionConfigOptionCategoryThoughtLevel
		opts := acp.SessionConfigSelectOptions{
			Grouped: &acp.SessionConfigSelectOptionsGrouped{
				{
					Group: "tier1",
					Name:  "Standard",
					Options: []acp.SessionConfigSelectOption{
						{Value: "low", Name: "Low"},
					},
				},
				{
					Group: "tier2",
					Name:  "Extended",
					Options: []acp.SessionConfigSelectOption{
						{Value: "high", Name: "High"},
					},
				},
			},
		}
		resp := &acp.NewSessionResponse{
			ConfigOptions: []acp.SessionConfigOption{
				{
					Select: &acp.SessionConfigOptionSelect{
						Category:     &thoughtCat,
						Id:           "thinkingEffort",
						Name:         "Thinking Effort",
						CurrentValue: "high",
						Options:      opts,
					},
				},
			},
		}
		state := extractACPThinkingEffort(resp)
		require.NotNil(t, state)
		assert.Equal(t, "high", state.CurrentID)
		require.Len(t, state.AvailableLevels, 2)
		assert.Equal(t, "low", state.AvailableLevels[0].ID)
		assert.Equal(t, "high", state.AvailableLevels[1].ID)
	})
}

func TestRefactor_ExtractACPThinkingEffortFromResume(t *testing.T) {
	t.Run("nil_response", func(t *testing.T) {
		assert.Nil(t, extractACPThinkingEffortFromResume(nil))
	})

	t.Run("with_thought_level", func(t *testing.T) {
		thoughtCat := acp.SessionConfigOptionCategoryThoughtLevel
		resp := &acp.ResumeSessionResponse{
			ConfigOptions: []acp.SessionConfigOption{
				{
					Select: &acp.SessionConfigOptionSelect{
						Category:     &thoughtCat,
						Id:           "thinkingEffort",
						Name:         "Thinking",
						CurrentValue: "low",
						Options:      acp.SessionConfigSelectOptions{},
					},
				},
			},
		}
		state := extractACPThinkingEffortFromResume(resp)
		require.NotNil(t, state)
		assert.Equal(t, "low", state.CurrentID)
	})
}

// ---------------------------------------------------------------------------
// extractACPModelList / extractACPModelListFromResume
// ---------------------------------------------------------------------------

func TestRefactor_ExtractACPModelList(t *testing.T) {
	t.Run("nil_response", func(t *testing.T) {
		assert.Nil(t, extractACPModelList(nil))
	})

	t.Run("no_model_category", func(t *testing.T) {
		modeCat := acp.SessionConfigOptionCategoryMode
		resp := &acp.NewSessionResponse{
			ConfigOptions: []acp.SessionConfigOption{
				{
					Select: &acp.SessionConfigOptionSelect{
						Category: &modeCat,
					},
				},
			},
		}
		assert.Nil(t, extractACPModelList(resp))
	})

	t.Run("with_model_category_ungrouped", func(t *testing.T) {
		modelCat := acp.SessionConfigOptionCategoryModel
		opts := acp.SessionConfigSelectOptions{
			Ungrouped: &acp.SessionConfigSelectOptionsUngrouped{
				{Value: "gpt-4", Name: "GPT-4"},
				{Value: "claude-3", Name: "Claude 3"},
			},
		}
		resp := &acp.NewSessionResponse{
			ConfigOptions: []acp.SessionConfigOption{
				{
					Select: &acp.SessionConfigOptionSelect{
						Category:     &modelCat,
						Id:           "model",
						Name:         "Model",
						CurrentValue: "gpt-4",
						Options:      opts,
					},
				},
			},
		}
		ml := extractACPModelList(resp)
		require.NotNil(t, ml)
		assert.Equal(t, "gpt-4", ml.CurrentModelID)
		require.Len(t, ml.Models, 2)
		assert.Equal(t, "gpt-4", ml.Models[0].ID)
		assert.Equal(t, "Claude 3", ml.Models[1].Name)
	})
}

func TestRefactor_ExtractACPModelListFromResume(t *testing.T) {
	t.Run("nil_response", func(t *testing.T) {
		assert.Nil(t, extractACPModelListFromResume(nil))
	})

	t.Run("with_model_category", func(t *testing.T) {
		modelCat := acp.SessionConfigOptionCategoryModel
		opts := acp.SessionConfigSelectOptions{
			Ungrouped: &acp.SessionConfigSelectOptionsUngrouped{
				{Value: "gemini-pro", Name: "Gemini Pro"},
			},
		}
		resp := &acp.ResumeSessionResponse{
			ConfigOptions: []acp.SessionConfigOption{
				{
					Select: &acp.SessionConfigOptionSelect{
						Category:     &modelCat,
						Id:           "model",
						Name:         "Model",
						CurrentValue: "gemini-pro",
						Options:      opts,
					},
				},
			},
		}
		ml := extractACPModelListFromResume(resp)
		require.NotNil(t, ml)
		assert.Equal(t, "gemini-pro", ml.CurrentModelID)
	})
}

// ---------------------------------------------------------------------------
// modeStateFromConfigState
// ---------------------------------------------------------------------------

func TestRefactor_ModeStateFromConfigState(t *testing.T) {
	t.Run("nil_input", func(t *testing.T) {
		assert.Nil(t, modeStateFromConfigState(nil))
	})

	t.Run("no_mode_category", func(t *testing.T) {
		cs := &ConfigOptionState{
			ConfigID: "model",
			Options: []ConfigOptionDef{
				{ID: "model", Category: "model", Values: []ConfigOptionValue{{ID: "gpt-4"}}},
			},
		}
		assert.Nil(t, modeStateFromConfigState(cs))
	})

	t.Run("with_mode_category", func(t *testing.T) {
		cs := &ConfigOptionState{
			ConfigID:  "mode",
			CurrentID: "code",
			Options: []ConfigOptionDef{
				{
					ID:       "mode",
					Category: "mode",
					Values: []ConfigOptionValue{
						{ID: "ask", Name: "Ask"},
						{ID: "code", Name: "Code"},
					},
				},
			},
		}
		ms := modeStateFromConfigState(cs)
		require.NotNil(t, ms)
		assert.Equal(t, "code", ms.CurrentModeID)
		require.Len(t, ms.AvailableModes, 2)
		assert.Equal(t, "ask", ms.AvailableModes[0].ID)
	})
}

// ---------------------------------------------------------------------------
// shouldSetConfig / markConfigSet / resetLastSetConfig / IsConfigUnsupported
// ---------------------------------------------------------------------------

func TestRefactor_ShouldSetConfig(t *testing.T) {
	agent := &model.Agent{ID: "test-should-config", Backend: "acp-stdio", AcpCommand: "echo"}
	conn := newACPConn(agent, "test-should-config")

	// Initially all values are empty, so any non-empty value should be set
	assert.True(t, conn.shouldSetConfig("model", "gpt-4"))
	assert.True(t, conn.shouldSetConfig("thinkingEffort", "high"))
	assert.True(t, conn.shouldSetConfig("mode", "code"))

	// Same value → should not set
	conn.markConfigSet("model", "gpt-4")
	assert.False(t, conn.shouldSetConfig("model", "gpt-4"))
	assert.True(t, conn.shouldSetConfig("model", "claude-3"))

	// Unknown configID → always true
	assert.True(t, conn.shouldSetConfig("unknown", "value"))
}

func TestRefactor_ResetLastSetConfig(t *testing.T) {
	agent := &model.Agent{ID: "test-reset-config", Backend: "acp-stdio", AcpCommand: "echo"}
	conn := newACPConn(agent, "test-reset-config")

	conn.markConfigSet("model", "gpt-4")
	conn.markConfigSet("mode", "code")
	assert.False(t, conn.shouldSetConfig("model", "gpt-4"))

	conn.resetLastSetConfig()
	assert.True(t, conn.shouldSetConfig("model", "gpt-4"))
	assert.True(t, conn.shouldSetConfig("mode", "code"))
}

func TestRefactor_IsConfigUnsupported(t *testing.T) {
	agent := &model.Agent{ID: "test-unsupported", Backend: "acp-stdio", AcpCommand: "echo"}
	conn := newACPConn(agent, "test-unsupported")

	assert.False(t, conn.IsConfigUnsupported("mode"))

	// Mark as unsupported
	conn.lastSetConfigMu.Lock()
	conn.unsupportedConfigs = map[string]bool{"mode": true}
	conn.lastSetConfigMu.Unlock()

	assert.True(t, conn.IsConfigUnsupported("mode"))
	assert.False(t, conn.IsConfigUnsupported("model"))

	// resetLastSetConfig clears unsupported
	conn.resetLastSetConfig()
	assert.False(t, conn.IsConfigUnsupported("mode"))
}

// ---------------------------------------------------------------------------
// snapshotCachedConfig
// ---------------------------------------------------------------------------

func TestRefactor_SnapshotCachedConfig(t *testing.T) {
	agent := &model.Agent{ID: "test-snapshot", Backend: "acp-stdio", AcpCommand: "echo"}
	conn := newACPConn(agent, "test-snapshot")

	conn.SetCurrentModeID("code")
	conn.SetCurrentModelID("gpt-4")
	conn.SetCurrentThinkingEffortID("high")

	snap := conn.snapshotCachedConfig()
	assert.Equal(t, "code", snap.mode)
	assert.Equal(t, "gpt-4", snap.model)
	assert.Equal(t, "high", snap.effort)
}

// ---------------------------------------------------------------------------
// ACPConn state accessors
// ---------------------------------------------------------------------------

func TestRefactor_ACPConn_Accessors(t *testing.T) {
	agent := &model.Agent{ID: "test-accessors", Backend: "acp-stdio", AcpCommand: "echo"}
	conn := newACPConn(agent, "test-accessors")

	assert.Equal(t, "test-accessors", conn.AgentID())
	assert.Equal(t, "", conn.AcpSID())
	assert.False(t, conn.IsAlive())

	conn.SetCurrentModeID("ask")
	assert.Equal(t, "ask", conn.GetCurrentModeID())

	conn.SetCurrentThinkingEffortID("medium")
	assert.Equal(t, "medium", conn.GetCurrentThinkingEffortID())

	conn.SetCurrentModelID("claude-3")
	assert.Equal(t, "claude-3", conn.GetCurrentModelID())
}

// ---------------------------------------------------------------------------
// buildPromptBlocks
// ---------------------------------------------------------------------------

func TestRefactor_BuildPromptBlocks(t *testing.T) {
	backend := &ACPBackend{}

	t.Run("without_system_prompt", func(t *testing.T) {
		req := ChatRequest{Prompt: "hello world"}
		blocks := backend.buildPromptBlocks(req)
		require.Len(t, blocks, 1)
		require.NotNil(t, blocks[0].Text)
		assert.Equal(t, "hello world", blocks[0].Text.Text)
	})

	t.Run("with_system_prompt", func(t *testing.T) {
		req := ChatRequest{
			Prompt:       "fix the bug",
			SystemPrompt: "You are a helpful assistant",
		}
		// ShouldInjectSystemPrompt returns true when SystemPrompt is set and not Resume
		blocks := backend.buildPromptBlocks(req)
		require.Len(t, blocks, 1)
		require.NotNil(t, blocks[0].Text)
		text := blocks[0].Text.Text
		assert.Contains(t, text, "[System Instructions: You are a helpful assistant]")
		assert.Contains(t, text, "fix the bug")
	})
}

// ---------------------------------------------------------------------------
// readProcStatus (best-effort; may not find /proc in all environments)
// ---------------------------------------------------------------------------

func TestRefactor_ReadProcStatus_InvalidPid(t *testing.T) {
	// A very high PID almost certainly doesn't exist
	ppid, rss, err := readProcStatus(999999999)
	assert.Error(t, err)
	assert.Equal(t, 0, ppid)
	assert.Equal(t, 0, rss)
}

// ---------------------------------------------------------------------------
// SetExternalSessionIDGetter / SetAutoApproveGetter / SetPermissionStateChangeCallback
// ---------------------------------------------------------------------------

func TestRefactor_GlobalSetters(t *testing.T) {
	t.Run("SetExternalSessionIDGetter", func(t *testing.T) {
		orig := getExternalSessionID
		defer func() { getExternalSessionID = orig }()

		// Default returns empty
		assert.Equal(t, "", getExternalSessionID("any"))

		SetExternalSessionIDGetter(func(sid string) string {
			return "ext-" + sid
		})
		assert.Equal(t, "ext-abc", getExternalSessionID("abc"))
	})

	t.Run("SetAutoApproveGetter", func(t *testing.T) {
		orig := getSessionAutoApprove
		defer func() { getSessionAutoApprove = orig }()

		assert.False(t, getSessionAutoApprove("any"))

		SetAutoApproveGetter(func(sid string) bool {
			return sid == "approved"
		})
		assert.True(t, getSessionAutoApprove("approved"))
		assert.False(t, getSessionAutoApprove("other"))
	})

	t.Run("SetPermissionStateChangeCallback", func(t *testing.T) {
		orig := onPermissionStateChange
		defer func() { onPermissionStateChange = orig }()

		called := false
		SetPermissionStateChangeCallback(func(sid string, pending bool) {
			called = true
		})
		onPermissionStateChange("test", true)
		assert.True(t, called)
	})
}

// ---------------------------------------------------------------------------
// mapACPSelectOptions
// ---------------------------------------------------------------------------

func TestRefactor_MapACPSelectOptions(t *testing.T) {
	t.Run("ungrouped", func(t *testing.T) {
		opts := acp.SessionConfigSelectOptions{
			Ungrouped: &acp.SessionConfigSelectOptionsUngrouped{
				{Value: "a", Name: "A"},
				{Value: "b", Name: "B"},
			},
		}
		var optDef ConfigOptionDef
		mapACPSelectOptions(opts, &optDef)
		require.Len(t, optDef.Values, 2)
		assert.Equal(t, "a", optDef.Values[0].ID)
		assert.Equal(t, "B", optDef.Values[1].Name)
	})

	t.Run("grouped", func(t *testing.T) {
		opts := acp.SessionConfigSelectOptions{
			Grouped: &acp.SessionConfigSelectOptionsGrouped{
				{
					Group: "g1",
					Name:  "Group 1",
					Options: []acp.SessionConfigSelectOption{
						{Value: "x", Name: "X"},
					},
				},
			},
		}
		var optDef ConfigOptionDef
		mapACPSelectOptions(opts, &optDef)
		require.Len(t, optDef.Values, 1)
		assert.Equal(t, "x", optDef.Values[0].ID)
	})

	t.Run("both_ungrouped_and_grouped", func(t *testing.T) {
		opts := acp.SessionConfigSelectOptions{
			Ungrouped: &acp.SessionConfigSelectOptionsUngrouped{
				{Value: "a", Name: "A"},
			},
			Grouped: &acp.SessionConfigSelectOptionsGrouped{
				{
					Group: "g1",
					Name:  "Group 1",
					Options: []acp.SessionConfigSelectOption{
						{Value: "b", Name: "B"},
					},
				},
			},
		}
		var optDef ConfigOptionDef
		mapACPSelectOptions(opts, &optDef)
		require.Len(t, optDef.Values, 2)
		assert.Equal(t, "a", optDef.Values[0].ID)
		assert.Equal(t, "b", optDef.Values[1].ID)
	})

	t.Run("empty_options", func(t *testing.T) {
		opts := acp.SessionConfigSelectOptions{}
		var optDef ConfigOptionDef
		mapACPSelectOptions(opts, &optDef)
		assert.Empty(t, optDef.Values)
	})
}

// ---------------------------------------------------------------------------
// buildConfigOptionStateFromSelect
// ---------------------------------------------------------------------------

func TestRefactor_BuildConfigOptionStateFromSelect(t *testing.T) {
	modeCat := acp.SessionConfigOptionCategoryMode
	sel := &acp.SessionConfigOptionSelect{
		Category:     &modeCat,
		Id:           "mode",
		Name:         "Mode",
		CurrentValue: "code",
		Options: acp.SessionConfigSelectOptions{
			Ungrouped: &acp.SessionConfigSelectOptionsUngrouped{
				{Value: "ask", Name: "Ask"},
				{Value: "code", Name: "Code"},
			},
		},
	}
	cs := buildConfigOptionStateFromSelect(sel, "mode")
	require.NotNil(t, cs)
	assert.Equal(t, "mode", cs.ConfigID)
	assert.Equal(t, "code", cs.CurrentID)
	require.Len(t, cs.Options, 1)
	assert.Equal(t, "mode", cs.Options[0].Category)
	require.Len(t, cs.Options[0].Values, 2)
}

// ---------------------------------------------------------------------------
// buildThinkingEffortStateFromSelect
// ---------------------------------------------------------------------------

func TestRefactor_BuildThinkingEffortStateFromSelect_Empty(t *testing.T) {
	// Empty select with no current value → nil
	sel := &acp.SessionConfigOptionSelect{
		Options: acp.SessionConfigSelectOptions{},
	}
	assert.Nil(t, buildThinkingEffortStateFromSelect(sel))
}

// ---------------------------------------------------------------------------
// buildModelListStateFromSelect
// ---------------------------------------------------------------------------

func TestRefactor_BuildModelListStateFromSelect_Empty(t *testing.T) {
	sel := &acp.SessionConfigOptionSelect{
		Options: acp.SessionConfigSelectOptions{},
	}
	assert.Nil(t, buildModelListStateFromSelect(sel))
}

func TestRefactor_BuildModelListStateFromSelect_Grouped(t *testing.T) {
	sel := &acp.SessionConfigOptionSelect{
		CurrentValue: "gpt-4",
		Options: acp.SessionConfigSelectOptions{
			Grouped: &acp.SessionConfigSelectOptionsGrouped{
				{
					Group: "openai",
					Name:  "OpenAI",
					Options: []acp.SessionConfigSelectOption{
						{Value: "gpt-4", Name: "GPT-4"},
						{Value: "gpt-3.5", Name: "GPT-3.5"},
					},
				},
			},
		},
	}
	state := buildModelListStateFromSelect(sel)
	require.NotNil(t, state)
	assert.Equal(t, "gpt-4", state.CurrentModelID)
	require.Len(t, state.Models, 2)
	assert.Equal(t, "gpt-3.5", state.Models[1].ID)
}

// ---------------------------------------------------------------------------
// extractModeStateFromModes edge cases
// ---------------------------------------------------------------------------

func TestRefactor_ExtractModeStateFromModes_CurrentOnly(t *testing.T) {
	// Only current mode set, no available modes → still returns non-nil
	ms := extractModeStateFromModes(&acp.SessionModeState{
		CurrentModeId: "code",
	})
	require.NotNil(t, ms)
	assert.Equal(t, "code", ms.CurrentModeID)
	assert.Empty(t, ms.AvailableModes)
}

// ---------------------------------------------------------------------------
// extractConfigOptionsFromOpts edge cases
// ---------------------------------------------------------------------------

func TestRefactor_ExtractConfigOptionsFromOpts_EmptyOpts(t *testing.T) {
	assert.Nil(t, extractConfigOptionsFromOpts(nil))
	assert.Nil(t, extractConfigOptionsFromOpts([]acp.SessionConfigOption{}))
}

// ---------------------------------------------------------------------------
// OrphanChildEnvVar
// ---------------------------------------------------------------------------

func TestRefactor_OrphanChildEnvVar(t *testing.T) {
	assert.Equal(t, "CLAWBENCH_CHILD=1", OrphanChildEnvVar)
	assert.True(t, strings.Contains(OrphanChildEnvVar, "CLAWBENCH_CHILD"))
}
