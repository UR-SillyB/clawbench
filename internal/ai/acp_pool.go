package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"clawbench/internal/model"
)

// ---------------------------------------------------------------------------
// configKilledConnectionError — typed error for set_config_option killing the connection
// ---------------------------------------------------------------------------

// configKilledConnectionError indicates that a SetSessionConfigOption call
// caused the agent process to crash or exit, killing the ACP connection.
// This is a retryable error — the connection is already marked dead and will
// be respawned on the next prompt attempt.
type configKilledConnectionError struct {
	configID string // "model", "thinkingEffort", or "mode"
	value    string // the value that caused the crash
	diag     crashDiagnostics
}

func (e *configKilledConnectionError) Error() string {
	s := "acp: set_config_option(" + e.configID + ") killed connection"
	if e.value != "" {
		s += " (value=" + e.value + ")"
	}
	if diagStr := e.diag.String(); diagStr != "" {
		s += diagStr
	}
	return s
}

// ConfigID returns the config ID that caused the crash (e.g., "model").
func (e *configKilledConnectionError) ConfigID() string { return e.configID }

// Value returns the config value that caused the crash.
func (e *configKilledConnectionError) Value() string { return e.value }

// errConfigKilledConnection creates a configKilledConnectionError for the given config ID.
func errConfigKilledConnection(configID, value string) error {
	return &configKilledConnectionError{configID: configID, value: value}
}

// isConfigKilledConnection reports whether the error indicates a set_config_option
// call killed the agent connection. These errors are retryable.
func isConfigKilledConnection(err error) bool {
	var e *configKilledConnectionError
	return errors.As(err, &e)
}

// ---------------------------------------------------------------------------
// ACPConnManager — singleton managing one ACP connection per ClawBench session
// ---------------------------------------------------------------------------

// ACPConnManager manages one ACP stdio connection per ClawBench session.
// Idle connections are reaped by a background sweep goroutine to prevent
// stale agent processes from consuming resources indefinitely.
type ACPConnManager struct {
	mu       sync.Mutex
	conns    map[string]*ACPConn // keyed by clawbenchSID
	stopSweep chan struct{}       // closed to stop the idle sweep goroutine

	// isSessionRunning is a callback that checks whether a session is
	// actively running. Set by the service layer to avoid circular imports.
	// If nil, idle sweep skips the running-check and closes all idle connections.
	isSessionRunning func(sessionID string) bool
}

const (
	// idleSweepInterval controls how often the background sweep runs.
	idleSweepInterval = 1 * time.Minute
	// idleConnTimeout is the maximum duration a connection can be idle
	// before it is closed and removed from the pool.
	idleConnTimeout = 5 * time.Minute
)

var (
	globalManager     *ACPConnManager
	globalManagerOnce sync.Once
)

// GetACPConnManager returns the singleton connection manager.
func GetACPConnManager() *ACPConnManager {
	globalManagerOnce.Do(func() {
		globalManager = &ACPConnManager{
			conns:     make(map[string]*ACPConn),
			stopSweep: make(chan struct{}),
		}
		go globalManager.idleSweep()
	})
	return globalManager
}

// StopAll closes all connections and stops the idle sweep goroutine.
// Called on server shutdown.
func (m *ACPConnManager) StopAll() {
	// Stop the idle sweep goroutine
	close(m.stopSweep)

	m.mu.Lock()
	for sid, conn := range m.conns {
		conn.close()
		delete(m.conns, sid)
	}
	m.mu.Unlock()
}

// idleSweep periodically closes connections that have been idle for longer
// than idleConnTimeout. This prevents stale agent processes from consuming
// resources indefinitely after sessions complete without explicit deletion.
// Connections with actively running sessions are skipped.
func (m *ACPConnManager) idleSweep() {
	ticker := time.NewTicker(idleSweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopSweep:
			return
		case <-ticker.C:
			m.sweepOnce()
		}
	}
}

// sweepOnce performs a single idle sweep pass.
func (m *ACPConnManager) sweepOnce() {
	var toClose []string

	m.mu.Lock()
	now := time.Now()
	for sid, conn := range m.conns {
		conn.mu.Lock()
		idle := now.Sub(conn.lastUsed)
		alive := conn.alive
		conn.mu.Unlock()

		if !alive {
			continue // already dead, will be respawned on next use
		}
		if idle < idleConnTimeout {
			continue // not idle enough yet
		}
		// Skip connections with actively running sessions
		if m.isSessionRunning != nil && m.isSessionRunning(sid) {
			continue
		}
		toClose = append(toClose, sid)
	}
	m.mu.Unlock()

	for _, sid := range toClose {
		m.mu.Lock()
		conn, ok := m.conns[sid]
		if ok {
			delete(m.conns, sid)
		}
		m.mu.Unlock()

		if ok {
			conn.mu.Lock()
			idle := time.Since(conn.lastUsed)
			conn.mu.Unlock()
			slog.Info("acp: idle sweep closing connection", "clawbench_sid", sid, "idle_duration", idle)
			conn.close()
		}
	}
}

// SetSessionRunningChecker sets the callback used by idle sweep to check
// whether a session is actively running. Must be called once during startup
// by the service layer (avoids circular import between ai and service packages).
func (m *ACPConnManager) SetSessionRunningChecker(fn func(sessionID string) bool) {
	m.isSessionRunning = fn
}

// GetOrCreateConn returns the ACPConn for a ClawBench session, creating one if needed.
// If the existing connection is dead, it respawns and tries to recover the session
// via ResumeSession. If recovery fails or there's no prior session, it creates a new one.
// Returns (conn, isNew, error) where isNew indicates whether a new ACP session was created.
func (m *ACPConnManager) GetOrCreateConn(ctx context.Context, agent *model.Agent, clawbenchSID, cwd string) (*ACPConn, bool, error) {
	m.mu.Lock()
	conn, ok := m.conns[clawbenchSID]
	if !ok {
		conn = newACPConn(agent, clawbenchSID)
		m.conns[clawbenchSID] = conn
	}
	m.mu.Unlock()

	isNew, err := conn.ensureAliveWithSession(ctx, cwd)
	if err != nil {
		return nil, false, err
	}
	return conn, isNew, nil
}

// GetConn returns the ACPConn for the given ClawBench session ID.
// Returns nil if no connection exists.
func (m *ACPConnManager) GetConn(clawbenchSID string) *ACPConn {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.conns[clawbenchSID]
}

