package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"yt/internal/apperr"
	"yt/internal/transcript"
)

// The JSON branch used to report block-rendered metadata (truncated=false,
// returned_chars including the header) while emitting an unbounded cue list.
func TestMCPJSONMetaMatchesPayload(t *testing.T) {
	f, cleanup := newFakeYouTube(t)
	defer cleanup()

	s := NewServer(f.client)
	maxChars := 60
	res, _, err := s.getTranscript(context.Background(), nil, transcriptIn{
		URLOrID: "testvideo01", Format: "json", MaxChars: &maxChars,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %v", res.Content)
	}
	var doc struct {
		Meta     transcriptMeta       `json:"meta"`
		Segments []transcript.Segment `json:"segments"`
	}
	raw := res.Content[0].(*mcpsdk.TextContent).Text
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if doc.Meta.ReturnedCues != len(doc.Segments) {
		t.Errorf("meta says %d cues, payload has %d", doc.Meta.ReturnedCues, len(doc.Segments))
	}
	if doc.Meta.Granularity != "cue" || doc.Meta.TimestampMode != "none" {
		t.Errorf("JSON metadata should describe cue segments, got %+v", doc.Meta)
	}
	if !doc.Meta.Truncated || doc.Meta.NextCueIndex == nil {
		t.Fatalf("char budget must truncate at a cue boundary: %+v", doc.Meta)
	}
	if doc.Meta.ReturnedChars > maxChars {
		t.Errorf("returned_chars %d exceeds max_chars %d", doc.Meta.ReturnedChars, maxChars)
	}
	if doc.Meta.NextCueIndex != nil && *doc.Meta.NextCueIndex != doc.Meta.ReturnedCues {
		t.Errorf("next_cue_index %d should follow the returned cues %d", *doc.Meta.NextCueIndex, doc.Meta.ReturnedCues)
	}
}

func TestMCPMaxCuesReportsContinuation(t *testing.T) {
	f, cleanup := newFakeYouTube(t)
	defer cleanup()

	s := NewServer(f.client)
	res, _, err := s.getTranscript(context.Background(), nil, transcriptIn{
		URLOrID: "testvideo01", Granularity: "cue", TimestampMode: "cue", MaxCues: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	out, ok := res.StructuredContent.(transcriptOut)
	if !ok {
		t.Fatalf("want transcriptOut, got %T", res.StructuredContent)
	}
	meta := out.transcriptMeta
	if !meta.Truncated || meta.NextCueIndex == nil || *meta.NextCueIndex != 3 {
		t.Fatalf("max_cues must advertise the next cue: %+v", meta)
	}
	if meta.ReturnedCues != 3 {
		t.Errorf("want 3 returned cues, got %d", meta.ReturnedCues)
	}
}

func TestMCPRejectsUnknownFormat(t *testing.T) {
	s := NewServer(nil)
	res, _, err := s.getTranscript(context.Background(), nil, transcriptIn{
		URLOrID: "testvideo01", Format: "xml",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertToolError(t, res, apperr.CodeBadRequest)
}

func TestMCPRejectsInvertedRange(t *testing.T) {
	s := NewServer(nil)
	res, _, err := s.getTranscript(context.Background(), nil, transcriptIn{
		URLOrID: "testvideo01", StartSeconds: 30, EndSeconds: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertToolError(t, res, apperr.CodeBadRequest)
}

// SDK input validation runs before handlers, so its IsError results used to
// arrive with no structured payload. The middleware must add a code.
func TestSchemaValidationErrorsAreStructured(t *testing.T) {
	next := func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
		return &mcpsdk.CallToolResult{
			IsError: true,
			Content: []mcpsdk.Content{&mcpsdk.TextContent{
				Text: `unexpected additional property "maxchars"`,
			}},
		}, nil
	}
	h := structuredSchemaErrors(next)
	res, err := h(context.Background(), "tools/call", nil)
	if err != nil {
		t.Fatal(err)
	}
	cr, ok := res.(*mcpsdk.CallToolResult)
	if !ok {
		t.Fatalf("want CallToolResult, got %T", res)
	}
	payload, ok := cr.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("schema error lost its structure: %#v", cr.StructuredContent)
	}
	te, ok := payload["error"].(*apperr.Error)
	if !ok || te.Code != apperr.CodeBadRequest {
		t.Fatalf("want bad_request, got %#v", payload["error"])
	}
	if !strings.Contains(te.Message, "maxchars") {
		t.Errorf("original validation message lost: %q", te.Message)
	}
}

// A client that only surfaces structuredContent must still receive the body.
// Regression: get_transcript reported returned_chars > 0 while shipping the
// text only in content[0].text, so such clients summarized nothing.
func TestMCPTranscriptBodyIsInStructuredContent(t *testing.T) {
	f, cleanup := newFakeYouTube(t)
	defer cleanup()

	s := NewServer(f.client)

	// Text format: the rendered body must be present in structured content.
	res, _, err := s.getTranscript(context.Background(), nil, transcriptIn{URLOrID: "testvideo01"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %v", res.Content)
	}
	out, ok := res.StructuredContent.(transcriptOut)
	if !ok {
		t.Fatalf("want transcriptOut, got %T", res.StructuredContent)
	}
	if strings.TrimSpace(out.Text) == "" {
		t.Fatal("structuredContent.text is empty: structured-only clients receive no transcript")
	}
	if block := res.Content[0].(*mcpsdk.TextContent).Text; block != out.Text {
		t.Error("content block and structuredContent.text disagree")
	}

	// JSON format: the cue segments must be present too.
	res, _, err = s.getTranscript(context.Background(), nil, transcriptIn{URLOrID: "testvideo01", Format: "json"})
	if err != nil {
		t.Fatal(err)
	}
	jout, ok := res.StructuredContent.(transcriptOut)
	if !ok {
		t.Fatalf("want transcriptOut, got %T", res.StructuredContent)
	}
	if len(jout.Segments) == 0 {
		t.Fatal("structuredContent.segments is empty for format=json")
	}
	if jout.ReturnedCues != len(jout.Segments) {
		t.Errorf("meta says %d cues, structured payload has %d", jout.ReturnedCues, len(jout.Segments))
	}
}

func assertToolError(t *testing.T, res *mcpsdk.CallToolResult, want apperr.Code) {
	t.Helper()
	if res == nil || !res.IsError {
		t.Fatalf("expected IsError result, got %+v", res)
	}
	payload, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("want structured payload, got %T", res.StructuredContent)
	}
	te, ok := payload["error"].(*apperr.Error)
	if !ok {
		t.Fatalf("want *apperr.Error, got %T", payload["error"])
	}
	if te.Code != want {
		t.Fatalf("want %s, got %s (%s)", want, te.Code, te.Message)
	}
}

// Text-only clients (including Code Mode bindings) surface content[0].text and
// drop structuredContent, so the actionable language list must be in the text.
func TestErrorTextIncludesAvailableLanguages(t *testing.T) {
	res := errorResult(&apperr.Error{
		Code:               apperr.CodeNoMatchingLang,
		Message:            "No caption track matches requested languages xx.",
		AvailableLanguages: []string{"en", "es"},
		Suggestion:         "Retry with one of available_languages.",
	})
	if !res.IsError {
		t.Fatal("expected IsError")
	}
	tc, ok := res.Content[0].(*mcpsdk.TextContent)
	if !ok {
		t.Fatalf("want TextContent, got %T", res.Content[0])
	}
	if !strings.Contains(tc.Text, "Available: en, es.") {
		t.Fatalf("error text must list available languages, got %q", tc.Text)
	}
}
