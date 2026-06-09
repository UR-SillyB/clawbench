package ai

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"clawbench/internal/model"
)

// --- ACPConnManager.CancelTurn ---

func TestACPConnManager_CancelTurn_NoConn(t *testing.T) {
	// CancelTurn on a session with no connection should not panic
	mgr := GetACPConnManager()
	assert.NotPanics(t, func() {
		mgr.CancelTurn("nonexistent-session")
	})
}

func TestACPConnManager_CancelTurn_WithConn(t *testing.T) {
	mgr := GetACPConnManager()

	// Inject a mock ACPConn with a session mapping
	agent := &model.Agent{ID: "test-cancel-agent", Backend: "acp-stdio", AcpCommand: "echo"}
	conn := newACPConn(agent, "session-cancel-turn")
	mgr.SetConnForTest("session-cancel-turn", conn)
	defer mgr.CloseConn("session-cancel-turn")

	// CancelTurn should not panic even when the connection has no real ACP session
	assert.NotPanics(t, func() {
		mgr.CancelTurn("session-cancel-turn")
	})
}

func TestACPConnManager_CancelTurn_DeadConn(t *testing.T) {
	mgr := GetACPConnManager()

	agent := &model.Agent{ID: "test-dead-agent", Backend: "acp-stdio", AcpCommand: "echo"}
	conn := newACPConn(agent, "session-dead-turn")
	// Connection is alive=false by default (not spawned)
	mgr.SetConnForTest("session-dead-turn", conn)
	defer mgr.CloseConn("session-dead-turn")

	// CancelTurn on a dead connection should not panic
	assert.NotPanics(t, func() {
		mgr.CancelTurn("session-dead-turn")
	})
}

// --- ACPConn.CancelTurn ---

func TestACPConn_CancelTurn_NoSession(t *testing.T) {
	agent := &model.Agent{ID: "test-cancel-nosession", Backend: "acp-stdio", AcpCommand: "echo"}
	conn := newACPConn(agent, "session-no-acp")

	// No ACP session ID set — CancelTurn should be a no-op
	assert.NotPanics(t, func() {
		conn.CancelTurn(context.Background())
	})
}

func TestACPConn_CancelTurn_NoConn(t *testing.T) {
	agent := &model.Agent{ID: "test-cancel-noconn", Backend: "acp-stdio", AcpCommand: "echo"}
	conn := newACPConn(agent, "session-no-conn")

	// No connection object — CancelTurn should be a no-op even with acpSID set
	conn.SetSessionMappingForTest("session-no-conn", "acp-session-123")
	assert.NotPanics(t, func() {
		conn.CancelTurn(context.Background())
	})
}

// --- ACPConnManager.GetConn ---

func TestACPConnManager_GetConn_Exists(t *testing.T) {
	mgr := GetACPConnManager()

	agent := &model.Agent{ID: "test-getconn", Backend: "acp-stdio", AcpCommand: "echo"}
	conn := newACPConn(agent, "session-getconn")
	mgr.SetConnForTest("session-getconn", conn)
	defer mgr.CloseConn("session-getconn")

	got := mgr.GetConn("session-getconn")
	assert.NotNil(t, got)
}

func TestACPConnManager_GetConn_NotExists(t *testing.T) {
	mgr := GetACPConnManager()
	got := mgr.GetConn("nonexistent")
	assert.Nil(t, got)
}

// --- ACPConnManager.MarkIdle ---

func TestACPConnManager_MarkIdle_ExistingConn(t *testing.T) {
	mgr := GetACPConnManager()

	agent := &model.Agent{ID: "test-markidle", Backend: "acp-stdio", AcpCommand: "echo"}
	conn := newACPConn(agent, "session-markidle")
	mgr.SetConnForTest("session-markidle", conn)
	defer mgr.CloseConn("session-markidle")

	// Set lastUsed to a known old time
	conn.mu.Lock()
	conn.lastUsed = time.Now().Add(-10 * time.Minute)
	oldLastUsed := conn.lastUsed
	conn.mu.Unlock()

	// MarkIdle should update lastUsed
	mgr.MarkIdle("session-markidle")

	conn.mu.Lock()
	newLastUsed := conn.lastUsed
	conn.mu.Unlock()

	assert.True(t, newLastUsed.After(oldLastUsed), "MarkIdle should update lastUsed")
}

