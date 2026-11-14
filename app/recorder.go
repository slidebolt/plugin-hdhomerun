package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	domain "github.com/slidebolt/sb-domain"
	media "github.com/slidebolt/sb-media-sdk"
	storage "github.com/slidebolt/sb-storage-sdk"
)

type recordingSession struct {
	cancel   context.CancelFunc
	stopping atomic.Bool
	canceled atomic.Bool
}

func (s *recordingSession) stop(cancelRecording bool) {
	s.stopping.Store(true)
	s.canceled.Store(cancelRecording)
	s.cancel()
}

type recordingStatus string

const (
	recordingStatusRecording recordingStatus = "recording"
	recordingStatusEncoding  recordingStatus = "processing"
	recordingStatusCompleted recordingStatus = "ready"
	recordingStatusFailed    recordingStatus = "failed"
	recordingStatusCanceled  recordingStatus = "canceled"
)

type recordingRecord struct {
	ID                 string            `json:"id"`
	RecorderKey        string            `json:"recorderKey"`
	Status             recordingStatus   `json:"status"`
	RelativePath       string            `json:"relativePath"`
	PartialPath        string            `json:"partialPath,omitempty"`
	Format             string            `json:"format"`
	Profile            string            `json:"profile"`
	Channel            domain.TVChannel  `json:"channel"`
	Program            *domain.TVProgram `json:"program,omitempty"`
	StartedAt          time.Time         `json:"startedAt"`
	FinishedAt         *time.Time        `json:"finishedAt,omitempty"`
	EncodingStartedAt  *time.Time        `json:"encodingStartedAt,omitempty"`
	EncodingFinishedAt *time.Time        `json:"encodingFinishedAt,omitempty"`
	SourceBytes        int64             `json:"sourceBytes,omitempty"`
	BytesWritten       int64             `json:"bytesWritten"`
	Error              string            `json:"error,omitempty"`
}

type recordingKey string

func (k recordingKey) Key() string { return PluginID + ".recordings." + string(k) }

const recordingsDeviceID = "recordings"

func (a *App) startRecording(recorderKey string, cmd domain.RecordStart) {
	a.mu.Lock()
	if a.sessions[recorderKey] != nil {
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()
	channelKey := cmd.Source.Entity
	var program *domain.TVProgram
	if raw := cmd.Metadata["program"]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &program)
	}
	if channelKey == "" {
		parts := strings.Split(recorderKey, ".")
		if len(parts) == 3 {
			number := a.selected[parts[1]]
			for key, state := range a.channelStates {
				if state.Number == number {
					channelKey = key
					break
				}
			}
		}
	}
	if channelKey == "" {
		return
	}
	channel, ok := a.channels[channelKey]
	if !ok || strings.Contains(channel.Tags, "drm") {
		return
	}
	state := a.channelStates[channelKey]
	profile := cmd.Profile
	if profile == "" {
		profile = a.config.Profile
	}
	if profile != "raw-ts" && profile != string(media.FileProfileRemuxMKV) && profile != string(media.FileProfileH264MP4) && profile != string(media.FileProfileUniversalMP4) {
		a.saveRecorder(recorderKey, domain.Recorder{Status: "error", LastError: fmt.Sprintf("unsupported recording profile %q", profile)})
		return
	}
	ctx, cancel := context.WithCancel(a.ctx)
	session := &recordingSession{cancel: cancel}
	a.mu.Lock()
	if a.sessions[recorderKey] != nil {
		a.mu.Unlock()
		cancel()
		return
	}
	a.sessions[recorderKey] = session
	a.mu.Unlock()

	start := time.Now().UTC()
	fileID := fmt.Sprintf("recording-%s-%d", start.Format("20060102-150405"), start.UnixNano()%100000)
	rel := recordingPath(program, state, profile, start)
	ext := filepath.Ext(rel)
	rel = strings.TrimSuffix(rel, ext) + "-" + shortHash(fileID) + ext
	partial := filepath.Join(".staging", fileID+".ts.partial")
	partialAbs := filepath.Join(a.config.RecordingDir, partial)
	if err := os.MkdirAll(filepath.Dir(partialAbs), 0755); err != nil {
		a.failStart(recorderKey, err)
		return
	}
	fileState := recordingRecord{
		ID: fileID, RecorderKey: recorderKey, Status: recordingStatusRecording,
		RelativePath: rel, PartialPath: partial, Format: fileExt(profile), Profile: profile,
		Channel: state, Program: program, StartedAt: start,
	}
	_ = a.saveRecordingFile(fileID, fileState)
	output := mediaItemRef(fileID)
	source := cmd.Source
	a.saveRecorder(recorderKey, domain.Recorder{Status: "recording", Source: &source, Output: &output, StartedAt: &start})
	a.wg.Add(1)
	go a.capture(ctx, session, recorderKey, fileID, channel.URL, partialAbs, fileState)
}

