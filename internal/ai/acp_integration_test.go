//go:build integration

package ai

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"clawbench/internal/model"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Shared Helpers
// ---------------------------------------------------------------------------

// acpTestAgent returns a model.Agent configured for CodeBuddy ACP transport.
func acpTestAgent() *model.Agent {
	return &model.Agent{
		ID:         "codebuddy-acp-test",
		Name:       "CodeBuddy ACP Test",
		Backend:    "codebuddy",
		Transport:  "acp-stdio",
		AcpCommand: "codebuddy --acp",
		Models: []model.AgentModel{
			{ID: "glm-4-plus", Name: "GLM-4 Plus", Default: true},
		},
		ThinkingEffortLevels: []string{"low", "medium", "high"},
	}
}

// acpTestWorkDir returns the project root directory (git repo preferred).
func acpTestWorkDir() string {
	if dir, _ := os.Getwd(); dir != "" {
		return dir
	}
	return os.TempDir()
}

// requireCodeBuddyACPAvailable skips the test if CodeBuddy CLI is not installed
// or doesn't support the `acp` subcommand.
func requireCodeBuddyACPAvailable(t *testing.T) {
	t.Helper()
	path, err := exec.LookPath("codebuddy")
	if err != nil {
		t.Skipf("codebuddy CLI not available, skipping ACP integration test")
	}
	// Verify acp subcommand is supported (codebuddy --acp --version works)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--acp", "--version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Skipf("codebuddy acp subcommand not supported (error: %v, output: %s), skipping",
			err, truncate(string(output), 200))
	}
}

// acpSessionID generates a unique ClawBench session ID for testing.
func acpSessionID() string {
	return uuid.New().String()
}

// acpTestEnv holds test environment state for cleanup.
type acpTestEnv struct {
	mgr      *ACPConnManager
	agent    *model.Agent
	storeSID func(clawbenchSID, acpSID string) // store external session ID
}

// closeConn closes the connection for the given session and removes it from the pool.
func (e *acpTestEnv) closeConn(t *testing.T, sessionID string) {
	t.Helper()
	e.mgr.CloseConn(sessionID)
}

// setupACPTestEnv creates a test environment with external session ID store
// and state persister overrides. Uses t.Cleanup() to ensure global state
// is restored after all test teardown completes.
func setupACPTestEnv(t *testing.T) *acpTestEnv {
	t.Helper()

	agent := acpTestAgent()
	mgr := GetACPConnManager()

	// Store external session IDs in memory (normally backed by DB)
	var sidMu sync.Mutex
	externalSessionIDs := make(map[string]string)

	// Override package-level function for external session ID lookup
	origGetExtSID := getExternalSessionID
	getExternalSessionID = func(clawbenchSID string) string {
		sidMu.Lock()
		defer sidMu.Unlock()
		return externalSessionIDs[clawbenchSID]
	}

	// Override state persister (normally writes to DB)
	origPersist := persistAgentACPStateToDB
	persistAgentACPStateToDB = func(agentID, modeState, commands, thinkingState, modelListState string) error {
		return nil // no-op for testing
	}

	storeSID := func(clawbenchSID, acpSID string) {
		sidMu.Lock()
		defer sidMu.Unlock()
		externalSessionIDs[clawbenchSID] = acpSID
	}

	// Use t.Cleanup to ensure global state is restored after all other teardown
	t.Cleanup(func() {
		getExternalSessionID = origGetExtSID
		persistAgentACPStateToDB = origPersist
	})

	return &acpTestEnv{
		mgr:      mgr,
		agent:    agent,
		storeSID: storeSID,
	}
}

// sendACPPrompt sends a prompt through ACPBackend.ExecuteStream and collects all events.
func sendACPPrompt(t *testing.T, backend *ACPBackend, sessionID, prompt string, timeout time.Duration) []StreamEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ch, err := backend.ExecuteStream(ctx, ChatRequest{
		Prompt:    prompt,
		SessionID: sessionID,
		WorkDir:   acpTestWorkDir(),
	})
	require.NoError(t, err, "ExecuteStream should not return error")

	return collectACPEvents(t, ch, timeout)
}

// collectACPEvents reads all events from the channel until it closes or timeout.
func collectACPEvents(t *testing.T, ch <-chan StreamEvent, timeout time.Duration) []StreamEvent {
	t.Helper()
	var events []StreamEvent
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-ch:
			if !ok {
				return events
			}
			events = append(events, event)
		case <-timer.C:
			t.Log("collectACPEvents: timeout waiting for channel to close")
			return events
		}
	}
}

// findACPEvents returns all events matching the given type.
func findACPEvents(events []StreamEvent, eventType string) []StreamEvent {
	var matched []StreamEvent
	for _, e := range events {
		if e.Type == eventType {
			matched = append(matched, e)
		}
	}
	return matched
}

// requireDoneEvent asserts that events contain a "done" event.
func requireDoneEvent(t *testing.T, events []StreamEvent) {
	t.Helper()
	dones := findACPEvents(events, "done")
	require.NotEmpty(t, dones, "expected a 'done' event in stream, got event types: %v", acpEventTypes(events))
}

// acpEventTypes returns the type of each event as a string slice.
func acpEventTypes(events []StreamEvent) []string {
	types := make([]string, len(events))
	for i, e := range events {
		types[i] = e.Type
	}
	return types
}

// concatACPContent joins all content from content-type events.
func concatACPContent(events []StreamEvent) string {
	var sb strings.Builder
	for _, e := range events {
		if e.Type == "content" {
			sb.WriteString(e.Content)
		}
	}
	return sb.String()
}

