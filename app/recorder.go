package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	domain "github.com/slidebolt/sb-domain"
)

type recordingSession struct {
	cancel   context.CancelFunc
	stopping atomic.Bool
}

func (s *recordingSession) stop() { s.stopping.Store(true); s.cancel() }

func (a *App) startRecording(recorderKey string, cmd domain.SwitchTurnOn) {
	a.mu.Lock()
	if a.sessions[recorderKey] != nil {
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()
	var input domain.RecordingStartInput
	_ = json.Unmarshal(cmd.Input, &input)
	if input.ChannelKey == "" {
		parts := strings.Split(recorderKey, ".")
		if len(parts) == 3 {
			number := a.selected[parts[1]]
			for key, state := range a.channelStates {
				if state.Number == number {
					input.ChannelKey = key
					break
				}
			}
		}
	}
	if input.ChannelKey == "" {
		return
	}
	channel, ok := a.channels[input.ChannelKey]
	if !ok || strings.Contains(channel.Tags, "drm") {
		return
	}
	state := a.channelStates[input.ChannelKey]
	profile := input.Profile
	if profile == "" {
		profile = a.config.Profile
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
	rel := recordingPath(input.Program, state, profile, start)
	ext := filepath.Ext(rel)
	rel = strings.TrimSuffix(rel, ext) + "-" + shortHash(fileID) + ext
	partial := rel + ".partial"
	if profile == "universal-mp4" {
		partial = filepath.Join(".staging", fileID+".ts.partial")
	}
	finalAbs := filepath.Join(a.config.RecordingDir, rel)
	partialAbs := filepath.Join(a.config.RecordingDir, partial)
	if err := os.MkdirAll(filepath.Dir(partialAbs), 0755); err != nil {
		a.failStart(recorderKey, err)
		return
	}
	fileState := domain.RecordingFile{Status: domain.RecordingStatusRecording, RelativePath: rel, PartialPath: partial, Format: fileExt(profile), Profile: profile, Channel: state, Program: input.Program, StartedAt: start}
	_ = a.saveFileDevice(fileID, fileState)
	a.saveRecorder(recorderKey, domain.RecordingSwitch{Power: true, ChannelKey: input.ChannelKey, Program: input.Program, FileDeviceID: fileID, StartedAt: &start})
	a.wg.Add(1)
	go a.capture(ctx, session, recorderKey, fileID, channel.URL, partialAbs, finalAbs, fileState)
}

func (a *App) capture(ctx context.Context, session *recordingSession, recorderKey, fileID, streamURL, partialAbs, finalAbs string, state domain.RecordingFile) {
	defer a.wg.Done()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err == nil {
		var resp *http.Response
		resp, err = (&http.Client{}).Do(req)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				err = fmt.Errorf("stream HTTP %d: %s", resp.StatusCode, resp.Header.Get("X-HDHomeRun-Error"))
			} else if state.Profile == "universal-mp4" {
				err = copyStream(resp.Body, partialAbs)
			} else {
				err = writeStream(ctx, state.Profile, resp.Body, partialAbs)
			}
		}
	}
	if state.Profile == "universal-mp4" {
		a.finishCaptureForEncoding(session, recorderKey, fileID, partialAbs, finalAbs, state, err)
		return
	}
	finish := time.Now().UTC()
	state.FinishedAt = &finish
	if err == nil || session.stopping.Load() {
		if renameErr := os.Rename(partialAbs, finalAbs); renameErr != nil {
			err = renameErr
		} else if session.stopping.Load() {
			err = nil
		}
	}
	if info, statErr := os.Stat(map[bool]string{true: finalAbs, false: partialAbs}[err == nil]); statErr == nil {
		state.BytesWritten = info.Size()
	}
	if err != nil && !session.stopping.Load() {
		state.Status = domain.RecordingStatusFailed
		state.Error = err.Error()
	} else if err != nil {
		state.Status = domain.RecordingStatusFailed
		state.Error = err.Error()
	} else {
		state.Status = domain.RecordingStatusCompleted
		state.PartialPath = ""
	}
	_ = a.saveFileDevice(fileID, state)
	a.mu.Lock()
	delete(a.sessions, recorderKey)
	a.mu.Unlock()
	a.saveRecorder(recorderKey, domain.RecordingSwitch{LastError: state.Error})
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

func (a *App) finishCaptureForEncoding(session *recordingSession, recorderKey, fileID, partialAbs, finalAbs string, state domain.RecordingFile, captureErr error) {
	a.mu.Lock()
	delete(a.sessions, recorderKey)
	a.mu.Unlock()
	a.saveRecorder(recorderKey, domain.RecordingSwitch{})

	// Canceling is the normal way a timer or person ends a recording. The bytes
	// already written are a complete transport-stream capture and can be encoded.
	if captureErr != nil && !session.stopping.Load() {
		now := time.Now().UTC()
		state.Status, state.Error, state.FinishedAt = domain.RecordingStatusFailed, captureErr.Error(), &now
		if info, err := os.Stat(partialAbs); err == nil {
			state.SourceBytes = info.Size()
		}
		_ = a.saveFileDevice(fileID, state)
		return
	}
	capturedAbs := strings.TrimSuffix(partialAbs, ".partial")
	if err := os.Rename(partialAbs, capturedAbs); err != nil {
		now := time.Now().UTC()
		state.Status, state.Error, state.FinishedAt = domain.RecordingStatusFailed, err.Error(), &now
		_ = a.saveFileDevice(fileID, state)
		return
	}
	info, err := os.Stat(capturedAbs)
	if err != nil || info.Size() == 0 {
		if err == nil {
			err = fmt.Errorf("empty capture")
		}
		now := time.Now().UTC()
		state.Status, state.Error, state.FinishedAt = domain.RecordingStatusFailed, err.Error(), &now
		_ = a.saveFileDevice(fileID, state)
		return
	}
	state.Status = domain.RecordingStatusEncoding
	state.PartialPath = strings.TrimSuffix(state.PartialPath, ".partial")
	state.SourceBytes = info.Size()
	state.Error = ""
	_ = a.saveFileDevice(fileID, state)
	a.encodeUniversal(fileID, capturedAbs, finalAbs, state)
}

func (a *App) encodeUniversal(fileID, sourceAbs, finalAbs string, state domain.RecordingFile) {
	a.encodeMu.Lock()
	defer a.encodeMu.Unlock()
	if err := a.ctx.Err(); err != nil {
		return // durable encoding state is resumed on the next start
	}
	started := time.Now().UTC()
	state.EncodingStartedAt = &started
	_ = a.saveFileDevice(fileID, state)
	outputPartial := finalAbs + ".partial"
	args := universalArgs(sourceAbs, outputPartial)
	err := exec.CommandContext(a.ctx, "ffmpeg", args...).Run()
	if err == nil {
		err = validateUniversal(a.ctx, outputPartial)
	}
	if err == nil {
		err = os.Rename(outputPartial, finalAbs)
	}
	finished := time.Now().UTC()
	state.EncodingFinishedAt, state.FinishedAt = &finished, &finished
	if err != nil {
		state.Status, state.Error = domain.RecordingStatusFailed, "encode: "+err.Error()
		_ = os.Remove(outputPartial)
	} else {
		state.Status, state.Error, state.PartialPath = domain.RecordingStatusCompleted, "", ""
		if info, statErr := os.Stat(finalAbs); statErr == nil {
			state.BytesWritten = info.Size()
		}
		_ = os.Remove(sourceAbs)
	}
	_ = a.saveFileDevice(fileID, state)
}

func universalArgs(source, output string) []string {
	return []string{
		"-hide_banner", "-loglevel", "warning", "-y", "-i", source,
		"-map", "0:v:0", "-map", "0:a:0?", "-sn", "-dn",
		"-vf", "bwdif=mode=send_frame:parity=auto:deint=interlaced,scale=w='min(iw,1920)':h='min(ih,1080)':force_original_aspect_ratio=decrease:force_divisible_by=2,setsar=1",
		"-c:v", "libx264", "-preset", "medium", "-crf", "22", "-pix_fmt", "yuv420p", "-profile:v", "high", "-level:v", "4.1",
		"-c:a", "aac", "-b:a", "128k", "-ac", "2", "-ar", "48000", "-movflags", "+faststart", output,
	}
}

func validateUniversal(ctx context.Context, path string) error {
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-show_entries", "stream=codec_name,codec_type,pix_fmt", "-of", "json", path).Output()
	if err != nil {
		return fmt.Errorf("ffprobe: %w", err)
	}
	var probe struct {
		Streams []struct {
			CodecName string `json:"codec_name"`
			CodecType string `json:"codec_type"`
			Pixel     string `json:"pix_fmt"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if json.Unmarshal(out, &probe) != nil {
		return fmt.Errorf("ffprobe returned invalid JSON")
	}
	duration, _ := strconv.ParseFloat(probe.Format.Duration, 64)
	videoOK := false
	for _, stream := range probe.Streams {
		if stream.CodecType == "video" && stream.CodecName == "h264" && stream.Pixel == "yuv420p" {
			videoOK = true
		}
	}
	if !videoOK || duration <= 0 {
		return fmt.Errorf("invalid universal MP4: h264=%t duration=%g", videoOK, duration)
	}
	return nil
}

func (a *App) recoverEncodings() {
	entries, err := a.store.Search(PluginID + ".recording-*.file")
	if err != nil {
		return
	}
	for _, entry := range entries {
		var entity domain.Entity
		if json.Unmarshal(entry.Data, &entity) != nil {
			continue
		}
		state, ok := entity.State.(domain.RecordingFile)
		if !ok || state.Status != domain.RecordingStatusEncoding || state.PartialPath == "" {
			continue
		}
		sourceAbs := filepath.Join(a.config.RecordingDir, state.PartialPath)
		finalAbs := filepath.Join(a.config.RecordingDir, state.RelativePath)
		if info, statErr := os.Stat(sourceAbs); statErr != nil || info.Size() == 0 {
			continue
		}
		id := entity.DeviceID
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			a.encodeUniversal(id, sourceAbs, finalAbs, state)
		}()
	}
}

func writeStream(ctx context.Context, profile string, src io.Reader, path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if profile == "raw-ts" || profile == "" {
		_, err = io.Copy(f, src)
		return err
	}
	args := []string{"-hide_banner", "-loglevel", "warning", "-i", "pipe:0"}
	if profile == "remux-mkv" {
		args = append(args, "-c", "copy", "-f", "matroska", "pipe:1")
	} else if profile == "h264-mp4" {
		args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-c:a", "aac", "-movflags", "frag_keyframe+empty_moov", "-f", "mp4", "pipe:1")
	} else {
		return fmt.Errorf("unknown profile %q", profile)
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Stdin = src
	cmd.Stdout = f
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (a *App) stopRecording(key string) {
	a.mu.Lock()
	session := a.sessions[key]
	a.mu.Unlock()
	if session != nil {
		session.stop()
	}
}
func (a *App) failStart(key string, err error) {
	a.mu.Lock()
	delete(a.sessions, key)
	a.mu.Unlock()
	a.saveRecorder(key, domain.RecordingSwitch{LastError: err.Error()})
}
func (a *App) saveRecorder(key string, state domain.RecordingSwitch) {
	parts := strings.Split(key, ".")
	if len(parts) != 3 {
		return
	}
	_ = a.store.Save(domain.Entity{ID: parts[2], Plugin: parts[0], DeviceID: parts[1], Type: "recording_switch", Name: "Recording", Commands: []string{"switch_turn_on", "switch_turn_off"}, State: state})
}
func (a *App) saveFileDevice(id string, state domain.RecordingFile) error {
	if err := a.store.Save(domain.Device{ID: id, Plugin: PluginID, Name: fileName(state)}); err != nil {
		return err
	}
	return a.store.Save(domain.Entity{ID: "file", Plugin: PluginID, DeviceID: id, Type: "recording_file", Name: fileName(state), State: state})
}
func fileName(state domain.RecordingFile) string {
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

var _ = log.Printf