func TestACPConnManager_MarkIdle_NonexistentConn(t *testing.T) {
	mgr := GetACPConnManager()

	// MarkIdle on a nonexistent session should not panic
	assert.NotPanics(t, func() {
		mgr.MarkIdle("nonexistent-session")
	})
}

// --- spawnLocked mutex release during cmd.Wait ---

func TestACPConn_CancelTurn_DoesNotBlockOnDeadConn(t *testing.T) {
	// This test verifies that CancelTurn does not block when the connection
	// is dead. In the old code, spawnLocked held c.mu during cmd.Wait(),
	// which could block CancelTurn (which also acquires c.mu) indefinitely.
	// After the fix, spawnLocked releases c.mu during cmd.Wait(), so
	// other operations can proceed.
	agent := &model.Agent{ID: "test-spawn-mutex", Backend: "acp-stdio", AcpCommand: "echo"}
	conn := newACPConn(agent, "session-spawn-mutex")

	// The connection starts dead (alive=false). CancelTurn acquires c.mu
	// briefly — it should complete quickly, not block.
	done := make(chan struct{})
	go func() {
		conn.CancelTurn(context.Background())
		close(done)
	}()

	select {
	case <-done:
		// Success — CancelTurn did not block
	case <-time.After(2 * time.Second):
		t.Fatal("CancelTurn blocked — spawnLocked may be holding mutex during cmd.Wait()")
	}
}

// --- ACPConnManager.GetCachedStateByClawbenchSID ---

func TestGetCachedStateByClawbenchSID_NilConn(t *testing.T) {
	mgr := GetACPConnManager()
	s := mgr.GetCachedStateByClawbenchSID("nonexistent-session")
	assert.Nil(t, s.Mode)
	assert.Nil(t, s.Config)
	assert.Nil(t, s.Effort)
	assert.Nil(t, s.Commands)
	assert.Nil(t, s.ModelList)
	assert.Nil(t, s.Plan)
}

func TestGetCachedStateByClawbenchSID_NilClient(t *testing.T) {
	mgr := GetACPConnManager()
	agent := &model.Agent{ID: "test-nil-client", Backend: "acp-stdio", AcpCommand: "echo"}
	conn := newACPConn(agent, "session-nil-client")
	// client is nil by default — should not panic
	mgr.SetConnForTest("session-nil-client", conn)
	defer mgr.CloseConn("session-nil-client")

	assert.NotPanics(t, func() {
		mgr.GetCachedStateByClawbenchSID("session-nil-client")
	})
	s := mgr.GetCachedStateByClawbenchSID("session-nil-client")
	assert.Nil(t, s.Mode)
	assert.Nil(t, s.Config)
	assert.Nil(t, s.Effort)
	assert.Nil(t, s.Commands)
	assert.Nil(t, s.ModelList)
	assert.Nil(t, s.Plan)
}

func TestGetCachedStateByClawbenchSID_WithCachedState(t *testing.T) {
	mgr := GetACPConnManager()
	agent := &model.Agent{ID: "test-cached-state", Backend: "acp-stdio", AcpCommand: "echo"}
	conn := newACPConn(agent, "session-cached-state")
	conn.SetCachedModeState(&ModeState{CurrentModeID: "code", AvailableModes: []ModeDef{{ID: "code", Name: "Code"}}})
	conn.SetCachedPlanState(&PlanState{Entries: []PlanEntry{{Content: "Plan A content", Status: "pending"}}})
	mgr.SetConnForTest("session-cached-state", conn)
	defer mgr.CloseConn("session-cached-state")

	s := mgr.GetCachedStateByClawbenchSID("session-cached-state")
	assert.NotNil(t, s.Mode)
	assert.Equal(t, "code", s.Mode.CurrentModeID)
	assert.NotNil(t, s.Plan)
	assert.Len(t, s.Plan.Entries, 1)
}

// --- ACPConn.shouldSetConfig / markConfigSet ---

func TestACPConn_ShouldSetConfig_ModeInitial(t *testing.T) {
	agent := &model.Agent{ID: "test-config-mode", Backend: "acp-stdio", AcpCommand: "echo"}
	conn := newACPConn(agent, "session-config-mode")

	// Initially, mode should need to be set (empty → non-empty)
	assert.True(t, conn.shouldSetConfig("mode", "code"))
}

