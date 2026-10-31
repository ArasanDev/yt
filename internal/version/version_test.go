package version

import "testing"

func TestStringIncludesStampedMetadata(t *testing.T) {
	oldVersion, oldCommit, oldDate := Version, Commit, Date
	t.Cleanup(func() { Version, Commit, Date = oldVersion, oldCommit, oldDate })

	Version, Commit, Date = "v1.2.3", "abc1234", "2026-10-31T00:00:00Z"
	if got, want := String(), "v1.2.3 (abc1234, 2026-10-31T00:00:00Z)"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestStringOmitsUnstampedCommit(t *testing.T) {
	oldVersion, oldCommit, oldDate := Version, Commit, Date
	t.Cleanup(func() { Version, Commit, Date = oldVersion, oldCommit, oldDate })

	Version, Commit, Date = "v1.2.3", "none", "unknown"
	if got, want := String(), "v1.2.3"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestCurrentPrefersLdflagsVersion(t *testing.T) {
	oldVersion := Version
	t.Cleanup(func() { Version = oldVersion })

	Version = "v1.2.3"
	if got := Current(); got != "v1.2.3" {
		t.Fatalf("Current() = %q, want v1.2.3", got)
	}
}
