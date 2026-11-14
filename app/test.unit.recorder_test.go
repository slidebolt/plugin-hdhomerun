package app

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
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

func TestUniversalProfileUsesPortableCodecsAndFastStart(t *testing.T) {
	args := universalArgs("capture.ts", "episode.mp4.partial")
	for _, want := range []string{"libx264", "22", "yuv420p", "high", "4.1", "aac", "128k", "2", "+faststart"} {
		if !slices.Contains(args, want) {
			t.Errorf("args missing %q: %v", want, args)
		}
	}
	if args[len(args)-1] != "episode.mp4.partial" {
		t.Fatalf("output argument: %q", args[len(args)-1])
	}
}

func TestUniversalProfileProducesValidatedMP4(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not installed")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "source.ts")
	// Production encodes to an atomic partial name before publishing the MP4.
	// Keep this suffix here so the test proves the muxer is selected explicitly.
	output := filepath.Join(dir, "output.mp4.partial")
	makeSource := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30:duration=1", "-f", "lavfi", "-i", "sine=frequency=1000:duration=1", "-c:v", "mpeg2video", "-c:a", "mp2", "-f", "mpegts", source)
	if out, err := makeSource.CombinedOutput(); err != nil {
		t.Fatalf("make source: %v: %s", err, out)
	}
	encode := exec.Command("ffmpeg", universalArgs(source, output)...)
	if out, err := encode.CombinedOutput(); err != nil {
		t.Fatalf("encode: %v: %s", err, out)
	}
	if err := validateUniversal(context.Background(), output); err != nil {
		t.Fatal(err)
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
