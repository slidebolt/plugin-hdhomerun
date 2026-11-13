package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"sort"
	"strings"
	"time"

	domain "github.com/slidebolt/sb-domain"
	messenger "github.com/slidebolt/sb-messenger-sdk"
	scheduler "github.com/slidebolt/sb-scheduler-sdk"
)

func (a *App) createSeriesRule(channelKey string, cmd domain.TVRecordSeries) {
	channel, ok := a.channelStates[channelKey]
	if !ok || channel.CurrentProgram == nil || channel.CurrentProgram.SeriesID == "" {
		return
	}
	rule := domain.TVRecordingRule{Enabled: true, SeriesID: channel.CurrentProgram.SeriesID, Title: channel.CurrentProgram.Title, NewOnly: !cmd.IncludeRepeats, ChannelNumbers: []string{channel.Number}, Profile: a.config.Profile}
	deviceID := "recording-rules"
	id := "rule-" + shortHash(rule.SeriesID)
	_ = a.store.Save(domain.Device{ID: deviceID, Plugin: PluginID, Name: "Recording Rules"})
	_ = a.store.Save(domain.Entity{ID: id, Plugin: PluginID, DeviceID: deviceID, Type: "tv_recording_rule", Name: rule.Title, Commands: []string{"switch_turn_on", "switch_turn_off"}, State: rule})
	_ = a.materializeRules(time.Now())
}

func (a *App) setRuleEnabled(id string, enabled bool) {
	key := domain.EntityKey{Plugin: PluginID, DeviceID: "recording-rules", ID: id}
	raw, err := a.store.Get(key)
	if err != nil {
		return
	}
	var entity domain.Entity
	if json.Unmarshal(raw, &entity) != nil {
		return
	}
	rule, ok := entity.State.(domain.TVRecordingRule)
	if !ok {
		return
	}
	rule.Enabled = enabled
	entity.State = rule
	_ = a.store.Save(entity)
	if enabled {
		_ = a.materializeRules(time.Now())
	}
	if !enabled {
		a.disableRuleTimers(rule.SeriesID)
	}
}

func (a *App) disableRuleTimers(seriesID string) {
	entries, _ := a.store.Search("sb-scheduler.*.timer")
	for _, entry := range entries {
		var entity domain.Entity
		if json.Unmarshal(entry.Data, &entity) != nil {
			continue
		}
		timer, ok := entity.State.(domain.Timer)
		if !ok {
			continue
		}
		var input domain.RecordingStartInput
		if json.Unmarshal(timer.Input, &input) != nil || input.Program == nil || input.Program.SeriesID != seriesID {
			continue
		}
		_ = a.scheduler.DisableTimer(domain.EntityKey{Plugin: entity.Plugin, DeviceID: entity.DeviceID, ID: entity.ID})
	}
}

func (a *App) materializeRules(now time.Time) error {
	entries, err := a.store.Search(PluginID + ".recording-rules.rule-*")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		var entity domain.Entity
		if json.Unmarshal(entry.Data, &entity) != nil {
			continue
		}
		rule, ok := entity.State.(domain.TVRecordingRule)
		if !ok || !rule.Enabled {
			continue
		}
		for channelKey, programs := range a.programs {
			channel := a.channelStates[channelKey]
			if len(rule.ChannelNumbers) > 0 && !contains(rule.ChannelNumbers, channel.Number) {
				continue
			}
			for i := range programs {
				p := programs[i]
				if p.SeriesID != rule.SeriesID || !p.StartsAt.After(now) || rule.NewOnly && !p.IsNew {
					continue
				}
				a.createAiringTimer(channelKey, &p, rule.Profile)
			}
		}
		scanned := time.Now().UTC()
		rule.LastScannedAt = &scanned
		entity.State = rule
		_ = a.store.Save(entity)
	}
	return nil
}

func (a *App) createAiringTimer(channelKey string, program *domain.TVProgram, profile string) {
	if len(a.recorders) == 0 {
		return
	}
	target := a.selectRecorder(program.StartsAt, program.EndsAt)
	input, _ := json.Marshal(domain.RecordingStartInput{ChannelKey: channelKey, Program: program, Profile: profile})
	id := "airing-" + shortHash(program.AiringID)
	timerKey, err := a.scheduler.CreateTimer(scheduler.CreateTimerRequest{ID: id, Name: program.Title, Timer: domain.Timer{Enabled: true, Target: target, Input: input, On: domain.TimerTrigger{Kind: "at", At: &program.StartsAt}, Off: domain.TimerTrigger{Kind: "at", At: &program.EndsAt}}})
	if err != nil {
		log.Printf("plugin-hdhomerun: create airing timer: %v", err)
		return
	}
	channel := a.channelStates[channelKey]
	recording := domain.TVRecording{Status: "scheduled", Provider: PluginID, ExternalID: timerKey.Key(), Channel: channel, Program: *program, StartsAt: program.StartsAt, EndsAt: program.EndsAt}
	_ = a.store.Save(domain.Entity{ID: "recording-" + shortHash(program.AiringID), Plugin: PluginID, DeviceID: "television", Type: "tv_recording", Name: program.Title, Commands: []string{"tv_record_cancel"}, State: recording})
}

func (a *App) cancelTVRecording(addr messenger.Address) {
	raw, err := a.store.Get(domain.EntityKey{Plugin: PluginID, DeviceID: addr.DeviceID, ID: addr.EntityID})
	if err != nil {
		return
	}
	var envelope struct {
		State json.RawMessage `json:"state"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return
	}
	var recording domain.TVRecording
	if json.Unmarshal(envelope.State, &recording) != nil || recording.ExternalID == "" {
		return
	}
	parts := strings.Split(recording.ExternalID, ".")
	if len(parts) < 3 {
		return
	}
	key := domain.EntityKey{Plugin: parts[0], DeviceID: parts[1], ID: strings.Join(parts[2:], ".")}
	if err := a.scheduler.DisableTimer(key); err != nil {
		log.Printf("plugin-hdhomerun: cancel recording: %v", err)
		return
	}
	recording.Status = "canceled"
	_ = a.store.Save(domain.Entity{ID: addr.EntityID, Plugin: PluginID, DeviceID: addr.DeviceID, Type: "tv_recording", Name: recording.Program.Title, Commands: []string{"tv_record_cancel"}, State: recording})
}

func (a *App) selectRecorder(start, end time.Time) string {
	recorders := append([]string(nil), a.recorders...)
	sort.Strings(recorders)
	busy := map[string]bool{}
	entries, _ := a.store.Search("sb-scheduler.timer-*.timer")
	for _, entry := range entries {
		var entity domain.Entity
		if json.Unmarshal(entry.Data, &entity) != nil {
			continue
		}
		timer, ok := entity.State.(domain.Timer)
		if !ok || !timer.Enabled || timer.On.At == nil || timer.Off.At == nil {
			continue
		}
		if timer.On.At.Before(end) && timer.Off.At.After(start) {
			busy[timer.Target] = true
		}
	}
	for _, recorder := range recorders {
		if !busy[recorder] {
			return recorder
		}
	}
	return recorders[0]
}

func shortHash(v string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(v)))
	return hex.EncodeToString(sum[:8])
}