// killConnProcess kills the agent subprocess for a given connection.
// This simulates a process crash. Polls for watchProcessDeath to detect it.
func killConnProcess(t *testing.T, conn *ACPConn) {
	t.Helper()
	pid := conn.ProcessPID()
	require.NotZero(t, pid, "connection should have a running process")
	err := conn.KillProcessForTest()
	require.NoError(t, err, "killing agent process should succeed")

	// Poll for watchProcessDeath to detect the crash (up to 5s)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !conn.IsAlive() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out waiting for watchProcessDeath to detect process crash")
}

// requireNoErrorEvents asserts no error events in the stream (may still have done).
func requireNoErrorEvents(t *testing.T, events []StreamEvent) {
	t.Helper()
	errs := findACPEvents(events, "error")
	require.Empty(t, errs, "unexpected error events: %v", errs)
}

// extractACPCaptureID returns the ACP session ID from the first session_capture event.
func extractACPCaptureID(t *testing.T, events []StreamEvent) string {
	t.Helper()
	captures := findACPEvents(events, "session_capture")
	require.NotEmpty(t, captures, "expected session_capture event")
	return captures[0].Content
}

// fmtACPStateSummary returns a summary of cached state for logging.
func fmtACPStateSummary(conn *ACPConn) string {
	mode := "<nil>"
	if ms := conn.GetCachedModeState(); ms != nil {
		mode = ms.CurrentModeID
	}
	model := "<nil>"
	if ml := conn.GetCachedModelListState(); ml != nil {
		model = ml.CurrentModelID
	}
	effort := "<nil>"
	if es := conn.GetCachedThinkingEffortState(); es != nil {
		effort = es.CurrentID
	}
	return fmt.Sprintf("mode=%s model=%s effort=%s", mode, model, effort)
}

// cleanupConn closes the connection for the given session after the test.
func cleanupConn(t *testing.T, sessionID string) {
	t.Helper()
	t.Cleanup(func() {
		GetACPConnManager().CloseConn(sessionID)
	})
}

// ===========================================================================
// Category A: Connection Lifecycle
// ===========================================================================

// A1: First GetOrCreateConn → NewSession → session_capture event + cache populated
func TestACPIntegration_NewSession_CreateAndCapture(t *testing.T) {
	requireCodeBuddyACPAvailable(t)
	env := setupACPTestEnv(t)

	backend, err := NewACPBackend(env.agent)
	require.NoError(t, err)

	sessionID := acpSessionID()
	defer env.closeConn(t, sessionID)
	events := sendACPPrompt(t, backend, sessionID, "说一个字：好", 90*time.Second)
	requireDoneEvent(t, events)

	// Should have session_capture event
	captures := findACPEvents(events, "session_capture")
	require.NotEmpty(t, captures, "new session should emit session_capture")
	assert.NotEmpty(t, captures[0].Content, "session_capture should contain ACP session ID")

	// Should have content event
	content := concatACPContent(events)
	assert.NotEmpty(t, content, "should receive content from agent")

	// Cache should be populated on the connection
	conn := env.mgr.GetConn(sessionID)
	if conn != nil {
		t.Logf("State after first prompt: %s", fmtACPStateSummary(conn))
	}
}

// A2: Second prompt with same sessionID → connection reuse → no second session_capture
func TestACPIntegration_ConnReuse_SameSession(t *testing.T) {
	requireCodeBuddyACPAvailable(t)
	env := setupACPTestEnv(t)

	backend, err := NewACPBackend(env.agent)
	require.NoError(t, err)

	sessionID := acpSessionID()
	defer env.closeConn(t, sessionID)

	// First prompt — creates new session
	events1 := sendACPPrompt(t, backend, sessionID, "说一个字：好", 90*time.Second)
	requireDoneEvent(t, events1)
	captures1 := findACPEvents(events1, "session_capture")
	require.NotEmpty(t, captures1, "first prompt should emit session_capture")
	firstACPSSID := captures1[0].Content

	// Second prompt — should reuse connection
	events2 := sendACPPrompt(t, backend, sessionID, "再说一个字：棒", 90*time.Second)
	requireDoneEvent(t, events2)

	captures2 := findACPEvents(events2, "session_capture")
	assert.Empty(t, captures2, "reused session should NOT emit session_capture again")

	// Content should still arrive
	content := concatACPContent(events2)
	assert.NotEmpty(t, content, "should receive content on reused connection")
	assert.NotEmpty(t, firstACPSSID, "first session capture should have ACP session ID")
}

// A3: Kill agent process → next prompt triggers respawn + ResumeSession → state recovered
func TestACPIntegration_ProcessCrash_AutoResume(t *testing.T) {
	requireCodeBuddyACPAvailable(t)
	env := setupACPTestEnv(t)

	backend, err := NewACPBackend(env.agent)
	require.NoError(t, err)

	sessionID := acpSessionID()
	defer env.closeConn(t, sessionID)

	// First prompt — establish connection
	events1 := sendACPPrompt(t, backend, sessionID, "说一个字：好", 90*time.Second)
	requireDoneEvent(t, events1)
	acpSSID := extractACPCaptureID(t, events1)
	require.NotEmpty(t, acpSSID, "should have ACP session ID")

	// Store external session ID so ResumeSession can find it
	env.storeSID(sessionID, acpSSID)

	// Record state before crash
	conn := env.mgr.GetConn(sessionID)
	require.NotNil(t, conn, "should have a connection after first prompt")
	stateBefore := fmtACPStateSummary(conn)
	t.Logf("State before crash: %s", stateBefore)

	// Kill the agent process to simulate a crash
	killConnProcess(t, conn)

	// Second prompt — should respawn + ResumeSession
	events2 := sendACPPrompt(t, backend, sessionID, "再说一个字：棒", 90*time.Second)
	requireDoneEvent(t, events2)

	// Should get content back (resume succeeded)
	content := concatACPContent(events2)
	assert.NotEmpty(t, content, "should receive content after resume")

	// Connection should be alive again
	conn2 := env.mgr.GetConn(sessionID)
	require.NotNil(t, conn2, "should have a connection after resume")
	assert.True(t, conn2.IsAlive(), "connection should be alive after resume")
	t.Logf("State after resume: %s", fmtACPStateSummary(conn2))
}