// CancelTurn sends an ACP Cancel notification for the given ClawBench session.
// This tells the ACP agent to stop its current turn gracefully, which prevents
// zombie processes when the user cancels mid-stream. Safe to call even if no
// connection exists or the connection is dead.
func (m *ACPConnManager) CancelTurn(clawbenchSID string) {
	m.mu.Lock()
	conn := m.conns[clawbenchSID]
	m.mu.Unlock()

	if conn != nil {
		conn.CancelTurn(context.Background())
	}
}

// CloseConn closes and removes the connection for the given ClawBench session ID.
func (m *ACPConnManager) CloseConn(clawbenchSID string) {
	m.mu.Lock()
	conn, ok := m.conns[clawbenchSID]
	if ok {
		delete(m.conns, clawbenchSID)
	}
	m.mu.Unlock()

	if ok {
		conn.close()
	}
}

// GetCachedStateByClawbenchSID returns the cached state for the connection
// owned by the given ClawBench session ID.
func (m *ACPConnManager) GetCachedStateByClawbenchSID(clawbenchSID string) (mode *ModeState, config *ConfigOptionState, effort *ThinkingEffortState, cmds []AvailableCommandInfo, modelList *ModelListState, plan *PlanState) {
	m.mu.Lock()
	conn := m.conns[clawbenchSID]
	m.mu.Unlock()

	if conn == nil {
		return nil, nil, nil, nil, nil, nil
	}

	conn.mu.Lock()
	defer conn.mu.Unlock()
	if conn.client != nil {
		cmds = conn.client.GetCommandsAsInfo()
	}
	return conn.cachedModeState, conn.cachedConfigState, conn.cachedThinkingEffortState, cmds, conn.cachedModelListState, conn.cachedPlanState
}

// GetCachedStateByAgentID returns the cached state for any connection
// belonging to the given agent. Returns the first match found.
// Used for pre-fetching state before the first message (no session yet).
func (m *ACPConnManager) GetCachedStateByAgentID(agentID string) (mode *ModeState, config *ConfigOptionState, effort *ThinkingEffortState, modelList *ModelListState, plan *PlanState) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for key, conn := range m.conns {
		conn.mu.Lock()
		// Match by agent.ID, or by map key if agent is nil (test entries)
		matched := (conn.agent != nil && conn.agent.ID == agentID) || key == agentID
		if matched {
			mode = conn.cachedModeState
			config = conn.cachedConfigState
			effort = conn.cachedThinkingEffortState
			modelList = conn.cachedModelListState
			plan = conn.cachedPlanState
			conn.mu.Unlock()
			return
		}
		conn.mu.Unlock()
	}
	return nil, nil, nil, nil, nil
}

// GetCommandsByAgentID returns the cached slash commands for any connection
// belonging to the given agent.
func (m *ACPConnManager) GetCommandsByAgentID(agentID string) []AvailableCommandInfo {
	m.mu.Lock()
	defer m.mu.Unlock()

	for key, conn := range m.conns {
		conn.mu.Lock()
		matched := (conn.agent != nil && conn.agent.ID == agentID) || key == agentID
		if matched {
			client := conn.client
			conn.mu.Unlock()
			if client != nil {
				return client.GetCommandsAsInfo()
			}
			return nil
		}
		conn.mu.Unlock()
	}
	return nil
}

// GetClientByAgentID returns the ClawBenchACPClient for any connection
// belonging to the given agent. Returns nil if not found.
func (m *ACPConnManager) GetClientByAgentID(agentID string) *ClawBenchACPClient {
	m.mu.Lock()
	defer m.mu.Unlock()

	for key, conn := range m.conns {
		conn.mu.Lock()
		matched := (conn.agent != nil && conn.agent.ID == agentID) || key == agentID
		if matched {
			client := conn.client
			conn.mu.Unlock()
			return client
		}
		conn.mu.Unlock()
	}
	return nil
}