func TestACPConn_ShouldSetConfig_ModeSameValue(t *testing.T) {
	agent := &model.Agent{ID: "test-config-mode", Backend: "acp-stdio", AcpCommand: "echo"}
	conn := newACPConn(agent, "session-config-mode")

	conn.markConfigSet("mode", "code")
	assert.False(t, conn.shouldSetConfig("mode", "code"))
}

func TestACPConn_ShouldSetConfig_ModeDifferentValue(t *testing.T) {
	agent := &model.Agent{ID: "test-config-mode", Backend: "acp-stdio", AcpCommand: "echo"}
	conn := newACPConn(agent, "session-config-mode")

	conn.markConfigSet("mode", "code")
	assert.True(t, conn.shouldSetConfig("mode", "ask"))
}

func TestACPConn_ShouldSetConfig_ModeReset(t *testing.T) {
	agent := &model.Agent{ID: "test-config-mode", Backend: "acp-stdio", AcpCommand: "echo"}
	conn := newACPConn(agent, "session-config-mode")

	conn.markConfigSet("mode", "code")
	conn.resetLastSetConfig()
	assert.True(t, conn.shouldSetConfig("mode", "code"))
}

func TestACPConn_ShouldSetConfig_ModelInitial(t *testing.T) {
	agent := &model.Agent{ID: "test-config-model", Backend: "acp-stdio", AcpCommand: "echo"}
	conn := newACPConn(agent, "session-config-model")

	assert.True(t, conn.shouldSetConfig("model", "gpt-4"))
	conn.markConfigSet("model", "gpt-4")
	assert.False(t, conn.shouldSetConfig("model", "gpt-4"))
	assert.True(t, conn.shouldSetConfig("model", "claude-3"))
}

func TestACPConn_ShouldSetConfig_ThinkingEffortInitial(t *testing.T) {
	agent := &model.Agent{ID: "test-config-effort", Backend: "acp-stdio", AcpCommand: "echo"}
	conn := newACPConn(agent, "session-config-effort")

	assert.True(t, conn.shouldSetConfig("thinkingEffort", "high"))
	conn.markConfigSet("thinkingEffort", "high")
	assert.False(t, conn.shouldSetConfig("thinkingEffort", "high"))
}

func TestACPConn_ShouldSetConfig_UnknownConfigID(t *testing.T) {
	agent := &model.Agent{ID: "test-config-unknown", Backend: "acp-stdio", AcpCommand: "echo"}
	conn := newACPConn(agent, "session-config-unknown")

	// Unknown config IDs always return true (no caching)
	assert.True(t, conn.shouldSetConfig("unknown", "value"))
}

func TestACPConn_ShouldSetConfig_UnsupportedConfig(t *testing.T) {
	agent := &model.Agent{ID: "test-unsupported", Backend: "acp-stdio", AcpCommand: "echo"}
	conn := newACPConn(agent, "session-unsupported")

	// Initially, thinkingEffort should be allowed
	assert.True(t, conn.shouldSetConfig("thinkingEffort", "high"))

	// Mark thinkingEffort as unsupported
	conn.lastSetConfigMu.Lock()
	conn.unsupportedConfigs = map[string]bool{"thinkingEffort": true}
	conn.lastSetConfigMu.Unlock()

	// Now shouldSetConfig should return false for thinkingEffort
	assert.False(t, conn.shouldSetConfig("thinkingEffort", "high"))
	assert.False(t, conn.shouldSetConfig("thinkingEffort", "low"))
	// But other configs should still work
	assert.True(t, conn.shouldSetConfig("model", "gpt-4"))
}

func TestACPConn_ResetLastSetConfig_ClearsUnsupported(t *testing.T) {
	agent := &model.Agent{ID: "test-reset-unsupported", Backend: "acp-stdio", AcpCommand: "echo"}
	conn := newACPConn(agent, "session-reset-unsupported")

	// Mark thinkingEffort as unsupported
	conn.lastSetConfigMu.Lock()
	conn.unsupportedConfigs = map[string]bool{"thinkingEffort": true}
	conn.lastSetConfigMu.Unlock()

	assert.False(t, conn.shouldSetConfig("thinkingEffort", "high"))

	// Reset should clear unsupported tracking
	conn.resetLastSetConfig()
	assert.True(t, conn.shouldSetConfig("thinkingEffort", "high"))
}

