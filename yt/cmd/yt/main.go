// Command yt fetches YouTube transcripts and metadata for agents and humans.
package main

import (
	"os"

	"yt/internal/cli"
	"yt/internal/youtube"
)

func main() {
	if err := cli.NewRootCmd(youtube.NewClient()).Execute(); err != nil {
		os.Exit(1)
	}
}