// SetConnForTest injects a connection for testing. Production code must not use this.
func (m *ACPConnManager) SetConnForTest(clawbenchSID string, conn *ACPConn) {
	m.mu.Lock()
	m.conns[clawbenchSID] = conn
	m.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Backward-compatible aliases — temporary until all callers are migrated
// ---------------------------------------------------------------------------

// GetACPConnectionPool returns the singleton connection manager.
// Deprecated: use GetACPConnManager() instead.
func GetACPConnectionPool() *ACPConnManager {
	return GetACPConnManager()
}

// ACPConnectionPool is an alias for ACPConnManager for backward compatibility.
// Deprecated: use ACPConnManager instead.
type ACPConnectionPool = ACPConnManager

// ACPConnEntry is an alias for ACPConn for backward compatibility.
// Deprecated: use ACPConn instead.
type ACPConnEntry = ACPConn

// ---------------------------------------------------------------------------
// ACPConn — one ACP stdio connection for one ClawBench session
// ---------------------------------------------------------------------------

// ACPConn represents a dedicated ACP stdio connection for one ClawBench session.
// One session = one agent process = one ACP session. No sharing, no pooling.
type ACPConn struct {
	agent       *model.Agent
	clawbenchSID string
	mu          sync.Mutex

	cmd    *exec.Cmd
	conn   *acp.ClientSideConnection
	client *ClawBenchACPClient

	// acpSID is the ACP session ID. Populated from DB (ResumeSession) or
	// from NewSession response. Empty means no session yet.
	acpSID string

	// lastNewSessionResp stores the NewSessionResponse from the most recent
	// session/new so ExecuteStream can extract mode/config state. Cleared after reading.
	lastNewSessionResp *acp.NewSessionResponse

	// lastResumeSessionResp stores the ResumeSessionResponse from the most recent
	// session/resume so ExecuteStream can extract mode/config state. Cleared after reading.
	lastResumeSessionResp *acp.ResumeSessionResponse

	// liveness
	lastUsed time.Time
	alive    bool
	startedAt time.Time // when the agent process was spawned

	// cmdWaitOnce ensures cmd.Wait() is called exactly once; the result is
	// cached in cmdWaitState for subsequent readers (e.g. collectCrashDiagnostics
	// and spawnLocked both need the exit state).
	cmdWaitOnce  sync.Once
	cmdWaitState *os.ProcessState

	// cached state — populated from NewSession/ResumeSession responses and
	// re-emitted for every ExecuteStream call so the frontend always has
	// up-to-date mode/command state, even after page refreshes or SSE reconnects.
	cachedModeState           *ModeState
	cachedConfigState         *ConfigOptionState
	cachedThinkingEffortState *ThinkingEffortState
	cachedModelListState      *ModelListState
	cachedPlanState           *PlanState

	// lastSetConfig tracks the last values successfully sent to the agent via
	// setSessionConfigOption. Used to avoid re-sending unchanged values that
	// may trigger expensive agent-side restarts (e.g., Claude bridge setModel).
	lastSetConfigMu sync.Mutex
	lastSetModel    string
	lastSetEffort   string
	lastSetMode     string

	// unsupportedConfigs tracks config IDs that the agent reported as unknown
	// (e.g., CodeBuddy doesn't support "thinkingEffort"). Once detected, we
	// skip sending that config to avoid flooding the agent with errors on every
	// prompt. Cleared on respawn — the new process might support it after an update.
	unsupportedConfigs map[string]bool

	// persistDebounce timer for batching ACP state DB writes
	persistTimer *time.Timer
	persistMu    sync.Mutex // separate mutex to avoid deadlock with mu
}

// getExternalSessionID is the global function for looking up the ACP session ID
// from the database. Set by the application startup via SetExternalSessionIDGetter.
// Uses a function variable to avoid import cycles between internal/ai and internal/service.
var getExternalSessionID = func(clawbenchSID string) string {
	return "" // no-op until SetExternalSessionIDGetter is called
}

// SetExternalSessionIDGetter sets the function used to look up the ACP session ID
// from the database. Must be called once during application startup, after service.InitDB().
func SetExternalSessionIDGetter(fn func(clawbenchSID string) string) {
	getExternalSessionID = fn
}

// persistAgentACPStateToDB is the global function for persisting ACP state to the database.
// Set by the application startup via SetACPStatePersister.
var persistAgentACPStateToDB = func(agentID, modeState, commands, thinkingState, modelListState string) error {
	return nil // no-op until SetACPStatePersister is called
}

// SetACPStatePersister sets the function used to persist ACP cached state
// to the database. Must be called once during application startup.
func SetACPStatePersister(fn func(agentID, modeState, commands, thinkingState, modelListState string) error) {
	persistAgentACPStateToDB = fn
}

// newACPConn creates a new (uninitialized) ACPConn.
func newACPConn(agent *model.Agent, clawbenchSID string) *ACPConn {
	return &ACPConn{
		agent:        agent,
		clawbenchSID: clawbenchSID,
		lastUsed:     time.Now(),
		alive:        false,
	}
}

// ensureAliveWithSession ensures the connection is alive and has a valid ACP session.
// If the process is dead, it respawns and tries ResumeSession recovery, falling back to NewSession.
// Returns isNew=true if a new ACP session was created, false if reusing or recovered.
func (c *ACPConn) ensureAliveWithSession(ctx context.Context, cwd string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// If alive and already has a session, reuse
	if c.alive && c.isAliveLocked() && c.acpSID != "" {
		slog.Debug("acp conn: reusing existing connection", "clawbench_sid", c.clawbenchSID, "acp_sid", c.acpSID)
		c.lastUsed = time.Now()
		return false, nil
	}

	// Snapshot cached config state before spawn, so we can re-apply it after
	// ResumeSession. When an agent process crashes and is respawned, the
	// ResumeSession response reports the agent's DEFAULT config values (not
	// the previously-set ones), which would overwrite our cache and cause
	// "amnesia" — the user's mode/model/thinking selections would be lost.
	prevMode := ""
	if c.cachedModeState != nil {
		prevMode = c.cachedModeState.CurrentModeID
	}
	prevModel := ""
	if c.cachedModelListState != nil {
		prevModel = c.cachedModelListState.CurrentModelID
	}
	prevEffort := ""
	if c.cachedThinkingEffortState != nil {
		prevEffort = c.cachedThinkingEffortState.CurrentID
	}

	// Need to spawn or respawn
	if err := c.spawnLocked(ctx); err != nil {
		return false, err
	}

	// Try to recover session via ResumeSession (no history replay — ClawBench has its own DB)
	acpSID := getExternalSessionID(c.clawbenchSID)
	if acpSID != "" {
		resumeResp, err := c.conn.ResumeSession(ctx, acp.ResumeSessionRequest{
			SessionId: acp.SessionId(acpSID),
			Cwd:       cwd,
			McpServers: []acp.McpServer{},
		})
		if err != nil {
			slog.Error("acp conn: ResumeSession failed",
				"clawbench_sid", c.clawbenchSID,
				"acp_sid", acpSID,
				"error", err)
			return false, fmt.Errorf("acp: ResumeSession failed for session %s: %w", acpSID, err)
		}
		c.acpSID = acpSID
		c.lastResumeSessionResp = &resumeResp
		c.lastUsed = time.Now()
		slog.Info("acp conn: recovered session via ResumeSession", "clawbench_sid", c.clawbenchSID, "acp_sid", acpSID)

		// Re-apply previously cached config values to the respawned process.
		// ResumeSession reports the agent's defaults, not the user's selections.
		// Since spawnLocked already called resetLastSetConfig(), shouldSetConfig
		// will return true for these values — they won't be dedup-skipped.
		if prevMode != "" && c.alive && c.isAliveLocked() {
			c.mu.Unlock()
			c.setSessionConfigOption(ctx, acpSID, "mode", prevMode)
			c.mu.Lock()
			if c.alive {
				c.markConfigSet("mode", prevMode)
				slog.Info("acp conn: re-applied mode after resume", "mode", prevMode, "clawbench_sid", c.clawbenchSID)
			}
		}
		if prevModel != "" && c.alive && c.isAliveLocked() {
			c.mu.Unlock()
			c.setSessionConfigOption(ctx, acpSID, "model", prevModel)
			c.mu.Lock()
			if c.alive {
				c.markConfigSet("model", prevModel)
				slog.Info("acp conn: re-applied model after resume", "model", prevModel, "clawbench_sid", c.clawbenchSID)
			}
		}
		if prevEffort != "" && c.alive && c.isAliveLocked() {
			c.mu.Unlock()
			c.setSessionConfigOption(ctx, acpSID, "thinkingEffort", prevEffort)
			c.mu.Lock()
			if c.alive {
				c.markConfigSet("thinkingEffort", prevEffort)
				slog.Info("acp conn: re-applied thinking effort after resume", "effort", prevEffort, "clawbench_sid", c.clawbenchSID)
			}
		}

		return false, nil // not new — recovered
	}

	// No prior session — first message ever, create new session
	sessResp, err := c.conn.NewSession(ctx, acp.NewSessionRequest{
		Cwd:        cwd,
		McpServers: []acp.McpServer{},
	})
	if err != nil {
		return false, fmt.Errorf("acp: session/new: %w", err)
	}

	c.acpSID = string(sessResp.SessionId)
	c.lastNewSessionResp = &sessResp
	c.lastUsed = time.Now()
	slog.Info("acp conn: created new session", "clawbench_sid", c.clawbenchSID, "acp_sid", c.acpSID)
	return true, nil
}

// isAliveLocked checks if the connection is still alive (must hold c.mu).
func (c *ACPConn) isAliveLocked() bool {
	if c.conn == nil {
		return false
	}
	select {
	case <-c.conn.Done():
		return false
	default:
		return true
	}
}

// spawnLocked spawns the agent process and initializes the connection (must hold c.mu).
func (c *ACPConn) spawnLocked(ctx context.Context) error {
	// Kill any existing process first
	if c.cmd != nil && c.cmd.Process != nil {
		// Send ACP Cancel to let the agent stop gracefully before killing
		if c.conn != nil && c.acpSID != "" {
			cancelCtx, cancelCancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = c.conn.Cancel(cancelCtx, acp.CancelNotification{SessionId: acp.SessionId(c.acpSID)})
			cancelCancel()
		}
		_ = c.cmd.Process.Kill()
		// Release the mutex while waiting for the old process to exit,
		// since cmd.Wait() can block if the process is unresponsive.
		// Another goroutine calling GetOrCreateConn during this window
		// will find alive=false and attempt its own spawn — but that's
		// safe because we clear c.cmd below after re-acquiring the lock.
		oldCmd := c.cmd
		c.mu.Unlock()
		_ = oldCmd.Wait()
		c.mu.Lock()
		// Clear the old cmd reference only if it hasn't been replaced
		// by another concurrent spawn (unlikely but defensive).
		if c.cmd == oldCmd {
			c.cmd = nil
		}
	}

	// Reset cached config values — the new process doesn't know about prior settings.
	c.resetLastSetConfig()

	cmdParts := strings.Fields(c.agent.AcpCommand)
	if len(cmdParts) == 0 {
		return fmt.Errorf("acp: no acp_command configured for agent %q", c.agent.ID)
	}

	cmdName := cmdParts[0]
	cmdArgs := cmdParts[1:]

	cmd := exec.CommandContext(context.Background(), cmdName, cmdArgs...)
	cmd.Dir = "" // cwd is per-session, set during NewSession/ResumeSession
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, OrphanChildEnvVar)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("acp: stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("acp: stdout pipe: %w", err)
	}
	cmd.Stderr = &strings.Builder{}

	slog.Info("acp conn: spawning agent process", "agent_id", c.agent.ID, "clawbench_sid", c.clawbenchSID, "command", cmdName, "args", cmdArgs)

	if startErr := cmd.Start(); startErr != nil {
		return fmt.Errorf("acp: start: %w", startErr)
	}

	client := NewClawBenchACPClient()
	client.connRef = c // back-reference for cache updates
	conn := acp.NewClientSideConnection(client, stdinPipe, stdoutPipe)
	conn.SetLogger(slog.Default())

	initResp, err := conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientCapabilities: acp.ClientCapabilities{
			Fs: acp.FileSystemCapabilities{
				ReadTextFile:  true,
				WriteTextFile: true,
			},
			Terminal: true,
		},
		ClientInfo: &acp.Implementation{
			Name:    "clawbench",
			Version: "1.0.0",
		},
	})
	if err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("acp: initialize: %w", err)
	}

	slog.Info("acp conn: agent initialized", "agent_id", c.agent.ID, "protocol_version", initResp.ProtocolVersion)

	c.cmd = cmd
	c.conn = conn
	c.client = client
	c.acpSID = "" // cleared on respawn — will be set by ensureAliveWithSession
	c.alive = true
	c.lastUsed = time.Now()
	c.startedAt = time.Now()
	c.cmdWaitOnce = sync.Once{}
	c.cmdWaitState = nil

	go c.watchProcessDeath()
	return nil
}

