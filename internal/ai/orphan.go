package ai

import (
	"log/slog"
	"os"
	"runtime"
	"strings"
	"syscall"
)

// OrphanChildEnvVar is the environment variable injected into every AI
// subprocess spawned by ClawBench. On server startup, CleanupOrphans
// scans running processes for this marker and kills any orphans left
// behind by a previous server crash.
//
// This is simpler than PID-file tracking because:
//   - No Register/Unregister lifecycle — just set the env var on spawn
//   - No file I/O on every process create/destroy
//   - No cleanup needed on graceful shutdown
//   - No stale PID files to manage
//
// The env var is inert: no CLI tool reads or acts on it. It exists solely
// as a marker for orphan detection.
const OrphanChildEnvVar = "CLAWBENCH_CHILD=1"

// OrphanSupervisorVar is an older marker used by previous ClawBench versions.
// Some long-running processes from before the env var rename may still carry it.
const OrphanSupervisorVar = "CLAWBENCH_NO_SUPERVISOR=1"

// orphanCmdlinePatterns are patterns that identify an orphan ACP agent process
// by its command line. Each pattern is a pair of (binarySubstring, argSubstring).
// Both must be present in the cmdline for a match. Used as a fallback when
// env markers are missing.
var orphanCmdlinePatterns = [][2]string{
	{"codebuddy", "--acp"},
	{"claude", "--acp"},
	{"codex", "app-server"},
}

// CleanupOrphans kills any AI subprocess left running after a previous
// server crash. Called once at startup, before any new subprocesses spawn.
//
// On Linux: scans /proc/<pid>/environ for CLAWBENCH_CHILD=1 or
// CLAWBENCH_NO_SUPERVISOR=1, and also checks /proc/<pid>/cmdline for
// known ACP agent patterns (e.g., "--acp") as a fallback.
// On macOS/Windows: no-op (orphaned processes exit when stdin pipe closes).
func CleanupOrphans() {
	if runtime.GOOS != "linux" {
		return
	}

	entries, err := os.ReadDir("/proc")
	if err != nil {
		slog.Debug("orphan_cleanup: cannot read /proc, skipping", "error", err)
		return
	}

	var killed int
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		pid := 0
		if _, err := parsePID(entry.Name(), &pid); err != nil {
			continue
		}
		if pid <= 1 {
			continue // skip kernel/init
		}

		// Check environment markers
		environPath := "/proc/" + entry.Name() + "/environ"
		data, err := os.ReadFile(environPath)
		if err != nil {
			continue // permission denied or process exited
		}

		isOrphan := hasClawBenchChildMarker(data) || hasClawBenchSupervisorMarker(data)

		// Fallback: check cmdline for known ACP agent patterns
		if !isOrphan {
			cmdlinePath := "/proc/" + entry.Name() + "/cmdline"
			cmdData, err := os.ReadFile(cmdlinePath)
			if err != nil {
				continue
			}
			isOrphan = hasOrphanCmdlinePattern(cmdData)
		}

		if !isOrphan {
			continue
		}

		// Found an orphan — kill it
		proc, err := os.FindProcess(pid)
		if err != nil {
			continue
		}

		// Verify process is still alive before killing
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			continue
		}

		slog.Info("orphan_cleanup: killing orphan AI process", "pid", pid)
		if err := proc.Kill(); err != nil {
			slog.Warn("orphan_cleanup: failed to kill orphan", "pid", pid, "error", err)
		} else {
			// Reap the process to avoid zombies
			_, _ = proc.Wait()
			killed++
		}
	}

	if killed > 0 {
		slog.Info("orphan_cleanup: complete", "killed", killed)
	}
}

// hasClawBenchChildMarker checks if the /proc/<pid>/environ data
// contains the CLAWBENCH_CHILD=1 marker. The environ file uses
// null bytes (\0) as delimiters between entries.
func hasClawBenchChildMarker(environData []byte) bool {
	return bytesContainsSep(environData, []byte(OrphanChildEnvVar), 0)
}

// hasClawBenchSupervisorMarker checks for the older CLAWBENCH_NO_SUPERVISOR=1
// marker used by previous ClawBench versions.
func hasClawBenchSupervisorMarker(environData []byte) bool {
	return bytesContainsSep(environData, []byte(OrphanSupervisorVar), 0)
}

// hasOrphanCmdlinePattern checks if the /proc/<pid>/cmdline data contains
// known ACP agent patterns. The cmdline file uses null bytes as delimiters
// between arguments. Both the binary substring and arg substring must match.
func hasOrphanCmdlinePattern(cmdData []byte) bool {
	cmdStr := strings.ReplaceAll(string(cmdData), "\x00", " ")
	for _, pattern := range orphanCmdlinePatterns {
		if strings.Contains(cmdStr, pattern[0]) && strings.Contains(cmdStr, pattern[1]) {
			return true
		}
	}
	return false
}

// bytesContainsSep checks if data contains target as a segment
// delimited by sep byte. This avoids false positives from substring
// matches (e.g., "FOO_CLAWBENCH_CHILD=1" shouldn't match).
func bytesContainsSep(data, target []byte, sep byte) bool {
	targetLen := len(target)
	if targetLen == 0 {
		return true
	}

	for i := 0; i <= len(data)-targetLen; i++ {
		if data[i] != target[0] {
			continue
		}
		match := true
		for j := 1; j < targetLen; j++ {
			if data[i+j] != target[j] {
				match = false
				break
			}
		}
		if match {
			// Verify it's a complete segment (bounded by sep or at start/end)
			before := i == 0 || data[i-1] == sep
			after := i+targetLen == len(data) || data[i+targetLen] == sep
			if before && after {
				return true
			}
		}
	}
	return false
}

// parsePID parses a string as a positive integer PID.
func parsePID(s string, pid *int) (bool, error) { //nolint:unparam // error return kept for API consistency
	for _, c := range s {
		if c < '0' || c > '9' {
			return false, nil
		}
	}
	for _, c := range s {
		*pid = *pid*10 + int(c-'0')
	}
	return true, nil
}