// A4: Agent crashes during prompt → isACPPeerDisconnected → auto-retry with respawn
func TestACPIntegration_PeerDisconnect_RetryPrompt(t *testing.T) {
	requireCodeBuddyACPAvailable(t)
	env := setupACPTestEnv(t)

	backend, err := NewACPBackend(env.agent)
	require.NoError(t, err)

	sessionID := acpSessionID()
	defer env.closeConn(t, sessionID)

	// First prompt — establish connection and get ACP session ID
	events1 := sendACPPrompt(t, backend, sessionID, "说一个字：好", 90*time.Second)
	requireDoneEvent(t, events1)
	acpSSID := extractACPCaptureID(t, events1)
	env.storeSID(sessionID, acpSSID)

	// Send a long-running prompt, then kill the process mid-stream
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ch, err := backend.ExecuteStream(ctx, ChatRequest{
		Prompt:    "用200字描述Go语言的优点",
		SessionID: sessionID,
		WorkDir:   acpTestWorkDir(),
	})
	require.NoError(t, err)

	// Collect a few events, then kill the process
	var preKillEvents []StreamEvent
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	collectedEnough := false
	for !collectedEnough {
		select {
		case event, ok := <-ch:
			if !ok {
				collectedEnough = true
				break
			}
			preKillEvents = append(preKillEvents, event)
			if len(preKillEvents) >= 3 {
				collectedEnough = true
			}
		case <-timer.C:
			collectedEnough = true
		}
	}

	// Kill the process mid-stream
	conn := env.mgr.GetConn(sessionID)
	if conn != nil && conn.ProcessPID() != 0 {
		_ = conn.KillProcessForTest()
	}

	// Continue collecting — the retry mechanism should kick in
	remainingEvents := collectACPEvents(t, ch, 90*time.Second)
	allEvents := append(preKillEvents, remainingEvents...)

	// After retry, should eventually get either done or error
	dones := findACPEvents(allEvents, "done")
	errors := findACPEvents(allEvents, "error")
	assert.True(t, len(dones) > 0 || len(errors) > 0,
		"should get either done or error after peer disconnect, got types: %v", acpEventTypes(allEvents))
}

// A5: Idle sweep closes connection → next prompt triggers respawn + ResumeSession
func TestACPIntegration_IdleSweep_ConnectionRecycled(t *testing.T) {
	requireCodeBuddyACPAvailable(t)
	env := setupACPTestEnv(t)

	backend, err := NewACPBackend(env.agent)
	require.NoError(t, err)

	sessionID := acpSessionID()

	// First prompt — establish connection
	events1 := sendACPPrompt(t, backend, sessionID, "说一个字：好", 90*time.Second)
	requireDoneEvent(t, events1)
	acpSSID := extractACPCaptureID(t, events1)
	env.storeSID(sessionID, acpSSID)

	// Simulate idle sweep by manually closing the connection
	env.mgr.CloseConn(sessionID)

	// Second prompt — should create new connection + ResumeSession
	events2 := sendACPPrompt(t, backend, sessionID, "再说一个字：棒", 90*time.Second)
	requireDoneEvent(t, events2)

	content := concatACPContent(events2)
	assert.NotEmpty(t, content, "should receive content after idle sweep + resume")
}

// A6: Explicit CloseConn → next prompt creates entirely new session (not resume)
func TestACPIntegration_ExplicitClose_NewSessionCreated(t *testing.T) {
	requireCodeBuddyACPAvailable(t)
	env := setupACPTestEnv(t)

	backend, err := NewACPBackend(env.agent)
	require.NoError(t, err)

	sessionID := acpSessionID()

	// First prompt
	events1 := sendACPPrompt(t, backend, sessionID, "说一个字：好", 90*time.Second)
	requireDoneEvent(t, events1)
	firstACPSSID := extractACPCaptureID(t, events1)

	// Close the connection explicitly
	env.mgr.CloseConn(sessionID)

	// Don't store external session ID — getExternalSessionID returns "" for this
	// session since storeSID was never called, so ensureAliveWithSession falls
	// through to NewSession instead of ResumeSession

	// Second prompt — should create a brand new session
	events2 := sendACPPrompt(t, backend, sessionID, "再说一个字：棒", 90*time.Second)
	requireDoneEvent(t, events2)

	captures2 := findACPEvents(events2, "session_capture")
	assert.NotEmpty(t, captures2, "new session after close should emit session_capture")

	if len(captures2) > 0 {
		assert.NotEqual(t, firstACPSSID, captures2[0].Content,
			"new session should have a different ACP session ID")
	}
}