// watchProcessDeath monitors the ACP connection and marks it as dead
// when the agent process exits or the connection drops.
// Collects crash diagnostics (exit code, signal, stderr, uptime) to help
// diagnose why the agent process died.
func (c *ACPConn) watchProcessDeath() {
	if c.conn == nil {
		return
	}
	<-c.conn.Done()

	c.mu.Lock()
	if c.alive {
		c.alive = false
	}
	agentID := ""
	if c.agent != nil {
		agentID = c.agent.ID
	}
	c.mu.Unlock()

	// Collect crash diagnostics outside the lock
	diag := c.collectCrashDiagnostics()

	// Normal exit (exit_code=0, no signal) means the session completed and
	// the connection was closed by CloseConn — not an error.
	if diag.ExitCode == 0 && diag.Signal == "" {
		slog.Info("acp conn: agent process exited",
			"agent_id", agentID,
			"clawbench_sid", c.clawbenchSID,
			"exit_code", diag.ExitCode,
			"uptime", diag.Uptime.Round(time.Second),
		)
	} else {
		slog.Error("acp conn: agent process died",
			"agent_id", agentID,
			"clawbench_sid", c.clawbenchSID,
			"exit_code", diag.ExitCode,
			"signal", diag.Signal,
			"uptime", diag.Uptime.Round(time.Second),
			"stderr_tail", diag.StderrTail,
		)
	}

	c.resetLastSetConfig()
}

// GetAndClearNewSessionResp returns the last NewSessionResponse and clears it.
// Used by ExecuteStream to emit session_capture and mode_update events for new sessions.
func (c *ACPConn) GetAndClearNewSessionResp() *acp.NewSessionResponse {
	c.mu.Lock()
	defer c.mu.Unlock()
	resp := c.lastNewSessionResp
	c.lastNewSessionResp = nil
	return resp
}