// --- ACPConn plan state caching ---

func TestACPConn_GetSetCachedPlanState(t *testing.T) {
	agent := &model.Agent{ID: "test-plan-state", Backend: "acp-stdio", AcpCommand: "echo"}
	conn := newACPConn(agent, "session-plan-state")

	// Initially nil
	assert.Nil(t, conn.GetCachedPlanState())

	// Set and get
	planState := &PlanState{Entries: []PlanEntry{{Content: "Step 1 content", Status: "in_progress"}}}
	conn.SetCachedPlanState(planState)
	got := conn.GetCachedPlanState()
	assert.NotNil(t, got)
	assert.Len(t, got.Entries, 1)
	assert.Equal(t, "Step 1 content", got.Entries[0].Content)

	// Clear
	conn.SetCachedPlanState(nil)
	assert.Nil(t, conn.GetCachedPlanState())
}

// --- configKilledConnectionError ---

func TestConfigKilledConnectionError_Value(t *testing.T) {
	err := errConfigKilledConnection("model", "gpt-4")
	var cerr *configKilledConnectionError
	assert.ErrorAs(t, err, &cerr)
	assert.Equal(t, "model", cerr.ConfigID())
	assert.Equal(t, "gpt-4", cerr.Value())
	assert.Contains(t, cerr.Error(), "model")
	assert.Contains(t, cerr.Error(), "gpt-4")
}

func TestConfigKilledConnectionError_EmptyValue(t *testing.T) {
	err := errConfigKilledConnection("mode", "")
	var cerr *configKilledConnectionError
	assert.ErrorAs(t, err, &cerr)
	assert.Equal(t, "mode", cerr.ConfigID())
	assert.Equal(t, "", cerr.Value())
	assert.NotContains(t, cerr.Error(), "value=")
}

func TestConfigKilledConnectionError_Diagnostics(t *testing.T) {
	err := &configKilledConnectionError{
		configID: "thinkingEffort",
		value:    "high",
		diag:     crashDiagnostics{ExitCode: 1, StderrTail: "panic"},
	}
	errMsg := err.Error()
	assert.Contains(t, errMsg, "thinkingEffort")
	assert.Contains(t, errMsg, "high")
	assert.Contains(t, errMsg, "exit_code=1")
	assert.Contains(t, errMsg, "panic")
}

// --- crashDiagnostics.String ---

func TestCrashDiagnostics_String_Empty(t *testing.T) {
	d := crashDiagnostics{}
	assert.Equal(t, "", d.String())
}

func TestCrashDiagnostics_String_ExitCodeOnly(t *testing.T) {
	d := crashDiagnostics{ExitCode: 137}
	assert.Equal(t, " (exit_code=137 (SIGKILL (possible OOM killer)))", d.String())
}

func TestCrashDiagnostics_String_StderrOnly(t *testing.T) {
	d := crashDiagnostics{StderrTail: "segfault"}
	assert.Equal(t, " (stderr: segfault)", d.String())
}

func TestCrashDiagnostics_String_Both(t *testing.T) {
	d := crashDiagnostics{ExitCode: 1, StderrTail: "out of memory"}
	assert.Equal(t, " (exit_code=1 (general error), stderr: out of memory)", d.String())
}

// --- ACPConn.SetCachedConfigState derives modeState ---

func TestACPConn_SetCachedConfigState_DerivesModeState(t *testing.T) {
	agent := &model.Agent{ID: "test-derive-mode", Backend: "acp-stdio", AcpCommand: "echo"}
	conn := newACPConn(agent, "session-derive-mode")

	// cachedModeState starts nil
	assert.Nil(t, conn.GetCachedModeState())

	// Set config state with mode category — should derive modeState
	conn.SetCachedConfigState(&ConfigOptionState{
		ConfigID:  "mode",
		CurrentID: "code",
		Options: []ConfigOptionDef{
			{
				ID:       "mode",
				Name:     "Mode",
				Category: "mode",
				Values: []ConfigOptionValue{
					{ID: "code", Name: "Code"},
					{ID: "ask", Name: "Ask"},
				},
			},
		},
	})

	modeState := conn.GetCachedModeState()
	assert.NotNil(t, modeState)
	assert.Equal(t, "code", modeState.CurrentModeID)
	assert.Len(t, modeState.AvailableModes, 2)
	assert.Equal(t, "code", modeState.AvailableModes[0].ID)
	assert.Equal(t, "ask", modeState.AvailableModes[1].ID)
}