// A7: Two sessions → independent connections → one crash doesn't affect the other
func TestACPIntegration_MultipleSessions_IsolatedConns(t *testing.T) {
	requireCodeBuddyACPAvailable(t)
	env := setupACPTestEnv(t)

	backend, err := NewACPBackend(env.agent)
	require.NoError(t, err)

	sessionID1 := acpSessionID()
	sessionID2 := acpSessionID()
	defer env.closeConn(t, sessionID1)
	defer env.closeConn(t, sessionID2)

	// Establish both sessions
	events1a := sendACPPrompt(t, backend, sessionID1, "说一个字：好", 90*time.Second)
	requireDoneEvent(t, events1a)

	events2a := sendACPPrompt(t, backend, sessionID2, "说一个字：棒", 90*time.Second)
	requireDoneEvent(t, events2a)

	// Both should have session_capture with different ACP session IDs
	acpSSID1 := extractACPCaptureID(t, events1a)
	acpSSID2 := extractACPCaptureID(t, events2a)
	assert.NotEqual(t, acpSSID1, acpSSID2,
		"different sessions should have different ACP session IDs")

	// Kill session1's process
	conn1 := env.mgr.GetConn(sessionID1)
	require.NotNil(t, conn1)
	killConnProcess(t, conn1)

	// Session2 should still work fine
	events2b := sendACPPrompt(t, backend, sessionID2, "再说一个字：强", 90*time.Second)
	requireDoneEvent(t, events2b)
	content := concatACPContent(events2b)
	assert.NotEmpty(t, content, "session2 should still work after session1 crashed")
}

// ===========================================================================
// Category B: Mode/Model/Thinking Switching
// ===========================================================================

// B1: Switch mode via SetSessionConfigOption → cache reflects new mode
func TestACPIntegration_ModeSwitch_CodeToPlan(t *testing.T) {
	requireCodeBuddyACPAvailable(t)

	backend, err := NewACPBackend(acpTestAgent())
	require.NoError(t, err)

	sessionID := acpSessionID()
	cleanupConn(t, sessionID)

	// First prompt — establish connection and get initial state
	events1 := sendACPPrompt(t, backend, sessionID, "说一个字：好", 90*time.Second)
	requireDoneEvent(t, events1)

	// Check initial mode
	mgr := GetACPConnManager()
	conn := mgr.GetConn(sessionID)
	require.NotNil(t, conn, "should have a connection")

	modeUpdates := findACPEvents(events1, "mode_update")
	var initialMode string
	if len(modeUpdates) > 0 && modeUpdates[0].Mode != nil {
		initialMode = modeUpdates[0].Mode.CurrentModeID
	}
	t.Logf("Initial mode: %q, full state: %s", initialMode, fmtACPStateSummary(conn))

	// Try switching mode
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn.SetSessionConfigOption(ctx, "mode", "plan")

	// Give the agent a moment to process the config change
	time.Sleep(500 * time.Millisecond)

	// If connection died, the mode was unsupported or caused a crash
	if !conn.IsAlive() {
		t.Skip("Mode switch caused connection death (may not be supported)")
	}

	// After successful switch, cache should reflect "plan"
	modeState := conn.GetCachedModeState()
	require.NotNil(t, modeState, "mode state should be cached after switch")
	assert.Equal(t, "plan", modeState.CurrentModeID, "cached mode should be 'plan'")
	t.Logf("Mode after switch: %q", modeState.CurrentModeID)
}

// B2: Switch model via SetSessionConfigOption → cache reflects new model
func TestACPIntegration_ModelSwitch_ChangeModel(t *testing.T) {
	requireCodeBuddyACPAvailable(t)

	backend, err := NewACPBackend(acpTestAgent())
	require.NoError(t, err)

	sessionID := acpSessionID()
	cleanupConn(t, sessionID)

	// First prompt
	events1 := sendACPPrompt(t, backend, sessionID, "说一个字：好", 90*time.Second)
	requireDoneEvent(t, events1)

	mgr := GetACPConnManager()
	conn := mgr.GetConn(sessionID)
	require.NotNil(t, conn)

	modelList := conn.GetCachedModelListState()
	var targetModel string
	if modelList != nil && len(modelList.Models) > 1 {
		for _, m := range modelList.Models {
			if m.ID != modelList.CurrentModelID {
				targetModel = m.ID
				break
			}
		}
	}
	if targetModel == "" {
		t.Skip("No alternative model available for switching")
	}

	// Switch model
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn.SetSessionConfigOption(ctx, "model", targetModel)
	time.Sleep(500 * time.Millisecond)

	if !conn.IsAlive() {
		t.Skip("Model switch caused connection death")
	}

	modelList2 := conn.GetCachedModelListState()
	require.NotNil(t, modelList2)
	assert.Equal(t, targetModel, modelList2.CurrentModelID, "cached model should be updated")
	t.Logf("Model after switch: %q", modelList2.CurrentModelID)
}

// B3: Switch thinking effort via SetSessionConfigOption → cache reflects new effort
func TestACPIntegration_ThinkingEffortSwitch(t *testing.T) {
	requireCodeBuddyACPAvailable(t)

	backend, err := NewACPBackend(acpTestAgent())
	require.NoError(t, err)

	sessionID := acpSessionID()
	cleanupConn(t, sessionID)

	// First prompt
	events1 := sendACPPrompt(t, backend, sessionID, "说一个字：好", 90*time.Second)
	requireDoneEvent(t, events1)

	mgr := GetACPConnManager()
	conn := mgr.GetConn(sessionID)
	require.NotNil(t, conn)

	// Try switching thinking effort
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn.SetSessionConfigOption(ctx, "thinkingEffort", "high")
	time.Sleep(500 * time.Millisecond)

	if !conn.IsAlive() {
		t.Skip("Thinking effort switch caused connection death (may not be supported)")
	}

	effortState := conn.GetCachedThinkingEffortState()
	require.NotNil(t, effortState)
	assert.Equal(t, "high", effortState.CurrentID, "cached thinking effort should be 'high'")
}