func (a *App) capture(ctx context.Context, session *recordingSession, recorderKey, fileID, streamURL, partialAbs string, state recordingRecord) {
	defer a.wg.Done()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err == nil {
		var resp *http.Response
		resp, err = (&http.Client{}).Do(req)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				err = fmt.Errorf("stream HTTP %d: %s", resp.StatusCode, resp.Header.Get("X-HDHomeRun-Error"))
			} else {
				err = copyStream(resp.Body, partialAbs)
			}
		}
	}
	a.finishCapture(session, recorderKey, fileID, partialAbs, state, err)
}

func copyStream(src io.Reader, path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, src)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func (a *App) finishCapture(session *recordingSession, recorderKey, fileID, partialAbs string, state recordingRecord, captureErr error) {
	a.mu.Lock()
	delete(a.sessions, recorderKey)
	a.mu.Unlock()
	now := time.Now().UTC()
	state.FinishedAt = &now

	if session.canceled.Load() {
		_ = os.Remove(partialAbs)
		state.Status, state.PartialPath, state.Error = recordingStatusCanceled, "", ""
		_ = a.saveRecordingFile(fileID, state)
		a.saveRecorder(recorderKey, domain.Recorder{Status: "idle"})
		return
	}
	if captureErr != nil && !session.stopping.Load() {
		state.Status, state.Error = recordingStatusFailed, captureErr.Error()
		if info, err := os.Stat(partialAbs); err == nil {
			state.SourceBytes = info.Size()
		}
		_ = a.saveRecordingFile(fileID, state)
		a.saveRecorder(recorderKey, domain.Recorder{Status: "error", LastError: state.Error})
		return
	}

	capturedAbs := strings.TrimSuffix(partialAbs, ".partial")
	if err := os.Rename(partialAbs, capturedAbs); err != nil {
		state.Status, state.Error = recordingStatusFailed, err.Error()
		_ = a.saveRecordingFile(fileID, state)
		a.saveRecorder(recorderKey, domain.Recorder{Status: "error", LastError: state.Error})
		return
	}
	info, err := os.Stat(capturedAbs)
	if err != nil || info.Size() == 0 {
		if err == nil {
			err = fmt.Errorf("empty capture")
		}
		state.Status, state.Error = recordingStatusFailed, err.Error()
		_ = a.saveRecordingFile(fileID, state)
		a.saveRecorder(recorderKey, domain.Recorder{Status: "error", LastError: state.Error})
		return
	}
	state.SourceBytes = info.Size()
	state.PartialPath = strings.TrimSuffix(state.PartialPath, ".partial")
	state.Error = ""

	if state.Profile == "raw-ts" {
		finalAbs := filepath.Join(a.config.RecordingDir, state.RelativePath)
		if err := os.MkdirAll(filepath.Dir(finalAbs), 0755); err == nil {
			err = os.Rename(capturedAbs, finalAbs)
		}
		if err != nil {
			state.Status, state.Error = recordingStatusFailed, err.Error()
			a.saveRecorder(recorderKey, domain.Recorder{Status: "error", LastError: state.Error})
		} else {
			state.Status, state.PartialPath, state.BytesWritten = recordingStatusCompleted, "", info.Size()
			a.saveRecorder(recorderKey, domain.Recorder{Status: "idle"})
		}
		_ = a.saveRecordingFile(fileID, state)
		return
	}

	state.Status = recordingStatusEncoding
	started := time.Now().UTC()
	state.EncodingStartedAt = &started
	_ = a.saveRecordingFile(fileID, state)
	a.saveRecorder(recorderKey, domain.Recorder{Status: "processing", Output: ptrMediaItem(mediaItemRef(fileID))})
	if err := a.submitEncoding(state); err != nil {
		state.Status, state.Error = recordingStatusFailed, "submit encode: "+err.Error()
		finished := time.Now().UTC()
		state.EncodingFinishedAt, state.FinishedAt = &finished, &finished
		_ = a.saveRecordingFile(fileID, state)
		a.saveRecorder(recorderKey, domain.Recorder{Status: "error", LastError: state.Error})
	}
}

func ptrMediaItem(v domain.MediaItemRef) *domain.MediaItemRef { return &v }

func (a *App) submitEncoding(state recordingRecord) error {
	_, err := a.media.Transcode(media.FileTranscodeRequest{
		JobID: state.ID, InputPath: filepath.Join(a.config.RecordingDir, state.PartialPath),
		OutputPath: filepath.Join(a.config.RecordingDir, state.RelativePath),
		Profile:    media.FileProfile(state.Profile), RemoveInputOnSuccess: true,
	})
	return err
}

