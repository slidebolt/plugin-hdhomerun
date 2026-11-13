package app

import (
	"os"
	"strings"
	"time"
)

type config struct {
	Devices      []string
	RecordingDir string
	Profile      string
	XMLTVURL     string
	Refresh      time.Duration
}

func loadConfig() config {
	devices := splitCSV(os.Getenv("HDHOMERUN_DEVICES"))
	if len(devices) == 0 {
		devices = []string{"http://hdhomerun.local"}
	}
	dir := os.Getenv("HDHOMERUN_RECORDING_DIR")
	if dir == "" {
		dir = "/recordings"
	}
	profile := os.Getenv("HDHOMERUN_RECORDING_PROFILE")
	if profile == "" {
		profile = "raw-ts"
	}
	return config{Devices: devices, RecordingDir: dir, Profile: profile, XMLTVURL: os.Getenv("HDHOMERUN_XMLTV_URL"), Refresh: 10 * time.Minute}
}

func splitCSV(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if s := strings.TrimSpace(item); s != "" {
			out = append(out, strings.TrimRight(s, "/"))
		}
	}
	return out
}
