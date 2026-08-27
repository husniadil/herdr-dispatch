package config

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// WorkerMCPFile is the default document's name, under the state dir.
const WorkerMCPFile = "worker.mcp.json"

// WorkerMCPConfigPath is <state_dir>/worker.mcp.json, the doors a worker gets
// when no document names a file of its own.
//
// It is the dispatcher's own state and not the operator's HOME: a worker
// launched without `--mcp-config` discovers its doors through `~/.mcp.json`,
// which is the OPERATOR's file, and an operator whose own file holds only the
// hub connector then hands every worker a fleet with no local doors at all.
func WorkerMCPConfigPath() string { return filepath.Join(StateDir(), WorkerMCPFile) }

// WorkerMCPDoor is one stdio door in the default document: the server name a
// worker addresses, and the plugin binary behind it.
type WorkerMCPDoor struct {
	Server  string
	Command string
}

// WorkerMCPDoors are the four plugin doors a worker of this fleet gets, and
// nothing else. They are the local doors that derive a worker's principal
// from its pane (contract §3.1); a hub connector cannot, because it speaks
// for whoever registered it.
var WorkerMCPDoors = []WorkerMCPDoor{
	{Server: "herdr-tasks", Command: "htask"},
	{Server: "herdr-mail", Command: "hmail"},
	{Server: "herdr-dispatch", Command: "hdis"},
	{Server: "herdr-sched", Command: "hsched"},
}

// WorkerMCPVerb is the verb every plugin binary serves its door on (§7.3).
const WorkerMCPVerb = "mcp"

// mcpServer is one entry of the document Claude Code reads.
type mcpServer struct {
	Type    string   `json:"type"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// RenderWorkerMCPConfig is the default document: the four doors, each running
// its plugin's own `mcp` verb over stdio.
//
// A command is written as the ABSOLUTE path lookPath resolves, and as the bare
// name when it resolves nothing. The pane a worker comes up in is opened in a
// worktree under the state directory, where a plugin binary kept in a
// project's own bin/ is not on PATH — measured from a live worker pane on
// 2026-08-23, where `hmail` came back `command not found` — so a path taken
// while the daemon's own PATH still holds it is worth more than a name the
// worker has to resolve again. A name that resolves nowhere is kept rather
// than dropped: a door that may work later is better than one that cannot.
func RenderWorkerMCPConfig(lookPath func(string) (string, error)) ([]byte, error) {
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	servers := make(map[string]mcpServer, len(WorkerMCPDoors))
	for _, door := range WorkerMCPDoors {
		command := door.Command
		if abs, err := lookPath(command); err == nil && abs != "" {
			command = abs
		}
		servers[door.Server] = mcpServer{
			Type:    "stdio",
			Command: command,
			Args:    []string{WorkerMCPVerb},
		}
	}
	b, err := json.MarshalIndent(map[string]any{"mcpServers": servers}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("worker mcp config: %w", err)
	}
	return append(b, '\n'), nil
}

// WorkerMCPFileMode is what the default document is created with. It carries
// no secret, and it is written in a directory only the operator can list;
// this keeps it the same shape as everything else the state dir holds.
const WorkerMCPFileMode = 0o600

// EnsureWorkerMCPConfig writes the default document at path if nothing is
// there, and leaves what is there exactly as it is.
//
// Once, and never again: an operator who edited the doors edited them on
// purpose, and a dispatcher that rewrote the file on every spawn would undo
// that silently. Deleting the file is how the default is asked for again.
func EnsureWorkerMCPConfig(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	doc, err := RenderWorkerMCPConfig(exec.LookPath)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, WorkerMCPFileMode)
	if err != nil {
		if os.IsExist(err) {
			// Someone else got there first, which is the same answer as
			// finding it above: the file exists and is theirs.
			return nil
		}
		return fmt.Errorf("worker mcp config %s: %w", path, err)
	}
	_, err = f.Write(doc)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path)
		return fmt.Errorf("worker mcp config %s: %w", path, err)
	}
	return nil
}