// GetAndClearResumeSessionResp returns the last ResumeSessionResponse and clears it.
// Used by ExecuteStream to update mode/config cache for recovered sessions.
func (c *ACPConn) GetAndClearResumeSessionResp() *acp.ResumeSessionResponse {
	c.mu.Lock()
	defer c.mu.Unlock()
	resp := c.lastResumeSessionResp
	c.lastResumeSessionResp = nil
	return resp
}

// GetAndClearSessionResp returns the last NewSessionResponse and clears it.
// Deprecated: use GetAndClearNewSessionResp.
func (c *ACPConn) GetAndClearSessionResp() *acp.NewSessionResponse {
	return c.GetAndClearNewSessionResp()
}

// AcpSID returns the ACP session ID for this connection.
func (c *ACPConn) AcpSID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.acpSID
}

// Prompt sends a prompt on the ACP session and forwards events to streamCh.
func (c *ACPConn) Prompt(ctx context.Context, prompt []acp.ContentBlock, streamCh chan<- StreamEvent, req ChatRequest) error {
	// Clear stale plan state from the previous turn — a new prompt starts
	// a fresh execution cycle and the old plan entries are no longer relevant.
	c.mu.Lock()
	c.cachedPlanState = nil
	c.mu.Unlock()

	c.mu.Lock()
	client := c.client
	conn := c.conn
	acpSID := c.acpSID
	c.lastUsed = time.Now()
	c.mu.Unlock()

	if conn == nil || acpSID == "" {
		return fmt.Errorf("acp: connection not initialized")
	}

	// Register the stream channel so SessionUpdate callbacks are forwarded
	if client != nil {
		client.RegisterSession(acpSID, streamCh)
		defer client.UnregisterSession(acpSID)
	}

	// Set model if configured AND changed since last set (non-fatal).
	// Avoid re-sending unchanged values that may trigger agent-side restarts.
	// If the call kills the connection (agent crashed), abort early —
	// the retry path in ACPBackend.ExecuteStream will handle respawn.
	if req.Model != "" && c.shouldSetConfig("model", req.Model) {
		c.setSessionConfigOption(ctx, acpSID, "model", req.Model)
		if !c.IsAlive() {
			diag := c.collectCrashDiagnostics()
			slog.Error("acp conn: set_config_option(model) killed connection",
				"clawbench_sid", c.clawbenchSID, "acp_sid", acpSID, "model", req.Model,
				"exit_code", diag.ExitCode, "stderr_tail", diag.StderrTail)
			err := errConfigKilledConnection("model", req.Model)
			err.(*configKilledConnectionError).diag = diag
			return err
		}
		c.markConfigSet("model", req.Model)
	}

	// Set thinking effort if configured AND changed since last set (non-fatal).
	if req.ThinkingEffort != "" && c.shouldSetConfig("thinkingEffort", req.ThinkingEffort) {
		c.setSessionConfigOption(ctx, acpSID, "thinkingEffort", req.ThinkingEffort)
		if !c.IsAlive() {
			diag := c.collectCrashDiagnostics()
			slog.Error("acp conn: set_config_option(thinkingEffort) killed connection",
				"clawbench_sid", c.clawbenchSID, "acp_sid", acpSID, "thinking_effort", req.ThinkingEffort,
				"exit_code", diag.ExitCode, "stderr_tail", diag.StderrTail)
			err := errConfigKilledConnection("thinkingEffort", req.ThinkingEffort)
			err.(*configKilledConnectionError).diag = diag
			return err
		}
		c.markConfigSet("thinkingEffort", req.ThinkingEffort)
	}

	// Set mode if configured AND changed since last set (non-fatal).
	if req.Mode != "" && c.shouldSetConfig("mode", req.Mode) {
		c.setSessionConfigOption(ctx, acpSID, "mode", req.Mode)
		if !c.IsAlive() {
			diag := c.collectCrashDiagnostics()
			slog.Error("acp conn: set_config_option(mode) killed connection",
				"clawbench_sid", c.clawbenchSID, "acp_sid", acpSID, "mode", req.Mode,
				"exit_code", diag.ExitCode, "stderr_tail", diag.StderrTail)
			err := errConfigKilledConnection("mode", req.Mode)
			err.(*configKilledConnectionError).diag = diag
			return err
		}
		c.markConfigSet("mode", req.Mode)
	}

	// Send prompt
	_, err := conn.Prompt(ctx, acp.PromptRequest{
		SessionId: acp.SessionId(acpSID),
		Prompt:    prompt,
	})
	if err != nil {
		if ctx.Err() != nil {
			slog.Info("acp conn: prompt cancelled", "clawbench_sid", c.clawbenchSID, "acp_sid", acpSID)
			// Mark connection as dead so next prompt triggers respawn + ResumeSession
			c.mu.Lock()
			c.alive = false
			c.mu.Unlock()
			return ctx.Err()
		}

		// Peer disconnected — collect crash diagnostics (stderr, exit code)
		// and mark the connection as dead for respawn on retry.
		diag := c.collectCrashDiagnostics()
		c.mu.Lock()
		c.alive = false
		c.mu.Unlock()

		slog.Error("acp conn: prompt failed (peer disconnected)",
			"clawbench_sid", c.clawbenchSID, "acp_sid", acpSID,
			"exit_code", diag.ExitCode, "stderr_tail", diag.StderrTail)

		return fmt.Errorf("acp: prompt: %w%s", err, diag.String())
	}

	return nil
}

// crashDiagnostics holds crash info collected after an agent process exits unexpectedly.
type crashDiagnostics struct {
	ExitCode    int
	StderrTail  string // last ~2KB of stderr
	WasAlive    bool   // was conn.Done() already closed?
	Uptime      time.Duration
	Signal      string // decoded signal name (e.g., "SIGKILL", "SIGSEGV") if killed by signal
}