func (a *App) handleFileJob(event media.FileJobEvent) {
	raw, err := a.store.ReadFile(storage.Internal, recordingKey(event.JobID))
	if err != nil {
		return
	}
	var state recordingRecord
	if json.Unmarshal(raw, &state) != nil || state.Status != recordingStatusEncoding {
		return
	}
	state.SourceBytes = event.SourceBytes
	switch event.Status {
	case media.FileJobProcessing:
		return
	case media.FileJobReady:
		state.Status, state.PartialPath, state.Error = recordingStatusCompleted, "", ""
		state.BytesWritten = event.BytesWritten
		a.saveRecorder(state.RecorderKey, domain.Recorder{Status: "idle"})
	case media.FileJobCanceled:
		state.Status, state.Error = recordingStatusCanceled, ""
		_ = os.Remove(filepath.Join(a.config.RecordingDir, state.PartialPath))
		state.PartialPath = ""
		a.saveRecorder(state.RecorderKey, domain.Recorder{Status: "idle"})
	case media.FileJobFailed:
		state.Status, state.Error = recordingStatusFailed, event.Error
		a.saveRecorder(state.RecorderKey, domain.Recorder{Status: "error", LastError: state.Error})
	default:
		return
	}
	finished := time.Now().UTC()
	state.EncodingFinishedAt, state.FinishedAt = &finished, &finished
	_ = a.saveRecordingFile(event.JobID, state)
}

func (a *App) recoverEncodings() {
	entries, err := a.store.SearchFiles(storage.Internal, PluginID+"."+recordingsDeviceID+".recording-*")
	if err != nil {
		return
	}
	for _, entry := range entries {
		var state recordingRecord
		if json.Unmarshal(entry.Data, &state) != nil || state.Status != recordingStatusEncoding || state.PartialPath == "" {
			continue
		}
		sourceAbs := filepath.Join(a.config.RecordingDir, state.PartialPath)
		if info, statErr := os.Stat(sourceAbs); statErr != nil || info.Size() == 0 {
			continue
		}
		_ = a.submitEncoding(state)
	}
}

func (a *App) stopRecording(key string, cancelRecording bool) {
	a.mu.Lock()
	session := a.sessions[key]
	a.mu.Unlock()
	if session != nil {
		session.stop(cancelRecording)
		return
	}
	if !cancelRecording {
		return
	}
	entries, err := a.store.SearchFiles(storage.Internal, PluginID+"."+recordingsDeviceID+".recording-*")
	if err != nil {
		return
	}
	for _, entry := range entries {
		var state recordingRecord
		if json.Unmarshal(entry.Data, &state) == nil && state.RecorderKey == key && state.Status == recordingStatusEncoding {
			_ = a.media.CancelFileJob(state.ID)
		}
	}
}

func (a *App) failStart(key string, err error) {
	a.mu.Lock()
	delete(a.sessions, key)
	a.mu.Unlock()
	a.saveRecorder(key, domain.Recorder{Status: "error", LastError: err.Error()})
}

func (a *App) saveRecorder(key string, state domain.Recorder) {
	parts := strings.Split(key, ".")
	if len(parts) != 3 {
		return
	}
	_ = a.store.Save(domain.Entity{ID: parts[2], Plugin: parts[0], DeviceID: parts[1], Type: "recorder", Name: "Recorder", Commands: []string{"record_start", "record_stop", "record_cancel", "recording_rule_create", "recording_rule_delete"}, State: state})
}

func (a *App) saveRecordingFile(id string, state recordingRecord) error {
	state.ID = id
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return a.store.WriteFile(storage.Internal, recordingKey(id), data)
}

func mediaItemRef(id string) domain.MediaItemRef {
	return domain.MediaItemRef{Provider: PluginID, ServerID: recordingsDeviceID, LibraryID: recordingsDeviceID, ItemID: id}
}

func fileName(state recordingRecord) string {
	if state.Program != nil && state.Program.Title != "" {
		return state.Program.Title
	}
	return state.Channel.Number + " " + state.Channel.Name
}

func recordingPath(program *domain.TVProgram, ch domain.TVChannel, profile string, start time.Time) string {
	title := ch.Number + "-" + ch.Name
	if program != nil && program.Title != "" {
		title = program.Title
		if program.EpisodeTitle != "" {
			title += " - " + program.EpisodeTitle
		}
	}
	title = cleanName(title)
	return filepath.Join(start.Format("2006-01-02"), fmt.Sprintf("%s - %s.%s", start.Format("150405"), title, fileExt(profile)))
}

func cleanName(v string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.TrimSpace(v) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || r == '_' {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "- ")
}

func fileExt(profile string) string {
	switch profile {
	case "remux-mkv":
		return "mkv"
	case "h264-mp4", "universal-mp4":
		return "mp4"
	default:
		return "ts"
	}
}