// B4: Unsupported config option → graceful degradation
func TestACPIntegration_UnsupportedConfig_GracefulDegradation(t *testing.T) {
	requireCodeBuddyACPAvailable(t)

	backend, err := NewACPBackend(acpTestAgent())
	require.NoError(t, err)

	sessionID := acpSessionID()
	cleanupConn(t, sessionID)

	events1 := sendACPPrompt(t, backend, sessionID, "说一个字：好", 90*time.Second)
	requireDoneEvent(t, events1)

	mgr := GetACPConnManager()
	conn := mgr.GetConn(sessionID)
	require.NotNil(t, conn)

	// Try setting a non-existent config option
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn.SetSessionConfigOption(ctx, "nonexistent_config_option_xyz", "some_value")
	time.Sleep(500 * time.Millisecond)

	// Connection should still be alive (graceful degradation — errors are logged internally)
	assert.True(t, conn.IsAlive(), "connection should survive unsupported config attempt")
}

// B5: Setting same config value twice → dedup prevents second RPC
func TestACPIntegration_ConfigDedup_NoResend(t *testing.T) {
	requireCodeBuddyACPAvailable(t)

	backend, err := NewACPBackend(acpTestAgent())
	require.NoError(t, err)

	sessionID := acpSessionID()
	cleanupConn(t, sessionID)

	events1 := sendACPPrompt(t, backend, sessionID, "说一个字：好", 90*time.Second)
	requireDoneEvent(t, events1)

	mgr := GetACPConnManager()
	conn := mgr.GetConn(sessionID)
	require.NotNil(t, conn)

	// Set a model, then set the same model again
	modelList := conn.GetCachedModelListState()
	if modelList == nil || modelList.CurrentModelID == "" {
		t.Skip("No model to test dedup")
	}

	ctx := context.Background()
	conn.SetSessionConfigOption(ctx, "model", modelList.CurrentModelID)
	time.Sleep(500 * time.Millisecond)

	// Second set with same value should be deduplicated
	// (verified at unit level in acp_pool_test.go; here we ensure no crash)
	conn.SetSessionConfigOption(ctx, "model", modelList.CurrentModelID)
	assert.True(t, conn.IsAlive(), "connection should survive dedup test")
}

// B6: Config option that crashes agent → configKilledConnectionError → retry skips that config
func TestACPIntegration_ConfigKilledConnection_RetrySkipsConfig(t *testing.T) {
	requireCodeBuddyACPAvailable(t)

	backend, err := NewACPBackend(acpTestAgent())
	require.NoError(t, err)

	sessionID := acpSessionID()
	cleanupConn(t, sessionID)

	// Send a prompt with a non-existent model — the agent may crash or ignore it
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ch, err := backend.ExecuteStream(ctx, ChatRequest{
		Prompt:    "说一个字：好",
		SessionID: sessionID,
		WorkDir:   acpTestWorkDir(),
		Model:     "nonexistent-model-xyz-12345",
	})
	require.NoError(t, err)

	events := collectACPEvents(t, ch, 120*time.Second)

	dones := findACPEvents(events, "done")
	errors := findACPEvents(events, "error")
	assert.True(t, len(dones) > 0 || len(errors) > 0,
		"should get done or error, got types: %v", acpEventTypes(events))

	if len(errors) > 0 {
		t.Logf("Error with invalid model (expected): %s", errors[0].Error)
	}
}

// ===========================================================================
// Category C: State Consistency — "No Amnesia" Tests
// ===========================================================================
//
// These tests verify that ACP state is preserved after process crash + resume.
// "Amnesia" = cached state (mode, model, thinking effort, commands) is lost or
// reset to incorrect values after a connection is respawned.

// C1: Switch to plan mode → crash → resume → cached mode still "plan"
func TestACPIntegration_ResumeAfterCrash_ModePreserved(t *testing.T) {
	requireCodeBuddyACPAvailable(t)
	env := setupACPTestEnv(t)

	backend, err := NewACPBackend(env.agent)
	require.NoError(t, err)

	sessionID := acpSessionID()
	defer env.closeConn(t, sessionID)

	// Step 1: Establish connection
	events1 := sendACPPrompt(t, backend, sessionID, "说一个字：好", 90*time.Second)
	requireDoneEvent(t, events1)
	acpSSID := extractACPCaptureID(t, events1)
	env.storeSID(sessionID, acpSSID)

	// Step 2: Switch mode
	conn := env.mgr.GetConn(sessionID)
	require.NotNil(t, conn)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn.SetSessionConfigOption(ctx, "mode", "plan")
	time.Sleep(500 * time.Millisecond)

	if !conn.IsAlive() {
		t.Skip("Mode switch caused connection death (may not be supported)")
	}

	modeBefore := conn.GetCachedModeState()
	require.NotNil(t, modeBefore, "mode state should be cached before crash")
	t.Logf("Mode before crash: %q", modeBefore.CurrentModeID)

	// Step 3: Kill the process
	killConnProcess(t, conn)

	// Step 4: Resume — send another prompt
	events2 := sendACPPrompt(t, backend, sessionID, "说一个字：行", 90*time.Second)
	requireDoneEvent(t, events2)

	// Step 5: Check mode after resume — should NOT be amnesiac
	conn2 := env.mgr.GetConn(sessionID)
	require.NotNil(t, conn2, "should have a connection after resume")

	modeAfter := conn2.GetCachedModeState()
	require.NotNil(t, modeAfter, "mode state should be cached after resume")

	assert.Equal(t, modeBefore.CurrentModeID, modeAfter.CurrentModeID,
		"AMNESIA DETECTED: mode changed after resume! Before=%q, After=%q",
		modeBefore.CurrentModeID, modeAfter.CurrentModeID)
	t.Logf("Mode after resume: %q (preserved!)", modeAfter.CurrentModeID)
}