func (d crashDiagnostics) String() string {
	parts := make([]string, 0, 4)
	if d.ExitCode != 0 {
		exitStr := fmt.Sprintf("exit_code=%d", d.ExitCode)
		if sig := d.Signal; sig != "" {
			exitStr += " (" + sig + ")"
		} else if decoded := decodeExitCode(d.ExitCode); decoded != "" {
			exitStr += " (" + decoded + ")"
		}
		parts = append(parts, exitStr)
	}
	if d.Uptime > 0 {
		parts = append(parts, fmt.Sprintf("uptime=%s", d.Uptime.Round(time.Second)))
	}
	if d.StderrTail != "" {
		parts = append(parts, fmt.Sprintf("stderr: %s", d.StderrTail))
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// decodeExitCode maps common exit codes to human-readable descriptions.
// On Unix, exit codes > 128 indicate the process was killed by signal (128 + signal number).
func decodeExitCode(code int) string {
	switch code {
	case 1:
		return "general error"
	case 126:
		return "permission denied / not executable"
	case 127:
		return "command not found"
	case 128:
		return "invalid exit argument"
	case 129:
		return "SIGHUP"
	case 130:
		return "SIGINT (Ctrl+C)"
	case 137:
		return "SIGKILL (possible OOM killer)"
	case 139:
		return "SIGSEGV (segmentation fault)"
	case 141:
		return "SIGPIPE (broken pipe)"
	case 143:
		return "SIGTERM"
	default:
		if code > 128 {
			sigNum := code - 128
			return fmt.Sprintf("signal %d", sigNum)
		}
		return ""
	}
}

// collectCrashDiagnostics gathers exit code and stderr from the crashed agent process.
// Must be called after Prompt() returns a peer-disconnect error.
func (c *ACPConn) collectCrashDiagnostics() crashDiagnostics {
	var diag crashDiagnostics

	c.mu.Lock()
	cmd := c.cmd
	conn := c.conn
	startedAt := c.startedAt
	c.mu.Unlock()

	// Uptime
	if !startedAt.IsZero() {
		diag.Uptime = time.Since(startedAt)
	}

	// Check if the connection's Done channel is closed (confirming peer disconnect)
	if conn != nil {
		select {
		case <-conn.Done():
			diag.WasAlive = false
		default:
			diag.WasAlive = true
		}
	}

	if cmd == nil || cmd.Process == nil {
		return diag
	}

	// Use cmdWaitOnce to safely call Wait() exactly once, caching the result.
	// This avoids a race between collectCrashDiagnostics and spawnLocked both
	// calling Wait() on the same process.
	c.cmdWaitOnce.Do(func() {
		if state, err := cmd.Process.Wait(); err == nil {
			c.cmdWaitState = state
		}
	})

	if c.cmdWaitState != nil {
		diag.ExitCode = c.cmdWaitState.ExitCode()
		// Check if the process was killed by a signal (Unix-specific)
		if ws, ok := c.cmdWaitState.Sys().(syscall.WaitStatus); ok {
			if ws.Signaled() {
				diag.Signal = ws.Signal().String()
			}
		}
	}

	// Extract stderr from the strings.Builder
	c.mu.Lock()
	if c.cmd != nil {
		if sb, ok := c.cmd.Stderr.(*strings.Builder); ok {
			stderr := sb.String()
			if len(stderr) > 2048 {
				stderr = "..." + stderr[len(stderr)-2048:]
			}
			diag.StderrTail = stderr
		}
	}
	c.mu.Unlock()

	return diag
}

// CancelTurn cancels the current in-progress prompt turn.
func (c *ACPConn) CancelTurn(ctx context.Context) {
	c.mu.Lock()
	conn := c.conn
	acpSID := c.acpSID
	c.mu.Unlock()

	if conn != nil && acpSID != "" {
		_ = conn.Cancel(ctx, acp.CancelNotification{SessionId: acp.SessionId(acpSID)})
	}
}

// SetSessionConfigOption sets a config option for this session.
// Also updates cached state so re-emitted SSE events reflect the new value.
func (c *ACPConn) SetSessionConfigOption(ctx context.Context, configID, value string) {
	c.mu.Lock()
	acpSID := c.acpSID
	c.mu.Unlock()

	if acpSID == "" {
		slog.Debug("acp conn: SetSessionConfigOption: no session", "clawbench_sid", c.clawbenchSID)
		return
	}

	c.setSessionConfigOption(ctx, acpSID, configID, value)

	switch configID {
	case "mode":
		c.UpdateCachedCurrentMode(value)
	case "thinking_effort", "thought_level":
		c.UpdateCachedCurrentThinkingEffort(value)
	case "model":
		c.UpdateCachedCurrentModel(value)
	}
}

// setSessionConfigOption sets a config option. Errors are logged but not fatal.
func (c *ACPConn) setSessionConfigOption(ctx context.Context, acpSessionID, configID, value string) {
	c.mu.Lock()
	conn := c.conn
	alive := c.alive && c.isAliveLocked()
	c.mu.Unlock()

	if conn == nil || !alive {
		slog.Debug("acp conn: skipping set_config_option on dead connection", "config_id", configID, "value", value)
		return
	}

	slog.Info("acp conn: sending set_config_option", "config_id", configID, "value", value, "clawbench_sid", c.clawbenchSID, "acp_sid", acpSessionID)

	_, err := conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{
		ValueId: &acp.SetSessionConfigOptionValueId{
			SessionId: acp.SessionId(acpSessionID),
			ConfigId:  acp.SessionConfigId(configID),
			Value:     acp.SessionConfigValueId(value),
		},
	})
	if err != nil {
		slog.Warn("acp conn: set_config_option failed", "config_id", configID, "value", value, "error", err)
		// If the error indicates the agent doesn't know this config option,
		// mark it as unsupported so we don't retry on subsequent prompts.
		if isUnknownConfigOption(err) {
			c.lastSetConfigMu.Lock()
			if c.unsupportedConfigs == nil {
				c.unsupportedConfigs = make(map[string]bool)
			}
			c.unsupportedConfigs[configID] = true
			c.lastSetConfigMu.Unlock()
			slog.Info("acp conn: marking config as unsupported by agent", "config_id", configID, "value", value)
		}
		// If the error indicates the peer died, mark the connection as dead
		// so the next Prompt() triggers respawn + ResumeSession.
		if isACPPeerDisconnected(err) {
			c.mu.Lock()
			c.alive = false
			c.mu.Unlock()
			slog.Info("acp conn: set_config_option detected peer disconnect, marking dead", "config_id", configID, "value", value)
		}
	} else {
		slog.Info("acp conn: set_config_option completed", "config_id", configID, "value", value)
	}
}

// IsAlive returns whether the connection is currently alive.
func (c *ACPConn) IsAlive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.alive && c.isAliveLocked()
}

