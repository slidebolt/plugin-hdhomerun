package app

import "testing"

func TestHelloSetupAndToolRequirementsAreValid(t *testing.T) {
	hello := New().Hello()
	if err := hello.Validate(); err != nil {
		t.Fatal(err)
	}
	if hello.Setup == nil || len(hello.Setup.Tools) != 2 {
		t.Fatalf("setup tools = %#v", hello.Setup)
	}
}

func TestLoadConfigUsesInjectedToolPaths(t *testing.T) {
	t.Setenv("HDHOMERUN_FFMPEG_PATH", "/opt/media/ffmpeg")
	t.Setenv("HDHOMERUN_FFPROBE_PATH", "/opt/media/ffprobe")
	config := loadConfig()
	if config.FFmpegPath != "/opt/media/ffmpeg" || config.FFprobePath != "/opt/media/ffprobe" {
		t.Fatalf("tool paths = %q, %q", config.FFmpegPath, config.FFprobePath)
	}
}
