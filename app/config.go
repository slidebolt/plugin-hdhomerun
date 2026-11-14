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
	GuideEmail   string
	Refresh      time.Duration
	FFmpegPath   string
	FFprobePath  string
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
		profile = "universal-mp4"
	}
	ffmpegPath := strings.TrimSpace(os.Getenv("HDHOMERUN_FFMPEG_PATH"))
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	ffprobePath := strings.TrimSpace(os.Getenv("HDHOMERUN_FFPROBE_PATH"))
	if ffprobePath == "" {
		ffprobePath = "ffprobe"
	}
	return config{Devices: devices, RecordingDir: dir, Profile: profile, XMLTVURL: os.Getenv("HDHOMERUN_XMLTV_URL"), GuideEmail: strings.TrimSpace(os.Getenv("HDHOMERUN_GUIDE_EMAIL")), Refresh: 10 * time.Minute, FFmpegPath: ffmpegPath, FFprobePath: ffprobePath}
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
