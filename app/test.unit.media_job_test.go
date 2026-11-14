package app

import (
	"encoding/json"
	"path/filepath"
	"testing"

	media "github.com/slidebolt/sb-media-sdk"
	messenger "github.com/slidebolt/sb-messenger-sdk"
)

func TestSubmitEncodingUsesGenericMediaFileContract(t *testing.T) {
	msg, _, err := messenger.MockWithPayload()
	if err != nil {
		t.Fatal(err)
	}
	defer msg.Close()
	requests := make(chan media.FileTranscodeRequest, 1)
	_, err = msg.Subscribe(media.SubjectFileTranscode, func(message *messenger.Message) {
		var request media.FileTranscodeRequest
		_ = json.Unmarshal(message.Data, &request)
		requests <- request
		reply, _ := json.Marshal(media.FileJobReply{JobID: request.JobID})
		_ = message.Respond(reply)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := msg.Flush(); err != nil {
		t.Fatal(err)
	}
	a := &App{media: media.NewClient(msg), config: config{RecordingDir: "/recordings"}}
	state := recordingRecord{
		ID: "recording-123", PartialPath: filepath.Join(".staging", "recording-123.ts"),
		RelativePath: filepath.Join("show", "episode.mp4"), Profile: "universal-mp4",
	}
	if err := a.submitEncoding(state); err != nil {
		t.Fatal(err)
	}
	request := <-requests
	if request.JobID != state.ID || request.Profile != media.FileProfileUniversalMP4 || !request.RemoveInputOnSuccess {
		t.Fatalf("request = %+v", request)
	}
	if request.InputPath != "/recordings/.staging/recording-123.ts" || request.OutputPath != "/recordings/show/episode.mp4" {
		t.Fatalf("paths = %q -> %q", request.InputPath, request.OutputPath)
	}
}
