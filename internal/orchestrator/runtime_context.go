package orchestrator

import (
	"fmt"
	"strings"
	"time"

	// Embed the IANA timezone database so LoadRuntimeTimezone works in
	// minimal deployments without /usr/share/zoneinfo (e.g. the
	// Asia/Tokyo default must resolve in slim containers).
	_ "time/tzdata"
)

// defaultRuntimeTimezone is used when the configured timezone name is empty.
const defaultRuntimeTimezone = "Asia/Tokyo"

// LoadRuntimeTimezone resolves the configured IANA timezone name used for
// per-request date/time formatting. An empty name defaults to Asia/Tokyo.
// Local is rejected to avoid host-dependent timezone behavior, and invalid
// names return an APP_TIMEZONE error instead of falling back to UTC.
func LoadRuntimeTimezone(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = defaultRuntimeTimezone
	}
	if name == "Local" {
		return nil, fmt.Errorf("orchestrator: APP_TIMEZONE %q is not allowed; use a UTC or IANA timezone name", name)
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: APP_TIMEZONE %q is not a valid UTC or IANA timezone: %w", name, err)
	}
	return location, nil
}

// runtimeContext formats trusted per-request date/time facts derived solely
// from the reference time and the configured location. It never calls
// time.Now and never includes user content. Service validation guarantees a
// nonzero reference; the constructor guarantees a non-nil location.
func runtimeContext(reference time.Time, location *time.Location) string {
	local := reference.In(location)
	return fmt.Sprintf(
		"Trusted runtime context: Reference %s (the user's Discord message creation time), Timezone %s, Current date %s, Day of week %s",
		local.Format(time.RFC3339),
		location.String(),
		local.Format("2006-01-02"),
		local.Weekday().String(),
	)
}
