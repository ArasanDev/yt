package main

import (
	"bytes"
	"strings"
	"testing"
)

// Bare `yt` at an interactive prompt must print help, not start an MCP server
// that waits on stdin forever.
func TestRootNoArgsWithTTYShowsHelp(t *testing.T) {
	orig := stdinIsTerminal
	t.Cleanup(func() { stdinIsTerminal = orig })
	stdinIsTerminal = func() bool { return true }

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Fatalf("expected help output, got %q", out.String())
	}
	if strings.Contains(out.String(), `"jsonrpc"`) {
		t.Fatalf("TTY invocation served MCP instead of help: %q", out.String())
	}
}

// The explicit subcommand must keep working; the no-args default is additive.
func TestMCPSubcommandRemainsExplicit(t *testing.T) {
	cmd, _, err := newRootCmd().Find([]string{"mcp"})
	if err != nil {
		t.Fatalf("Find(mcp): %v", err)
	}
	if cmd.Name() != "mcp" {
		t.Fatalf("expected mcp subcommand, got %q", cmd.Name())
	}
}
