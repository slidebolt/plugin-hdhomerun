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

func (a *App) createRecordingRule(addr messenger.Address, cmd domain.RecordingRuleCreate) error {
	rule := cmd.Rule
	if rule.Recorder == "" {
		rule.Recorder = addr.Key()
	}
	if rule.Selection == "" {
		rule.Selection = "all"
	}
	rule.Enabled = true
	id := strings.TrimSpace(cmd.ID)
	if id == "" {
		seed, _ := json.Marshal(rule.Query)
		id = shortHash(string(seed))
	}
	entity := domain.Entity{ID: "rule-" + id, Plugin: PluginID, DeviceID: "recording-rules", Type: "recording_rule", Name: cmd.Name, State: rule}
	if entity.Name == "" {
		entity.Name = rule.Query.Title
	}
	if err := a.store.Save(entity); err != nil {
		return err
	}
	return a.materializeRules(time.Now().UTC())
}

func (a *App) deleteRecordingRule(reference string) error {
	id := strings.TrimSpace(reference)
	if strings.HasPrefix(id, PluginID+".recording-rules.") {
		id = strings.TrimPrefix(id, PluginID+".recording-rules.")
	}
	if !strings.HasPrefix(id, "rule-") {
		id = "rule-" + id
	}
	return a.store.Delete(domain.EntityKey{Plugin: PluginID, DeviceID: "recording-rules", ID: id})
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
		rule, ok := entity.State.(domain.RecordingRule)
		if !ok || !rule.Enabled {
			continue
		}
		seriesID := stringAttribute(rule.Query.Attributes, "seriesID")
		newOnly := boolAttribute(rule.Query.Attributes, "newOnly")
		channels := stringListAttribute(rule.Query.Attributes, "channelNumbers")
		created := 0
		for channelKey, programs := range a.programs {
			channel := a.channelStates[channelKey]
			if len(channels) > 0 && !contains(channels, channel.Number) {
				continue
			}
			for i := range programs {
				p := programs[i]
				if !p.StartsAt.After(now) || seriesID != "" && p.SeriesID != seriesID || rule.Query.Title != "" && !strings.EqualFold(p.Title, rule.Query.Title) || newOnly && !p.IsNew {
					continue
				}
				if rule.Selection == "next" && created > 0 {
					continue
				}
				if rule.Limit > 0 && rule.ScheduledCount+created >= rule.Limit {
					continue
				}
				if a.createAiringSchedule(channelKey, &p, rule.Profile) {
					created++
				}
			}
		}
		scanned := time.Now().UTC()
		rule.LastScannedAt = &scanned
		rule.ScheduledCount += created
		if rule.Selection == "next" && rule.ScheduledCount > 0 || rule.Limit > 0 && rule.ScheduledCount >= rule.Limit {
			rule.Enabled = false
		}
		entity.State = rule
		_ = a.store.Save(entity)
	}
	return nil
}

func (a *App) createAiringSchedule(channelKey string, program *domain.TVProgram, profile string) bool {
	if len(a.recorders) == 0 {
		return false
	}
	target := a.selectRecorder(program.StartsAt, program.EndsAt)
	programRaw, _ := json.Marshal(program)
	channelRaw, _ := json.Marshal(a.channelStates[channelKey])
	startInput, _ := json.Marshal(domain.RecordStart{Source: domain.PlayableSource{Kind: "live", Entity: channelKey}, Profile: profile, Metadata: map[string]json.RawMessage{"program": programRaw, "channel": channelRaw}})
	id := "airing-" + shortHash(program.AiringID)
	_, err := a.scheduler.CreateSchedule(scheduler.CreateScheduleRequest{ID: id, Name: program.Title, Schedule: domain.Schedule{Enabled: true, Steps: []domain.ScheduleStep{{ID: "start", Trigger: domain.Trigger{Kind: "at", At: &program.StartsAt}, Actions: []domain.Action{{Target: target, Command: "record_start", Input: startInput}}}, {ID: "stop", Trigger: domain.Trigger{Kind: "at", At: &program.EndsAt}, Actions: []domain.Action{{Target: target, Command: "record_stop"}}}}}})
	if err != nil {
		log.Printf("plugin-hdhomerun: create airing schedule: %v", err)
		return false
	}
	return true
}

func (a *App) selectRecorder(start, end time.Time) string {
	recorders := append([]string(nil), a.recorders...)
	sort.Strings(recorders)
	busy := map[string]bool{}
	entries, _ := a.store.Search("sb-scheduler.schedule-*.schedule")
	for _, entry := range entries {
		var entity domain.Entity
		if json.Unmarshal(entry.Data, &entity) != nil {
			continue
		}
		schedule, ok := entity.State.(domain.Schedule)
		if !ok || !schedule.Enabled {
			continue
		}
		var on, off *time.Time
		target := ""
		for _, step := range schedule.Steps {
			if len(step.Actions) == 0 || step.Trigger.At == nil {
				continue
			}
			if step.Actions[0].Command == "record_start" {
				on = step.Trigger.At
				target = step.Actions[0].Target
			}
			if step.Actions[0].Command == "record_stop" {
				off = step.Trigger.At
			}
		}
		if on != nil && off != nil && on.Before(end) && off.After(start) {
			busy[target] = true
		}
	}
	for _, recorder := range recorders {
		if !busy[recorder] {
			return recorder
		}
	}
	return recorders[0]
}

func stringAttribute(values map[string]json.RawMessage, key string) string {
	var out string
	_ = json.Unmarshal(values[key], &out)
	return out
}
func boolAttribute(values map[string]json.RawMessage, key string) bool {
	var out bool
	_ = json.Unmarshal(values[key], &out)
	return out
}
func stringListAttribute(values map[string]json.RawMessage, key string) []string {
	var out []string
	_ = json.Unmarshal(values[key], &out)
	return out
}
func shortHash(v string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(v)))
	return hex.EncodeToString(sum[:8])
}