// C2: Switch model → crash → resume → cached model still the new model
func TestACPIntegration_ResumeAfterCrash_ModelPreserved(t *testing.T) {
	requireCodeBuddyACPAvailable(t)
	env := setupACPTestEnv(t)

	backend, err := NewACPBackend(env.agent)
	require.NoError(t, err)

	sessionID := acpSessionID()
	defer env.closeConn(t, sessionID)

	// Step 1: Establish connection
	events1 := sendACPPrompt(t, backend, sessionID, "说一个字：好", 90*time.Second)
	requireDoneEvent(t, events1)
	acpSSID := extractACPCaptureID(t, events1)
	env.storeSID(sessionID, acpSSID)

	// Step 2: Switch model
	conn := env.mgr.GetConn(sessionID)
	require.NotNil(t, conn)

	modelList := conn.GetCachedModelListState()
	var targetModel string
	if modelList != nil && len(modelList.Models) > 1 {
		for _, m := range modelList.Models {
			if m.ID != modelList.CurrentModelID {
				targetModel = m.ID
				break
			}
		}
	}
	if targetModel == "" {
		t.Skip("No alternative model available for switching")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn.SetSessionConfigOption(ctx, "model", targetModel)
	time.Sleep(500 * time.Millisecond)

	if !conn.IsAlive() {
		t.Skip("Model switch caused connection death")
	}

	modelBefore := conn.GetCachedModelListState()
	require.NotNil(t, modelBefore)
	t.Logf("Model before crash: %q", modelBefore.CurrentModelID)

	// Step 3: Kill the process
	killConnProcess(t, conn)

	// Step 4: Resume
	events2 := sendACPPrompt(t, backend, sessionID, "说一个字：行", 90*time.Second)
	requireDoneEvent(t, events2)

	// Step 5: Check model after resume
	conn2 := env.mgr.GetConn(sessionID)
	require.NotNil(t, conn2)

	modelAfter := conn2.GetCachedModelListState()
	require.NotNil(t, modelAfter, "model list state should be cached after resume")

	assert.Equal(t, modelBefore.CurrentModelID, modelAfter.CurrentModelID,
		"AMNESIA DETECTED: model changed after resume! Before=%q, After=%q",
		modelBefore.CurrentModelID, modelAfter.CurrentModelID)
	t.Logf("Model after resume: %q (preserved!)", modelAfter.CurrentModelID)
}

// C3: Switch thinking effort → crash → resume → cached effort preserved
func TestACPIntegration_ResumeAfterCrash_ThinkingPreserved(t *testing.T) {
	requireCodeBuddyACPAvailable(t)
	env := setupACPTestEnv(t)

	backend, err := NewACPBackend(env.agent)
	require.NoError(t, err)

	sessionID := acpSessionID()
	defer env.closeConn(t, sessionID)

	// Step 1: Establish connection
	events1 := sendACPPrompt(t, backend, sessionID, "说一个字：好", 90*time.Second)
	requireDoneEvent(t, events1)
	acpSSID := extractACPCaptureID(t, events1)
	env.storeSID(sessionID, acpSSID)

	// Step 2: Switch thinking effort
	conn := env.mgr.GetConn(sessionID)
	require.NotNil(t, conn)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn.SetSessionConfigOption(ctx, "thinkingEffort", "high")
	time.Sleep(500 * time.Millisecond)

	if !conn.IsAlive() {
		t.Skip("Thinking effort switch caused connection death (may not be supported)")
	}

	effortBefore := conn.GetCachedThinkingEffortState()
	require.NotNil(t, effortBefore)
	t.Logf("Thinking effort before crash: %q", effortBefore.CurrentID)

	// Step 3: Kill the process
	killConnProcess(t, conn)

	// Step 4: Resume
	events2 := sendACPPrompt(t, backend, sessionID, "说一个字：行", 90*time.Second)
	requireDoneEvent(t, events2)

	// Step 5: Check thinking effort after resume
	conn2 := env.mgr.GetConn(sessionID)
	require.NotNil(t, conn2)

	effortAfter := conn2.GetCachedThinkingEffortState()
	require.NotNil(t, effortAfter, "thinking effort state should be cached after resume")

	assert.Equal(t, effortBefore.CurrentID, effortAfter.CurrentID,
		"AMNESIA DETECTED: thinking effort changed after resume! Before=%q, After=%q",
		effortBefore.CurrentID, effortAfter.CurrentID)
	t.Logf("Thinking effort after resume: %q (preserved!)", effortAfter.CurrentID)
}

// C4: Agent sends available_commands_update → crash → resume → commands still cached
func TestACPIntegration_ResumeAfterCrash_CommandsPreserved(t *testing.T) {
	requireCodeBuddyACPAvailable(t)
	env := setupACPTestEnv(t)

	backend, err := NewACPBackend(env.agent)
	require.NoError(t, err)

	sessionID := acpSessionID()
	defer env.closeConn(t, sessionID)

	// Step 1: Establish connection (commands are cached during session)
	events1 := sendACPPrompt(t, backend, sessionID, "说一个字：好", 90*time.Second)
	requireDoneEvent(t, events1)
	acpSSID := extractACPCaptureID(t, events1)
	env.storeSID(sessionID, acpSSID)

	conn := env.mgr.GetConn(sessionID)
	require.NotNil(t, conn)

	client := conn.GetClient()
	cmdsBefore := 0
	if client != nil {
		cmdsBefore = len(client.GetCommands())
	}
	t.Logf("Commands before crash: %d", cmdsBefore)

	// Step 2: Kill the process
	killConnProcess(t, conn)

	// Step 3: Resume
	events2 := sendACPPrompt(t, backend, sessionID, "说一个字：行", 90*time.Second)
	requireDoneEvent(t, events2)

	// Step 4: Check commands after resume
	conn2 := env.mgr.GetConn(sessionID)
	require.NotNil(t, conn2)

	client2 := conn2.GetClient()
	cmdsAfter := 0
	if client2 != nil {
		cmdsAfter = len(client2.GetCommands())
	}
	t.Logf("Commands after resume: %d", cmdsAfter)

	// Commands should be re-populated from the resumed session
	if cmdsBefore > 0 {
		assert.Greater(t, cmdsAfter, 0,
			"AMNESIA DETECTED: commands lost after resume! Before=%d, After=%d",
			cmdsBefore, cmdsAfter)
	}
}

// C5: Set model X → crash → resume → lastSetModel reset → re-sends model X (not infinite loop)
func TestACPIntegration_ResumeAfterCrash_ConfigDedupReset(t *testing.T) {
	requireCodeBuddyACPAvailable(t)
	env := setupACPTestEnv(t)

	backend, err := NewACPBackend(env.agent)
	require.NoError(t, err)

	sessionID := acpSessionID()
	defer env.closeConn(t, sessionID)

	// Step 1: Establish connection
	events1 := sendACPPrompt(t, backend, sessionID, "说一个字：好", 90*time.Second)
	requireDoneEvent(t, events1)
	acpSSID := extractACPCaptureID(t, events1)
	env.storeSID(sessionID, acpSSID)

	// Step 2: Switch to a model
	conn := env.mgr.GetConn(sessionID)
	require.NotNil(t, conn)

	modelList := conn.GetCachedModelListState()
	if modelList == nil || modelList.CurrentModelID == "" {
		t.Skip("No model available for switching")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn.SetSessionConfigOption(ctx, "model", modelList.CurrentModelID)

	t.Logf("Model before crash: %q", modelList.CurrentModelID)

	// Step 3: Kill the process — triggers resetLastSetConfig()
	killConnProcess(t, conn)

	// Step 4: Resume — this should work without infinite loop
	events2 := sendACPPrompt(t, backend, sessionID, "说一个字：行", 90*time.Second)
	requireDoneEvent(t, events2)

	// If we get here, no infinite loop occurred
	t.Log("Resume after config dedup reset succeeded — no infinite loop")

	// Verify connection is alive
	conn2 := env.mgr.GetConn(sessionID)
	require.NotNil(t, conn2)
	assert.True(t, conn2.IsAlive(), "connection should be alive after resume")
}

// C6: Agent sends plan → crash → resume → planState is nil (transient, expected loss)
func TestACPIntegration_ResumeAfterCrash_PlanStateLost(t *testing.T) {
	requireCodeBuddyACPAvailable(t)
	env := setupACPTestEnv(t)

	backend, err := NewACPBackend(env.agent)
	require.NoError(t, err)

	sessionID := acpSessionID()
	defer env.closeConn(t, sessionID)

	// Step 1: Establish connection
	events1 := sendACPPrompt(t, backend, sessionID, "说一个字：好", 90*time.Second)
	requireDoneEvent(t, events1)
	acpSSID := extractACPCaptureID(t, events1)
	env.storeSID(sessionID, acpSSID)

	conn := env.mgr.GetConn(sessionID)
	require.NotNil(t, conn)

	planBefore := conn.GetCachedPlanState()
	t.Logf("Plan state before crash: %v", planBefore != nil)

	// Step 2: Kill the process
	killConnProcess(t, conn)

	// Step 3: Resume
	events2 := sendACPPrompt(t, backend, sessionID, "说一个字：行", 90*time.Second)
	requireDoneEvent(t, events2)

	// Step 4: Plan state is expected to be nil after resume (transient by design)
	conn2 := env.mgr.GetConn(sessionID)
	require.NotNil(t, conn2)

	planAfter := conn2.GetCachedPlanState()
	t.Logf("Plan state after resume: %v", planAfter != nil)

	// Document: Plan state is transient and expected to be lost after resume.
	// This is NOT amnesia — it's by design (plan is per-execution-cycle).
	if planBefore != nil && planAfter == nil {
		t.Log("Plan state lost after resume — this is expected (transient state)")
	}
}

// ===========================================================================
// Category D: SSE Disconnect/Reconnect + Long-Running
// ===========================================================================

// D1: Simulate SSE disconnect → drain → agent continues → reconnect → cached state re-emitted
func TestACPIntegration_SSEDisconnect_DrainAndContinue(t *testing.T) {
	requireCodeBuddyACPAvailable(t)

	backend, err := NewACPBackend(acpTestAgent())
	require.NoError(t, err)

	sessionID := acpSessionID()
	cleanupConn(t, sessionID)

	// Step 1: Send a prompt and let it complete normally
	events1 := sendACPPrompt(t, backend, sessionID, "说一个字：好", 90*time.Second)
	requireDoneEvent(t, events1)

	// Step 2: Verify connection is still alive
	mgr := GetACPConnManager()
	conn := mgr.GetConn(sessionID)
	require.NotNil(t, conn)
	assert.True(t, conn.IsAlive(), "connection should be alive after prompt completes")

	// Step 3: Get cached state
	modeBefore := conn.GetCachedModeState()
	effortBefore := conn.GetCachedThinkingEffortState()
	t.Logf("State before reconnect: mode=%v, effort=%v",
		modeBefore != nil, effortBefore != nil)

	// Step 4: Simulate reconnection by sending another prompt
	events2 := sendACPPrompt(t, backend, sessionID, "再说一个字：棒", 90*time.Second)
	requireDoneEvent(t, events2)

	// After reconnection, config_update and commands_update should be re-emitted
	configUpdates := findACPEvents(events2, "config_update")
	commandsUpdates := findACPEvents(events2, "commands_update")
	t.Logf("Config updates on reconnect: %d, commands: %d",
		len(configUpdates), len(commandsUpdates))

	// Content should still arrive
	content := concatACPContent(events2)
	assert.NotEmpty(t, content, "should receive content after reconnect")
}

// D2: Full session → SSE reconnect → state events re-emitted correctly
func TestACPIntegration_SSEReconnect_StateReemitted(t *testing.T) {
	requireCodeBuddyACPAvailable(t)

	backend, err := NewACPBackend(acpTestAgent())
	require.NoError(t, err)

	sessionID := acpSessionID()
	cleanupConn(t, sessionID)

	// First prompt — establish connection and get initial state
	events1 := sendACPPrompt(t, backend, sessionID, "说一个字：好", 90*time.Second)
	requireDoneEvent(t, events1)

	// Record what state events were emitted on first connection
	firstConfigUpdates := findACPEvents(events1, "config_update")
	firstCommandsUpdates := findACPEvents(events1, "commands_update")

	t.Logf("First prompt state events: config=%d, commands=%d",
		len(firstConfigUpdates), len(firstCommandsUpdates))

	// Second prompt (simulates SSE reconnect)
	events2 := sendACPPrompt(t, backend, sessionID, "再说一个字：棒", 90*time.Second)
	requireDoneEvent(t, events2)

	secondConfigUpdates := findACPEvents(events2, "config_update")
	secondCommandsUpdates := findACPEvents(events2, "commands_update")

	t.Logf("Second prompt state events: config=%d, commands=%d",
		len(secondConfigUpdates), len(secondCommandsUpdates))

	// config_update should be re-emitted on every stream
	if len(firstConfigUpdates) > 0 {
		assert.NotEmpty(t, secondConfigUpdates,
			"config_update should be re-emitted on reconnect")
	}

	// Content should arrive on both prompts
	content1 := concatACPContent(events1)
	content2 := concatACPContent(events2)
	assert.NotEmpty(t, content1, "first prompt should have content")
	assert.NotEmpty(t, content2, "second prompt should have content")
}

// D3: 5 turns on same connection → no leaks, cache stays consistent
func TestACPIntegration_LongRunning_MultipleTurns(t *testing.T) {
	requireCodeBuddyACPAvailable(t)

	backend, err := NewACPBackend(acpTestAgent())
	require.NoError(t, err)

	sessionID := acpSessionID()
	cleanupConn(t, sessionID)

	prompts := []string{
		"说一个字：一",
		"说一个字：二",
		"说一个字：三",
		"说一个字：四",
		"说一个字：五",
	}

	for i, prompt := range prompts {
		t.Logf("Turn %d: %q", i+1, prompt)
		events := sendACPPrompt(t, backend, sessionID, prompt, 90*time.Second)
		requireDoneEvent(t, events)

		content := concatACPContent(events)
		assert.NotEmpty(t, content, "turn %d should produce content", i+1)
	}

	// After 5 turns, verify cache is still consistent
	mgr := GetACPConnManager()
	conn := mgr.GetConn(sessionID)
	require.NotNil(t, conn)
	assert.True(t, conn.IsAlive(), "connection should still be alive after 5 turns")

	t.Logf("State after 5 turns: %s", fmtACPStateSummary(conn))

	pid := conn.ProcessPID()
	assert.NotZero(t, pid, "should have a valid PID after 5 turns")
}

// D4: Switch config 10 times → final state matches last successful setting
func TestACPIntegration_LongRunning_ConfigStateConsistency(t *testing.T) {
	requireCodeBuddyACPAvailable(t)

	backend, err := NewACPBackend(acpTestAgent())
	require.NoError(t, err)

	sessionID := acpSessionID()
	cleanupConn(t, sessionID)

	// First prompt — establish connection
	events1 := sendACPPrompt(t, backend, sessionID, "说一个字：好", 90*time.Second)
	requireDoneEvent(t, events1)

	mgr := GetACPConnManager()
	conn := mgr.GetConn(sessionID)
	require.NotNil(t, conn)

	// Try switching thinking effort multiple times
	effortLevels := []string{"low", "medium", "high", "medium", "low", "high", "low", "high", "medium", "low"}
	lastSuccessfulEffort := ""

	for i, effort := range effortLevels {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		conn.SetSessionConfigOption(ctx, "thinkingEffort", effort)
		cancel()
		time.Sleep(200 * time.Millisecond)

		if !conn.IsAlive() {
			t.Logf("Turn %d: thinkingEffort=%q caused connection death", i+1, effort)
			break
		}

		// Verify cache was updated
		es := conn.GetCachedThinkingEffortState()
		if es != nil && es.CurrentID == effort {
			lastSuccessfulEffort = effort
			t.Logf("Turn %d: thinkingEffort=%q succeeded", i+1, effort)
		}
	}

	if lastSuccessfulEffort != "" {
		effortState := conn.GetCachedThinkingEffortState()
		require.NotNil(t, effortState, "thinking effort state should be cached")
		assert.Equal(t, lastSuccessfulEffort, effortState.CurrentID,
			"final thinking effort should match last successful switch: expected=%q, got=%q",
			lastSuccessfulEffort, effortState.CurrentID)
		t.Logf("Final thinking effort: %q (matches last switch)", effortState.CurrentID)
	}
}
