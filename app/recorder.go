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
	finalAbs := filepath.Join(a.config.RecordingDir, rel)
	partialAbs := filepath.Join(a.config.RecordingDir, partial)
	if err := os.MkdirAll(filepath.Dir(partialAbs), 0755); err != nil {
		a.failStart(recorderKey, err)
		return
	}
	fileState := domain.RecordingFile{Status: "recording", RelativePath: rel, PartialPath: partial, Format: fileExt(profile), Profile: profile, Channel: state, Program: input.Program, StartedAt: start}
	_ = a.saveFileDevice(fileID, fileState)
	a.saveRecorder(recorderKey, domain.RecordingSwitch{Power: true, ChannelKey: input.ChannelKey, Program: input.Program, FileDeviceID: fileID, StartedAt: &start})
	go a.capture(ctx, session, recorderKey, fileID, channel.URL, partialAbs, finalAbs, fileState)
}

func (a *App) capture(ctx context.Context, session *recordingSession, recorderKey, fileID, streamURL, partialAbs, finalAbs string, state domain.RecordingFile) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err == nil {
		var resp *http.Response
		resp, err = (&http.Client{}).Do(req)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				err = fmt.Errorf("stream HTTP %d: %s", resp.StatusCode, resp.Header.Get("X-HDHomeRun-Error"))
			} else {
				err = writeStream(ctx, state.Profile, resp.Body, partialAbs)
			}
		}
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
		state.Status = "failed"
		state.Error = err.Error()
	} else if err != nil {
		state.Status = "failed"
		state.Error = err.Error()
	} else {
		state.Status = "completed"
		state.PartialPath = ""
	}
	_ = a.saveFileDevice(fileID, state)
	a.mu.Lock()
	delete(a.sessions, recorderKey)
	a.mu.Unlock()
	a.saveRecorder(recorderKey, domain.RecordingSwitch{LastError: state.Error})
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
	case "h264-mp4":
		return "mp4"
	default:
		return "ts"
	}
}

var _ = log.Printf
