// Command yt fetches YouTube transcripts and metadata for agents and humans.
package main

import (
	"fmt"
	"os"

	"yt/internal/apperr"
	"yt/internal/cli"
	"yt/internal/youtube"
)

func main() {
	if err := cli.NewRootCmd(youtube.NewClient()).Execute(); err != nil {
		// Cobra prints the message line on stderr. Typed errors additionally
		// get the machine-readable contract fields, so agents can branch on
		// code/retryable instead of parsing prose. stdout stays clean.
		if te, ok := apperr.As(err); ok {
			fmt.Fprintf(os.Stderr, "code: %s\nretryable: %v\n", te.Code, te.Retryable)
			if te.RetryAfterSeconds > 0 {
				fmt.Fprintf(os.Stderr, "retry_after_seconds: %d\n", te.RetryAfterSeconds)
			}
			if te.Suggestion != "" {
				fmt.Fprintf(os.Stderr, "suggestion: %s\n", te.Suggestion)
			}
		}
		os.Exit(1)
	}
}