// GetClient returns the ClawBenchACPClient for this connection.
func (c *ACPConn) GetClient() *ClawBenchACPClient {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.client
}

// close kills the agent process and marks the connection as dead.
func (c *ACPConn) close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}
	c.cmd = nil
	c.conn = nil
	c.client = nil
	c.alive = false
	c.acpSID = ""
}

// Close kills the agent process and marks the connection as dead.
// Public alias for close().
func (c *ACPConn) Close() {
	c.close()
}

// ProcessPID returns the PID of the agent subprocess, or 0 if none.
func (c *ACPConn) ProcessPID() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd != nil && c.cmd.Process != nil {
		return c.cmd.Process.Pid
	}
	return 0
}

// KillProcessForTest kills the agent subprocess for integration testing.
// Returns an error if no process is running.
func (c *ACPConn) KillProcessForTest() error {
	c.mu.Lock()
	if c.cmd == nil || c.cmd.Process == nil {
		c.mu.Unlock()
		return fmt.Errorf("acp: no process to kill")
	}
	p := c.cmd.Process
	c.mu.Unlock()
	return p.Kill()
}

// IsConfigUnsupported reports whether the agent has rejected a config ID
// as unknown (e.g., CodeBuddy doesn't support "thinkingEffort").
func (c *ACPConn) IsConfigUnsupported(configID string) bool {
	c.lastSetConfigMu.Lock()
	defer c.lastSetConfigMu.Unlock()
	return c.unsupportedConfigs != nil && c.unsupportedConfigs[configID]
}

// ---------------------------------------------------------------------------
// Cached state accessors
// ---------------------------------------------------------------------------

func (c *ACPConn) GetCachedModeState() *ModeState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cachedModeState
}

func (c *ACPConn) GetCachedConfigState() *ConfigOptionState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cachedConfigState
}

func (c *ACPConn) GetCachedThinkingEffortState() *ThinkingEffortState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cachedThinkingEffortState
}

func (c *ACPConn) GetCachedModelListState() *ModelListState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cachedModelListState
}

// SetCachedPlanState caches the plan state from a plan_update event.
// Plan state is transient — not persisted to DB, not debounced.
func (c *ACPConn) SetCachedPlanState(state *PlanState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cachedPlanState = state
	// No debouncePersistACPState — plan is transient, not persisted to DB
}

// GetCachedPlanState returns the cached plan state.
// Returns nil if no plan state has been cached yet.
func (c *ACPConn) GetCachedPlanState() *PlanState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cachedPlanState
}

// shouldSetConfig returns true if the config value has changed since the last
// successful set AND the config is not marked as unsupported by the agent.
func (c *ACPConn) shouldSetConfig(configID, value string) bool {
	c.lastSetConfigMu.Lock()
	defer c.lastSetConfigMu.Unlock()
	// Skip if the agent previously reported this config as unknown
	if c.unsupportedConfigs != nil && c.unsupportedConfigs[configID] {
		return false
	}
	switch configID {
	case "model":
		return c.lastSetModel != value
	case "thinkingEffort":
		return c.lastSetEffort != value
	case "mode":
		return c.lastSetMode != value
	}
	return true
}

// markConfigSet records that a config value was successfully sent.
func (c *ACPConn) markConfigSet(configID, value string) {
	c.lastSetConfigMu.Lock()
	defer c.lastSetConfigMu.Unlock()
	switch configID {
	case "model":
		c.lastSetModel = value
	case "thinkingEffort":
		c.lastSetEffort = value
	case "mode":
		c.lastSetMode = value
	}
}

// resetLastSetConfig clears cached config values (called on respawn).
// Also clears unsupported config tracking — the new process might support
// previously-unsupported options after an update.
func (c *ACPConn) resetLastSetConfig() {
	c.lastSetConfigMu.Lock()
	defer c.lastSetConfigMu.Unlock()
	c.lastSetModel = ""
	c.lastSetEffort = ""
	c.lastSetMode = ""
	c.unsupportedConfigs = nil
}

func (c *ACPConn) SetCachedModeState(state *ModeState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cachedModeState = state
	c.debouncePersistACPState()
}

func (c *ACPConn) SetCachedConfigState(state *ConfigOptionState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cachedConfigState = state
	// ACP v2 agents (e.g., OpenCode) expose modes via ConfigOptions instead of
	// the legacy Modes field. If cachedModeState is nil (no v1 Modes in response),
	// derive it from the config so REST APIs and DB persistence can populate mode
	// chips without requiring SSE events.
	if c.cachedModeState == nil {
		if derived := modeStateFromConfigState(state); derived != nil {
			c.cachedModeState = derived
		}
	}
	c.debouncePersistACPState()
}

func (c *ACPConn) SetCachedThinkingEffortState(state *ThinkingEffortState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cachedThinkingEffortState = state
	c.debouncePersistACPState()
}

func (c *ACPConn) SetCachedModelListState(state *ModelListState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cachedModelListState = state
	c.debouncePersistACPState()
}

func (c *ACPConn) UpdateCachedCurrentModel(modelID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cachedModelListState != nil {
		c.cachedModelListState.CurrentModelID = modelID
	}
}

func (c *ACPConn) UpdateCachedCurrentMode(modeID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cachedModeState != nil {
		c.cachedModeState.CurrentModeID = modeID
	}
	if c.cachedConfigState != nil {
		c.cachedConfigState.CurrentID = modeID
	}
}

// HasNewAvailableModes returns true if the given modes list contains mode IDs
// not present in the cached available modes. Used to diff-check whether an ACP
// agent's ConfigOptionUpdate should be forwarded to the frontend.
func (c *ACPConn) HasNewAvailableModes(newModes []ModeDef) bool {
	c.mu.Lock()
	existing := c.cachedModeState
	c.mu.Unlock()
	if existing == nil || len(existing.AvailableModes) == 0 {
		return len(newModes) > 0
	}
	seen := make(map[string]struct{}, len(existing.AvailableModes))
	for _, m := range existing.AvailableModes {
		seen[m.ID] = struct{}{}
	}
	for _, m := range newModes {
		if _, ok := seen[m.ID]; !ok {
			return true
		}
	}
	return false
}

