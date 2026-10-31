package youtube

import (
	"context"
	"testing"
)

func TestParseChapters(t *testing.T) {
	desc := "Blurb about the episode.\n\nChapters:\n00:00 Opening\n01:30 - The problem\n1:02:03 Later\n\nLinks:\nhttps://example.com\n"
	got := parseChapters(desc, 4000)
	if len(got) != 3 {
		t.Fatalf("want 3 chapters, got %d: %+v", len(got), got)
	}
	if got[0].StartSeconds != 0 || got[0].Title != "Opening" {
		t.Errorf("chapter 0 = %+v", got[0])
	}
	if got[1].StartSeconds != 90 || got[1].Timestamp != "01:30" || got[1].Title != "The problem" {
		t.Errorf("chapter 1 = %+v", got[1])
	}
	if got[2].StartSeconds != 3723 || got[2].Timestamp != "01:02:03" {
		t.Errorf("chapter 2 = %+v", got[2])
	}
}

func TestParseChaptersVariants(t *testing.T) {
	for _, desc := range []string{
		"TIMESTAMPS\n00:00 Intro\n00:10 Body",
		"Chapters\n* 00:00 Intro\n* 00:10 Body",
		"chapters:\n00:00 – Intro\n00:10 — Body",
	} {
		if got := parseChapters(desc, 100); len(got) != 2 {
			t.Errorf("variant %q parsed %d chapters: %+v", desc, len(got), got)
		}
	}
}

func TestParseChaptersDecoratedHeadings(t *testing.T) {
	for _, desc := range []string{
		"Chapters: 00:00 Intro\n00:10 Body",
		"Chapters 0:00 Intro\n00:10 Body",
		"📝 Chapters\n00:00 Intro\n00:10 Body",
		"## Chapters\n00:00 Intro\n00:10 Body",
		"**Timestamps**\n00:00 Intro\n00:10 Body",
		"⏱️ Timestamps:\n00:00 Intro\n00:10 Body",
	} {
		got := parseChapters(desc, 100)
		if len(got) != 2 || got[0].Title != "Intro" || got[0].StartSeconds != 0 || got[1].Title != "Body" {
			t.Errorf("decorated heading %q parsed %+v", desc, got)
		}
	}
}

// A line that merely starts with the heading word but continues in prose must
// not be mistaken for a heading.
func TestParseChaptersRejectsProseMistakingForHeading(t *testing.T) {
	if got := parseChapters("Chapters are below\n00:00 Intro\n00:10 Body", 100); got != nil {
		t.Fatalf("prose line is not a heading, got %+v", got)
	}
}

// Invalid, duplicate, out-of-order, and past-duration entries are dropped;
// the valid remainder is kept.
func TestParseChaptersDropsInvalidEntries(t *testing.T) {
	desc := "Chapters:\n00:00 Intro\n99:99 Broken\n00:10 Body\n00:10 Duplicate\n00:05 OutOfOrder\n1:00:00 Past"
	got := parseChapters(desc, 120)
	if len(got) != 2 || got[0].Title != "Intro" || got[1].Title != "Body" {
		t.Fatalf("invalid entries must be dropped, not reject the list: %+v", got)
	}
}

func TestParseChaptersInDurationRule(t *testing.T) {
	desc := "Chapters:\n00:00 A\n00:59 B\n01:00 C"
	got := parseChapters(desc, 60)
	if len(got) != 2 || got[1].Title != "B" {
		t.Fatalf("an entry at/after the duration must be dropped, got %+v", got)
	}
	// An unknown duration (0) disables the bound.
	if got := parseChapters(desc, 0); len(got) != 3 {
		t.Fatalf("unknown duration must not filter entries, got %+v", got)
	}
}

func TestParseChaptersRejectsUnreliableLists(t *testing.T) {
	cases := map[string]struct {
		desc     string
		duration int
	}{
		"no heading":   {"00:00 Intro\n00:10 Body", 100},
		"single entry": {"Chapters:\n00:00 Intro", 100},
		"descending":   {"Chapters:\n00:10 Body\n00:00 Intro", 100},
		"past the end": {"Chapters:\n00:00 Intro\n10:00 Body", 60},
	}
	for name, tc := range cases {
		if got := parseChapters(tc.desc, tc.duration); got != nil {
			t.Errorf("%s: want nil, got %+v", name, got)
		}
	}
}

func TestParseChaptersSkipsPreambleBeforeList(t *testing.T) {
	desc := "Chapters:\nLinks below\n00:00 Intro\n00:10 Body"
	if got := parseChapters(desc, 100); len(got) != 2 {
		t.Fatalf("want 2 chapters after preamble, got %+v", got)
	}
}

func TestParseChaptersAllowsBlankLinesBetweenEntries(t *testing.T) {
	desc := "Chapters:\n00:00 Intro\n\n00:10 Body\n\n00:20 End\n\nLinks:\nhttps://example.com\n"
	if got := parseChapters(desc, 100); len(got) != 3 {
		t.Fatalf("want 3 chapters across blank lines, got %+v", got)
	}
}

func TestInfoParsesChaptersPastDescriptionCutoff(t *testing.T) {
	f, cleanup := newFakeYouTube(t)
	defer cleanup()
	f.description = "Blurb.\n\nChapters:\n00:00 Opening\n00:10 Middle\n00:20 End\n\nLinks:\nhttps://example.com\n"

	info, err := f.client.Info(context.Background(), "testvideo01", 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Chapters) != 3 {
		t.Fatalf("want 3 chapters even when the description is truncated, got %+v", info.Chapters)
	}
	if info.Chapters[1].StartSeconds != 10 || info.Chapters[1].Title != "Middle" {
		t.Errorf("chapter 1 = %+v", info.Chapters[1])
	}
	if !info.DescriptionTruncated {
		t.Error("description should still be truncated for the payload")
	}
}