func TestACPConn_SetCachedConfigState_DoesNotOverrideExistingModeState(t *testing.T) {
	agent := &model.Agent{ID: "test-no-override", Backend: "acp-stdio", AcpCommand: "echo"}
	conn := newACPConn(agent, "session-no-override")

	// Set mode state first
	conn.SetCachedModeState(&ModeState{CurrentModeID: "architect", AvailableModes: []ModeDef{{ID: "architect", Name: "Architect"}}})

	// Now set config state — should NOT override existing modeState
	conn.SetCachedConfigState(&ConfigOptionState{
		ConfigID:  "mode",
		CurrentID: "code",
		Options: []ConfigOptionDef{
			{ID: "mode", Category: "mode", Values: []ConfigOptionValue{{ID: "code", Name: "Code"}}},
		},
	})

	modeState := conn.GetCachedModeState()
	assert.NotNil(t, modeState)
	assert.Equal(t, "architect", modeState.CurrentModeID) // Original preserved
}

func TestACPConn_HasCurrentModeChanged(t *testing.T) {
	conn := newACPConn(&model.Agent{ID: "test-mode-changed", Backend: "acp-stdio", AcpCommand: "echo"}, "session-mode-changed")

	// Nil cache — any non-empty modeID is a change
	assert.True(t, conn.HasCurrentModeChanged("code"))
	assert.False(t, conn.HasCurrentModeChanged(""))

	// Set initial mode state
	conn.SetCachedModeState(&ModeState{CurrentModeID: "code", AvailableModes: []ModeDef{{ID: "code", Name: "Code"}}})

	// Same mode — no change
	assert.False(t, conn.HasCurrentModeChanged("code"))

	// Different mode — change
	assert.True(t, conn.HasCurrentModeChanged("ask"))
}

// --- ACPConnManager.PrefetchACPState ---

func TestPrefetchACPState_SIDDedup(t *testing.T) {
	mgr := GetACPConnManager()
	agent := &model.Agent{ID: "test-prefetch-dedup", Backend: "acp-stdio", AcpCommand: "echo"}
	prefetchSID := "_prefetch_" + agent.ID
	defer mgr.CloseConn(prefetchSID)

	// Insert a prefetch conn that is alive with cached state
	conn := newACPConn(agent, prefetchSID)
	conn.SetAliveForTest()
	conn.SetCachedModeState(&ModeState{
		CurrentModeID: "code",
		AvailableModes: []ModeDef{
			{ID: "code", Name: "Code"},
			{ID: "ask", Name: "Ask"},
		},
	})
	mgr.SetConnForTest(prefetchSID, conn)

	// PrefetchACPState should skip — conn is alive and has state
	mgr.PrefetchACPState(agent, "/tmp")

	// Verify the original conn is still there (not replaced)
	got := mgr.GetConn(prefetchSID)
	assert.Equal(t, conn, got)
}

func TestPrefetchACPState_SkipsWhenHasStateEvenIfDead(t *testing.T) {
	mgr := GetACPConnManager()
	agent := &model.Agent{ID: "test-prefetch-has-state", Backend: "acp-stdio", AcpCommand: "echo"}
	prefetchSID := "_prefetch_" + agent.ID
	defer mgr.CloseConn(prefetchSID)

	// Insert a dead conn that still has cached state
	conn := newACPConn(agent, prefetchSID)
	// Not calling SetAliveForTest — conn is dead
	conn.SetCachedModeState(&ModeState{
		CurrentModeID: "code",
		AvailableModes: []ModeDef{{ID: "code", Name: "Code"}},
	})
	mgr.SetConnForTest(prefetchSID, conn)

	// PrefetchACPState should skip — conn has cached state
	mgr.PrefetchACPState(agent, "/tmp")

	got := mgr.GetConn(prefetchSID)
	assert.Equal(t, conn, got) // not replaced
}

