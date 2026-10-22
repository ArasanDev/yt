package youtube

import (
	"context"
	"time"

	"yt/internal/apperr"
)

// DoctorClient is one acquisition path's health result.
type DoctorClient struct {
	Client        string      `json:"client"`
	Version       string      `json:"version,omitempty"`
	OK            bool        `json:"ok"`
	Title         string      `json:"title,omitempty"`
	CaptionTracks int         `json:"caption_tracks,omitempty"`
	MS            int64       `json:"ms,omitempty"`
	Code          apperr.Code `json:"code,omitempty"`
	Error         string      `json:"error,omitempty"`
}

// DoctorReport is the result of probing each acquisition path.
type DoctorReport struct {
	ProbeVideo string         `json:"probe_video"`
	OKClients  int            `json:"ok_clients"`
	TotalMS    int64          `json:"total_ms"`
	Results    []DoctorClient `json:"results"`
	Verdict    string         `json:"verdict"`
}

// Doctor checks which Innertube clients and the watch page still work.
func (c *Client) Doctor(ctx context.Context, probe string) DoctorReport {
	start := time.Now()
	report := DoctorReport{ProbeVideo: probe, Verdict: "healthy", Results: []DoctorClient{}}

	check := func(name, version string, fetch func() (*playerResponse, error)) {
		entry := DoctorClient{Client: name, Version: version}
		t0 := time.Now()
		pr, err := fetch()
		if err != nil {
			if te, ok := apperr.As(err); ok {
				entry.Code = te.Code
				entry.Error = te.Message
			} else {
				entry.Error = err.Error()
			}
		} else {
			entry.OK = true
			entry.Title = pr.VideoDetails.Title
			entry.CaptionTracks = len(pr.tracks())
			entry.MS = time.Since(t0).Milliseconds()
			report.OKClients++
		}
		report.Results = append(report.Results, entry)
	}

	for _, cc := range clientContexts {
		cc := cc
		check(cc.Name, cc.Version, func() (*playerResponse, error) {
			return c.innertubePlayer(ctx, probe, cc)
		})
	}
	check("WATCH_PAGE", "", func() (*playerResponse, error) {
		return c.watchPagePlayer(ctx, probe)
	})

	report.TotalMS = time.Since(start).Milliseconds()
	if report.OKClients == 0 {
		report.Verdict = "all_clients_failed"
	}
	return report
}
