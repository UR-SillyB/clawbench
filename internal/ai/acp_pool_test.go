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
