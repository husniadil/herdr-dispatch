package config

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// WorkerDeniedBash are the shell commands no worker of this dispatcher may
// run, whatever permission mode it is in.
//
// The failure they exist for, measured on 2026-08-29: a worker given a vague
// task in the Herdr checkout, running in `bypassPermissions`, ran
// `herdr workspace close w3`. That closed this daemon's own base pane, another
// task's live worker and a whole workspace — one command, three losses, and
// nothing in the task asked for any of them.
//
// Deny is the only rule shape that reaches a bypass-mode worker. Claude Code's
// own documentation is explicit about it: "Deny rules block in every mode,
// including bypassPermissions. Allow rules have no effect in
// bypassPermissions" (code.claude.com/docs/en/permission-modes, read
// 2026-08-29), and deny is evaluated before ask and allow at every scope, so
// nothing a profile or a project settings file says can hand one back.
//
// The verbs are the destructive half of `herdr --help` as it stands on herdr
// 0.8.2, and they are denied because none of them is a worker's to run: the
// panes, tabs and workspaces a worker lives in are this daemon's to open and
// retire, the server and the session belong to the operator, and the worktrees
// are handed out and reaped here. A worker that believes it needs one of these
// is a worker that should say so in its report.
//
// The `*` sits AFTER the subcommand, which is what bounds each rule to the
// verb it names: Claude Code matches everything before the first `*` as
// written. `Bash(herdr server*)` covers the bare `herdr server` as well as its
// subcommands, which is deliberate — a worker starting a second headless
// server is its own outage.
var WorkerDeniedBash = []string{
	"Bash(herdr workspace close*)",
	"Bash(herdr tab close*)",
	"Bash(herdr pane close*)",
	"Bash(herdr pane move*)",
	"Bash(herdr pane swap*)",
	"Bash(herdr worktree remove*)",
	"Bash(herdr session stop*)",
	"Bash(herdr session delete*)",
	"Bash(herdr server*)",
	"Bash(herdr update*)",
	"Bash(herdr config reset-keys*)",
}

// WorkerSettings is the `--settings` document a worker is launched with: the
// launcher's own document with this dispatcher's deny rules spliced into it.
//
// The launcher's half is passed in whole and never rewritten — a codex spawn's
// document already carries `permissions.deny` of its own, and dropping an
// entry of it here would silently undo a rule somebody else set. The two lists
// are merged, ours added only where it is not already there, so the result is
// the same document however many times it is composed.
//
// An empty document is the claude provider, which has no launcher half. It
// still gets a settings file, because the rules are about what a WORKER may do
// and not about how it is routed.
//
// The result is one line. herdr refuses an agent argument containing a
// newline, and the path this document is written to travels on a line herdr
// types into the pane.
func WorkerSettings(launcher string) (string, error) {
	root := map[string]json.RawMessage{}
	if doc := strings.TrimSpace(launcher); doc != "" {
		if err := json.Unmarshal([]byte(doc), &root); err != nil {
			return "", fmt.Errorf("worker settings: the launcher's document is not a JSON object: %w", err)
		}
	}
	perms := map[string]json.RawMessage{}
	if raw, ok := root["permissions"]; ok {
		if err := json.Unmarshal(raw, &perms); err != nil {
			return "", fmt.Errorf("worker settings: the launcher's document has a `permissions` that is not an object: %w", err)
		}
	}
	var deny []string
	if raw, ok := perms["deny"]; ok {
		if err := json.Unmarshal(raw, &deny); err != nil {
			return "", fmt.Errorf("worker settings: the launcher's document has a `permissions.deny` that is not a list of rules: %w", err)
		}
	}
	have := make(map[string]bool, len(deny))
	for _, rule := range deny {
		have[rule] = true
	}
	for _, rule := range WorkerDeniedBash {
		if !have[rule] {
			deny = append(deny, rule)
			have[rule] = true
		}
	}

	denyDoc, err := json.Marshal(deny)
	if err != nil {
		return "", fmt.Errorf("worker settings: %w", err)
	}
	perms["deny"] = denyDoc
	permsDoc, err := json.Marshal(perms)
	if err != nil {
		return "", fmt.Errorf("worker settings: %w", err)
	}
	root["permissions"] = permsDoc
	out, err := json.Marshal(root)
	if err != nil {
		return "", fmt.Errorf("worker settings: %w", err)
	}
	return string(out), nil
}