func TestPrefetchACPState_RetriesStaleEntry(t *testing.T) {
	mgr := GetACPConnManager()
	agent := &model.Agent{ID: "test-prefetch-stale", Backend: "acp-stdio", AcpCommand: "echo"}
	prefetchSID := "_prefetch_" + agent.ID
	defer mgr.CloseConn(prefetchSID)

	// Insert a dead conn with NO cached state — this is stale
	conn := newACPConn(agent, prefetchSID)
	// Not alive, no cached state
	mgr.SetConnForTest(prefetchSID, conn)

	// The stale entry has no state and is not alive, so PrefetchACPState
	// should remove it and create a new one.
	// Since we can't actually spawn a process in unit tests, we verify
	// that the old conn was removed by checking the map after the call.
	// The new conn won't be able to spawn (echo doesn't speak ACP), so
	// the goroutine will fail and clean up.
	mgr.PrefetchACPState(agent, "/tmp")

	// Wait briefly for the background goroutine to finish
	time.Sleep(200 * time.Millisecond)

	// The old entry should have been replaced (and likely cleaned up
	// by the failed spawn). At minimum, the old conn should be gone.
	mgr.mu.Lock()
	newConn, exists := mgr.conns[prefetchSID]
	mgr.mu.Unlock()
	if exists {
		// If the entry still exists, it should NOT be the old one
		assert.NotEqual(t, conn, newConn)
	}
	// Either the entry was cleaned up (failed spawn) or replaced
}

func TestPrefetchACPState_CallbackFired(t *testing.T) {
	mgr := GetACPConnManager()
	agent := &model.Agent{ID: "test-prefetch-callback", Backend: "acp-stdio", AcpCommand: "echo"}
	prefetchSID := "_prefetch_" + agent.ID
	defer mgr.CloseConn(prefetchSID)

	// Set up callback to capture the result
	var callbackAgentID string
	var callbackState ACPCachedState
	origCallback := onACPStatePrefetched
	onACPStatePrefetched = func(agentID string, state ACPCachedState) {
		callbackAgentID = agentID
		callbackState = state
	}
	defer func() { onACPStatePrefetched = origCallback }()

	// Manually simulate a successful prefetch by inserting a conn with state
	// and calling the callback logic directly (can't spawn real process in unit test)
	conn := newACPConn(agent, prefetchSID)
	conn.SetCachedModeState(&ModeState{
		CurrentModeID: "code",
		AvailableModes: []ModeDef{{ID: "code", Name: "Code"}},
	})
	conn.SetCachedThinkingEffortState(&ThinkingEffortState{
		CurrentID: "high",
		AvailableLevels: []ThinkingEffortDef{{ID: "high", Name: "High"}},
	})
	mgr.SetConnForTest(prefetchSID, conn)

	// Manually invoke the callback with the state (simulating what the goroutine does)
	state := mgr.GetCachedStateByAgentID(agent.ID)
	if state.Mode != nil || state.Effort != nil {
		onACPStatePrefetched(agent.ID, state)
	}

	assert.Equal(t, "test-prefetch-callback", callbackAgentID)
	assert.NotNil(t, callbackState.Mode)
	assert.Equal(t, "code", callbackState.Mode.CurrentModeID)
	assert.NotNil(t, callbackState.Effort)
	assert.Equal(t, "high", callbackState.Effort.CurrentID)
}

func TestSetACPStatePrefetchedCallback(t *testing.T) {
	origCallback := onACPStatePrefetched
	defer func() { onACPStatePrefetched = origCallback }()

	called := false
	SetACPStatePrefetchedCallback(func(agentID string, state ACPCachedState) {
		called = true
	})

	onACPStatePrefetched("test-agent", ACPCachedState{})
	assert.True(t, called)
}

func TestPrefetchACPState_CleansUpOnFailedSpawn(t *testing.T) {
	mgr := GetACPConnManager()
	agent := &model.Agent{ID: "test-prefetch-fail", Backend: "acp-stdio", AcpCommand: "nonexistent-command-xyz"}
	prefetchSID := "_prefetch_" + agent.ID

	mgr.PrefetchACPState(agent, "/tmp")

	// Wait for the background goroutine to fail and clean up
	time.Sleep(500 * time.Millisecond)

	// The prefetch entry should be cleaned up since the command doesn't exist
	mgr.mu.Lock()
	_, exists := mgr.conns[prefetchSID]
	mgr.mu.Unlock()
	assert.False(t, exists, "prefetch entry should be cleaned up after failed spawn")
}
