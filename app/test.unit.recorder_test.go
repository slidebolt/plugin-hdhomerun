package app

import (
	"strings"
	"testing"
	"time"

	domain "github.com/slidebolt/sb-domain"
)

func TestRecordingPathStaysRelative(t *testing.T) {
	p := domain.TVProgram{Title: "../../News", EpisodeTitle: "A/B"}
	got := recordingPath(&p, domain.TVChannel{Number: "5.1"}, "raw-ts", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	if strings.Contains(got, "..") || strings.HasPrefix(got, "/") || !strings.HasSuffix(got, ".ts") {
		t.Fatalf("unsafe path %q", got)
	}
}

func TestUniversalProfileHasMP4Extension(t *testing.T) {
	if got := fileExt("universal-mp4"); got != "mp4" {
		t.Fatalf("fileExt: got %q want mp4", got)
	}
}

func TestRecordingUsesStableMediaItemReference(t *testing.T) {
	got := mediaItemRef("recording-123")
	if got.Provider != PluginID || got.LibraryID != recordingsDeviceID || got.ItemID != "recording-123" {
		t.Fatalf("recording item ref: %+v", got)
	}
}
