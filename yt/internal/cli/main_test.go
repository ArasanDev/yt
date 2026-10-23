package cli

import (
	"bytes"
	"strings"
	"testing"

	"yt/internal/apperr"
	"yt/internal/youtube"
)

// Bare `yt` at an interactive prompt must print help, not start an MCP server
// that waits on stdin forever.
func TestRootNoArgsWithTTYShowsHelp(t *testing.T) {
	orig := stdinIsTerminal
	t.Cleanup(func() { stdinIsTerminal = orig })
	stdinIsTerminal = func() bool { return true }

	root := NewRootCmd(youtube.NewClient())
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
	cmd, _, err := NewRootCmd(youtube.NewClient()).Find([]string{"mcp"})
	if err != nil {
		t.Fatalf("Find(mcp): %v", err)
	}
	if cmd.Name() != "mcp" {
		t.Fatalf("expected mcp subcommand, got %q", cmd.Name())
	}
}

// Flag validation must fail with a typed bad_request BEFORE any network work.
// No real transport is exercised: the client is never called.
func TestTranscriptFlagValidationBeforeFetch(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantMsg string
	}{
		{"unknown format", []string{"transcript", "dQw4w9WgXcQ", "--format", "csv"}, "unknown --format"},
		{"negative start", []string{"transcript", "dQw4w9WgXcQ", "--start", "-2"}, "invalid --start"},
		{"negative end", []string{"transcript", "dQw4w9WgXcQ", "--end", "-1"}, "invalid --end"},
		{"inverted range", []string{"transcript", "dQw4w9WgXcQ", "--start", "20", "--end", "10"}, "inverted range"},
		{"equal range", []string{"transcript", "dQw4w9WgXcQ", "--start", "10", "--end", "10"}, "inverted range"},
		{"negative max-chars", []string{"transcript", "dQw4w9WgXcQ", "--max-chars", "-5"}, "invalid --max-chars"},
		{"negative start-cue", []string{"transcript", "dQw4w9WgXcQ", "--start-cue", "-1"}, "invalid --start-cue"},
		{"negative max-cues", []string{"transcript", "dQw4w9WgXcQ", "--max-cues", "-1"}, "invalid --max-cues"},
		{"srt rejects max-chars", []string{"transcript", "dQw4w9WgXcQ", "--format", "srt", "--max-chars", "100"}, "does not apply to --format srt"},
		{"srt rejects start-cue", []string{"transcript", "dQw4w9WgXcQ", "--format", "srt", "--start-cue", "3"}, "does not apply to --format srt"},
		{"srt rejects granularity", []string{"transcript", "dQw4w9WgXcQ", "--format", "srt", "--granularity", "cue"}, "does not apply to --format srt"},
		{"srt rejects timestamps", []string{"transcript", "dQw4w9WgXcQ", "--format", "srt", "--timestamps"}, "does not apply to --format srt"},
		{"bare needs text", []string{"transcript", "dQw4w9WgXcQ", "--format", "json", "--bare"}, "--bare applies only to"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := NewRootCmd(youtube.NewClient())
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs(tc.args)

			err := root.Execute()
			if err == nil {
				t.Fatal("expected a validation error, got nil")
			}
			te, ok := apperr.As(err)
			if !ok {
				t.Fatalf("expected typed *apperr.Error, got %T: %v", err, err)
			}
			if te.Code != apperr.CodeBadRequest {
				t.Errorf("code: want %q, got %q", apperr.CodeBadRequest, te.Code)
			}
			if !strings.Contains(te.Message, tc.wantMsg) {
				t.Errorf("message %q does not mention %q", te.Message, tc.wantMsg)
			}
			if te.Retryable {
				t.Error("validation errors must not be retryable")
			}
			if !strings.Contains(out.String(), tc.wantMsg) {
				t.Errorf("cobra stderr output %q does not mention %q", out.String(), tc.wantMsg)
			}
		})
	}
}

func TestSearchAndChannelFlagValidationBeforeFetch(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantMsg string
	}{
		{"search negative start-cue", []string{"search", "dQw4w9WgXcQ", "query", "--start-cue", "-1"}, "invalid --start-cue"},
		{"search negative context", []string{"search", "dQw4w9WgXcQ", "query", "--context-cues", "-1"}, "invalid --context-cues"},
		{"search negative max", []string{"search", "dQw4w9WgXcQ", "query", "--max-matches", "-1"}, "invalid --max-matches"},
		{"search unknown mode", []string{"search", "dQw4w9WgXcQ", "query", "--match-mode", "regex"}, "unknown match mode"},
		{"info negative description", []string{"info", "dQw4w9WgXcQ", "--description-chars", "-1"}, "invalid --description-chars"},
		{"channel negative limit", []string{"channel", "@handle", "--limit", "-3"}, "invalid --limit"},
		{"channel negative description", []string{"channel", "@handle", "--description-chars", "-1"}, "invalid --description-chars"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := NewRootCmd(youtube.NewClient())
			root.SetArgs(tc.args)
			err := root.Execute()
			if err == nil {
				t.Fatal("expected a validation error, got nil")
			}
			te, ok := apperr.As(err)
			if !ok {
				t.Fatalf("expected typed *apperr.Error, got %T: %v", err, err)
			}
			if te.Code != apperr.CodeBadRequest {
				t.Errorf("code: want %q, got %q", apperr.CodeBadRequest, te.Code)
			}
			if !strings.Contains(te.Message, tc.wantMsg) {
				t.Errorf("message %q does not mention %q", te.Message, tc.wantMsg)
			}
		})
	}
}

// The mcp help text advertises the full tool list, including the channel tool.
func TestMCPHelpListsChannelTool(t *testing.T) {
	cmd, _, err := NewRootCmd(youtube.NewClient()).Find([]string{"mcp"})
	if err != nil {
		t.Fatalf("Find(mcp): %v", err)
	}
	if !strings.Contains(cmd.Short, "list_channel_videos") {
		t.Errorf("mcp help must list list_channel_videos, got %q", cmd.Short)
	}
}