// HasNewAvailableThinkingEfforts returns true if the given levels list contains
// IDs not present in the cached available levels.
func (c *ACPConn) HasNewAvailableThinkingEfforts(newLevels []ThinkingEffortDef) bool {
	c.mu.Lock()
	existing := c.cachedThinkingEffortState
	c.mu.Unlock()
	if existing == nil || len(existing.AvailableLevels) == 0 {
		return len(newLevels) > 0
	}
	seen := make(map[string]struct{}, len(existing.AvailableLevels))
	for _, l := range existing.AvailableLevels {
		seen[l.ID] = struct{}{}
	}
	for _, l := range newLevels {
		if _, ok := seen[l.ID]; !ok {
			return true
		}
	}
	return false
}

// HasNewAvailableModels returns true if the given models list contains
// IDs not present in the cached available models.
func (c *ACPConn) HasNewAvailableModels(newModels []model.AgentModel) bool {
	c.mu.Lock()
	existing := c.cachedModelListState
	c.mu.Unlock()
	if existing == nil || len(existing.Models) == 0 {
		return len(newModels) > 0
	}
	seen := make(map[string]struct{}, len(existing.Models))
	for _, m := range existing.Models {
		seen[m.ID] = struct{}{}
	}
	for _, m := range newModels {
		if _, ok := seen[m.ID]; !ok {
			return true
		}
	}
	return false
}

func (c *ACPConn) UpdateCachedCurrentThinkingEffort(effortID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cachedThinkingEffortState != nil {
		c.cachedThinkingEffortState.CurrentID = effortID
	}
}

// ---------------------------------------------------------------------------
// ACP state persistence
// ---------------------------------------------------------------------------

const acpPersistDebounce = 2 * time.Second

func (c *ACPConn) debouncePersistACPState() {
	c.persistMu.Lock()
	defer c.persistMu.Unlock()

	if c.persistTimer != nil {
		c.persistTimer.Stop()
	}
	c.persistTimer = time.AfterFunc(acpPersistDebounce, func() {
		c.persistACPState()
	})
}

func (c *ACPConn) persistACPState() { //nolint:gocyclo // ACP state serialization has multiple optional fields
	c.mu.Lock()
	if c.agent == nil {
		c.mu.Unlock()
		return
	}
	agentID := c.agent.ID
	var modeJSON, thinkingJSON, modelListJSON string
	var cmdsJSON []byte

	if c.cachedModeState != nil {
		if b, err := json.Marshal(c.cachedModeState); err == nil {
			modeJSON = string(b)
		}
	}
	if c.cachedThinkingEffortState != nil {
		if b, err := json.Marshal(c.cachedThinkingEffortState); err == nil {
			thinkingJSON = string(b)
		}
	}
	if c.cachedModelListState != nil {
		if b, err := json.Marshal(c.cachedModelListState); err == nil {
			modelListJSON = string(b)
		}
	}
	if c.client != nil {
		if cmds := c.client.GetCommandsAsInfo(); len(cmds) > 0 {
			cmdsJSON, _ = json.Marshal(cmds)
		}
	}
	c.mu.Unlock()

	if modeJSON == "" && thinkingJSON == "" && len(cmdsJSON) == 0 && modelListJSON == "" {
		return
	}

	cmdsStr := "[]"
	if len(cmdsJSON) > 0 {
		cmdsStr = string(cmdsJSON)
	}

	if err := persistAgentACPStateToDB(agentID, modeJSON, cmdsStr, thinkingJSON, modelListJSON); err != nil {
		slog.Debug("acp: failed to persist ACP state to DB", "agent_id", agentID, "error", err)
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// SetClientForTest injects a client for testing.
func (c *ACPConn) SetClientForTest(client *ClawBenchACPClient) {
	c.mu.Lock()
	c.client = client
	c.mu.Unlock()
}

// SetSessionMappingForTest injects an ACP session ID for testing.
func (c *ACPConn) SetSessionMappingForTest(_, acpSID string) {
	c.mu.Lock()
	c.acpSID = acpSID
	c.mu.Unlock()
}

// SetAliveForTest marks the connection as alive without spawning a real process.
func (c *ACPConn) SetAliveForTest() {
	pr, pw := io.Pipe()
	conn := acp.NewClientSideConnection(c.client, pw, pr)
	c.mu.Lock()
	c.alive = true
	c.conn = conn
	c.mu.Unlock()
}

// SetEntryForTest injects a connection for testing. Alias for SetConnForTest on manager.
// Deprecated: use SetConnForTest.
func (m *ACPConnManager) SetEntryForTest(agentID string, entry *ACPConn) {
	m.SetConnForTest(agentID, entry)
}

// CloseConnection closes and removes the connection for the given key.
// Deprecated: use CloseConn.
func (m *ACPConnManager) CloseConnection(key string) {
	m.CloseConn(key)
}

// GetACPSessionID resolves a ClawBench session ID to an ACP session ID.
// Deprecated: use conn.AcpSID() directly.
func (m *ACPConnManager) GetACPSessionID(_ string, clawbenchSID string) string {
	m.mu.Lock()
	conn := m.conns[clawbenchSID]
	m.mu.Unlock()

	if conn == nil {
		return ""
	}
	return conn.AcpSID()
}

// GetClientByACPSession returns the ClawBenchACPClient for the connection
// that owns the given ACP session ID.
// Deprecated: not needed in one-to-one model.
func (m *ACPConnManager) GetClientByACPSession(acpSessionID string) *ClawBenchACPClient {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, conn := range m.conns {
		conn.mu.Lock()
		if conn.acpSID == acpSessionID {
			client := conn.client
			conn.mu.Unlock()
			return client
		}
		conn.mu.Unlock()
	}
	return nil
}

// GetOrCreateSession returns the ACP session ID for a ClawBench session.
// Deprecated: use ensureAliveWithSession or AcpSID() instead.
func (c *ACPConn) GetOrCreateSession(ctx context.Context, clawbenchSID string, cwd string) (string, bool, error) {
	isNew, err := c.ensureAliveWithSession(ctx, cwd)
	if err != nil {
		return "", false, err
	}
	return c.AcpSID(), isNew, nil
}

// GetClient returns the ClawBenchACPClient for the given agent ID.
// Deprecated: use GetClientByAgentID instead.
func (m *ACPConnManager) GetClient(agentID string) *ClawBenchACPClient {
	return m.GetClientByAgentID(agentID)
}
