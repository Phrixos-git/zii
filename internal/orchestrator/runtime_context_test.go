package orchestrator

import (
	"strings"
	"testing"
	"time"
)

func TestLoadRuntimeTimezone(t *testing.T) {
	// Fixed reference used only to verify the resolved offset of each zone.
	ref := time.Date(2026, 10, 2, 17, 13, 0, 0, time.UTC)

	tests := []struct {
		name       string
		input      string
		wantErr    bool
		wantName   string
		wantOffset int
	}{
		{name: "blank defaults to tokyo", input: "", wantName: "Asia/Tokyo", wantOffset: 9 * 3600},
		{name: "whitespace defaults to tokyo", input: " \t ", wantName: "Asia/Tokyo", wantOffset: 9 * 3600},
		{name: "tokyo", input: "Asia/Tokyo", wantName: "Asia/Tokyo", wantOffset: 9 * 3600},
		{name: "trimmed tokyo", input: "  Asia/Tokyo  ", wantName: "Asia/Tokyo", wantOffset: 9 * 3600},
		{name: "utc", input: "UTC", wantName: "UTC", wantOffset: 0},
		{name: "invalid name", input: "Not/AZone", wantErr: true},
		{name: "local rejected", input: "Local", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loc, err := LoadRuntimeTimezone(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("LoadRuntimeTimezone(%q) = %v, want error", tt.input, loc)
				}
				if loc != nil {
					t.Fatalf("LoadRuntimeTimezone(%q) location = %v, want nil (no silent fallback)", tt.input, loc)
				}
				if !strings.Contains(err.Error(), "APP_TIMEZONE") {
					t.Fatalf("error %q does not contain APP_TIMEZONE", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadRuntimeTimezone(%q) unexpected error: %v", tt.input, err)
			}
			if loc == nil {
				t.Fatalf("LoadRuntimeTimezone(%q) location = nil, want %s", tt.input, tt.wantName)
			}
			if got := loc.String(); got != tt.wantName {
				t.Fatalf("location name = %q, want %q", got, tt.wantName)
			}
			// Verify the zone actually resolves (no silent fallback to UTC/default).
			if _, off := ref.In(loc).Zone(); off != tt.wantOffset {
				t.Fatalf("offset for %q = %d, want %d", tt.input, off, tt.wantOffset)
			}
		})
	}
}

func TestRuntimeContext(t *testing.T) {
	ref := time.Date(2026, 10, 2, 17, 13, 0, 0, time.UTC)

	tokyo, err := LoadRuntimeTimezone("Asia/Tokyo")
	if err != nil {
		t.Fatalf("LoadRuntimeTimezone(Asia/Tokyo) error: %v", err)
	}
	utc, err := LoadRuntimeTimezone("UTC")
	if err != nil {
		t.Fatalf("LoadRuntimeTimezone(UTC) error: %v", err)
	}

	tokyoCtx := runtimeContext(ref, tokyo)
	for _, want := range []string{
		"Trusted runtime context: Reference ",
		"the user's Discord message creation time",
		"2026-10-03T02:13:00+09:00",
		"Timezone Asia/Tokyo",
		"Current date 2026-10-03",
		"Day of week Saturday",
	} {
		if !strings.Contains(tokyoCtx, want) {
			t.Errorf("Tokyo context %q missing %q", tokyoCtx, want)
		}
	}

	utcCtx := runtimeContext(ref, utc)
	for _, want := range []string{
		"Trusted runtime context: Reference ",
		"the user's Discord message creation time",
		"2026-10-02T17:13:00Z",
		"Timezone UTC",
		"Current date 2026-10-02",
		"Day of week Friday",
	} {
		if !strings.Contains(utcCtx, want) {
			t.Errorf("UTC context %q missing %q", utcCtx, want)
		}
	}
}

func TestRuntimeContextMidnightBoundary(t *testing.T) {
	tokyo, err := LoadRuntimeTimezone("Asia/Tokyo")
	if err != nil {
		t.Fatalf("LoadRuntimeTimezone(Asia/Tokyo) error: %v", err)
	}
	// A different input location to prove conversion to the configured
	// zone is used, not the input's own location.
	sydney, err := LoadRuntimeTimezone("Australia/Sydney")
	if err != nil {
		t.Fatalf("LoadRuntimeTimezone(Australia/Sydney) error: %v", err)
	}

	boundaries := []struct {
		name     string
		ref      time.Time
		wantHms  string // expected local RFC3339 instant
		wantDate string
		wantDow  string
	}{
		{
			name:     "one second before tokyo midnight",
			ref:      time.Date(2026, 10, 2, 14, 59, 59, 0, time.UTC),
			wantHms:  "2026-10-02T23:59:59+09:00",
			wantDate: "Current date 2026-10-02",
			wantDow:  "Day of week Friday",
		},
		{
			name:     "at tokyo midnight",
			ref:      time.Date(2026, 10, 2, 15, 0, 0, 0, time.UTC),
			wantHms:  "2026-10-03T00:00:00+09:00",
			wantDate: "Current date 2026-10-03",
			wantDow:  "Day of week Saturday",
		},
	}

	for _, tt := range boundaries {
		t.Run(tt.name, func(t *testing.T) {
			got := runtimeContext(tt.ref, tokyo)
			for _, want := range []string{tt.wantHms, tt.wantDate, tt.wantDow} {
				if !strings.Contains(got, want) {
					t.Errorf("context %q missing %q", got, want)
				}
			}
			// Equivalent instant expressed in another input location must
			// produce identical output.
			if same := runtimeContext(tt.ref.In(sydney), tokyo); same != got {
				t.Errorf("context for instant in %s = %q, want identical %q", sydney, same, got)
			}
		})
	}
}
