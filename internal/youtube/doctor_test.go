package youtube

import "testing"

func TestDoctorVerdict(t *testing.T) {
	tests := []struct {
		name    string
		results []DoctorClient
		want    string
	}{
		{"all failed", []DoctorClient{{OK: false}, {OK: false}}, "all_clients_failed"},
		{"ok but no captions", []DoctorClient{{OK: true, CaptionTracks: 0}}, "no_captions"},
		{"one caption source", []DoctorClient{{OK: true, CaptionTracks: 0}, {OK: true, CaptionTracks: 2}}, "healthy"},
		{"empty", nil, "all_clients_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := doctorVerdict(tt.results); got != tt.want {
				t.Fatalf("doctorVerdict() = %q, want %q", got, tt.want)
			}
		})
	}
}
