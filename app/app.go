package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/slidebolt/plugin-hdhomerun/internal/guide"
	"github.com/slidebolt/plugin-hdhomerun/internal/hdhr"
	contract "github.com/slidebolt/sb-contract"
	domain "github.com/slidebolt/sb-domain"
	media "github.com/slidebolt/sb-media-sdk"
	messenger "github.com/slidebolt/sb-messenger-sdk"
	scheduler "github.com/slidebolt/sb-scheduler-sdk"
	storage "github.com/slidebolt/sb-storage-sdk"
)

const PluginID = "plugin-hdhomerun"

type App struct {
	msg           messenger.Messenger
	store         storage.Storage
	cmds          *messenger.Commands
	scheduler     *scheduler.Client
	subs          []messenger.Subscription
	config        config
	client        *hdhr.Client
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	encodeMu      sync.Mutex
	wg            sync.WaitGroup
	channels      map[string]hdhr.Channel
	channelStates map[string]domain.TVChannel
	programs      map[string][]domain.TVProgram
	recorders     []string
	selected      map[string]string
	sessions      map[string]*recordingSession
	deviceAuth    []string
	guideData     *guide.Data
	nextGuide     time.Time
}

func New() *App {
	return &App{client: hdhr.New(), channels: map[string]hdhr.Channel{}, channelStates: map[string]domain.TVChannel{}, programs: map[string][]domain.TVProgram{}, sessions: map[string]*recordingSession{}, selected: map[string]string{}}
}

func (a *App) Hello() contract.HelloResponse {
	return contract.HelloResponse{ID: PluginID, Kind: contract.KindPlugin, ContractVersion: contract.ContractVersion, DependsOn: []string{"messenger", "storage", "scheduler"}}
}

func (a *App) OnStart(deps map[string]json.RawMessage) (json.RawMessage, error) {
	msg, err := messenger.Connect(deps)
	if err != nil {
		return nil, err
	}
	store, err := storage.Connect(deps)
	if err != nil {
		msg.Close()
		return nil, err
	}
	a.msg, a.store, a.scheduler, a.config = msg, store, scheduler.NewClient(msg), loadConfig()
	a.cmds = messenger.NewCommands(msg, domain.LookupCommand)
	sub, err := a.cmds.Receive(PluginID+".>", a.handleCommand)
	if err != nil {
		return nil, err
	}
	a.subs = append(a.subs, sub)
	providerSubs, err := media.ServeProvider(msg, PluginID, a)
	if err != nil {
		return nil, err
	}
	a.subs = append(a.subs, providerSubs...)
	a.ctx, a.cancel = context.WithCancel(context.Background())
	if err := a.reconcile(a.ctx); err != nil {
		log.Printf("plugin-hdhomerun: initial reconcile: %v", err)
	}
	a.recoverEncodings()
	go a.reconcileLoop(a.ctx)
	log.Println("plugin-hdhomerun: started")
	return nil, nil
}

func (a *App) OnShutdown() error {
	if a.cancel != nil {
		a.cancel()
	}
	a.mu.Lock()
	for _, s := range a.sessions {
		s.stop()
	}
	a.mu.Unlock()
	a.wg.Wait()
	for _, sub := range a.subs {
		_ = sub.Unsubscribe()
	}
	if a.store != nil {
		a.store.Close()
	}
	if a.msg != nil {
		a.msg.Close()
	}
	return nil
}

func (a *App) reconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(a.config.Refresh)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.reconcile(ctx); err != nil {
				log.Printf("plugin-hdhomerun: reconcile: %v", err)
			}
		}
	}
}

func (a *App) reconcile(ctx context.Context) error {
	var firstErr error
	for _, base := range a.config.Devices {
		discovery, err := a.client.Discover(ctx, base)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		lineup, err := a.client.Lineup(ctx, discovery.LineupURL)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		a.syncDevice(discovery, lineup)
	}
	if err := a.refreshGuide(ctx); err != nil {
		if firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (a *App) syncDevice(d hdhr.Discover, lineup []hdhr.Channel) {
	_ = a.store.Save(domain.Device{ID: d.DeviceID, Plugin: PluginID, Name: d.FriendlyName, Meta: map[string]json.RawMessage{"model": jsonString(d.ModelNumber)}})
	if d.DeviceAuth != "" {
		a.deviceAuth = appendUnique(a.deviceAuth, d.DeviceAuth)
	}
	_ = a.store.Save(domain.Device{ID: "television", Plugin: PluginID, Name: "Television"})
	for i := 0; i < d.TunerCount; i++ {
		deviceID := fmt.Sprintf("%s-tuner%d", strings.ToLower(d.DeviceID), i)
		key := PluginID + "." + deviceID + ".recording"
		_ = a.store.Save(domain.Device{ID: deviceID, Plugin: PluginID, Name: fmt.Sprintf("%s Tuner %d", d.FriendlyName, i+1)})
		_ = a.store.Save(domain.Entity{ID: "tuner", Plugin: PluginID, DeviceID: deviceID, Type: "tv_tuner", Name: "Tuner", State: domain.TVTuner{Available: true, DeviceID: d.DeviceID, Model: d.ModelNumber, Tuner: i}})
		entity := domain.Entity{ID: "recording", Plugin: PluginID, DeviceID: deviceID, Type: "recording_switch", Name: "Recording", Commands: []string{"switch_turn_on", "switch_turn_off"}, State: domain.RecordingSwitch{}}
		if raw, err := a.store.Get(domain.EntityKey{Plugin: PluginID, DeviceID: deviceID, ID: "recording"}); err == nil {
			var old domain.Entity
			if json.Unmarshal(raw, &old) == nil {
				if state, ok := old.State.(domain.RecordingSwitch); ok {
					a.mu.Lock()
					active := a.sessions[key] != nil
					a.mu.Unlock()
					if state.Power && !active {
						a.markInterrupted(state.FileDeviceID)
						state = domain.RecordingSwitch{LastError: "recording interrupted by restart"}
					}
					entity.State = state
				}
			}
		}
		_ = a.store.Save(entity)
		a.recorders = appendUnique(a.recorders, key)
	}
	for _, ch := range lineup {
		id := "channel-" + safeID(ch.GuideNumber)
		key := PluginID + ".television." + id
		state := domain.TVChannel{
			Number: ch.GuideNumber, Name: ch.GuideName, Available: true,
			DRM: strings.Contains(ch.Tags, "drm"), HD: ch.HD != 0,
			VideoCodec: ch.VideoCodec, AudioCodec: ch.AudioCodec,
			SignalStrength: ch.SignalStrength, SignalQuality: ch.SignalQuality,
		}
		a.channels[key] = ch
		a.channelStates[key] = state
		_ = a.store.Save(domain.Entity{ID: id, Plugin: PluginID, DeviceID: "television", Type: "tv_channel", Name: ch.GuideNumber + " " + ch.GuideName, Commands: []string{"tv_record_current", "tv_record_airing", "tv_record_series"}, State: state})
	}
	options := make([]string, 0, len(lineup))
	for _, ch := range lineup {
		if !strings.Contains(ch.Tags, "drm") {
			options = append(options, ch.GuideNumber)
		}
	}
	sort.Strings(options)
	for _, recorderKey := range a.recorders {
		if !strings.HasPrefix(recorderKey, PluginID+"."+strings.ToLower(d.DeviceID)+"-") {
			continue
		}
		parts := strings.Split(recorderKey, ".")
		if len(options) > 0 && a.selected[parts[1]] == "" {
			a.selected[parts[1]] = options[0]
		}
		_ = a.store.Save(domain.Entity{ID: "channel", Plugin: PluginID, DeviceID: parts[1], Type: "select", Name: "Channel", Commands: []string{"select_option"}, State: domain.Select{Option: a.selected[parts[1]], Options: options}})
	}
	sort.Strings(a.recorders)
}

func (a *App) markInterrupted(fileDeviceID string) {
	if fileDeviceID == "" {
		return
	}
	raw, err := a.store.Get(domain.EntityKey{Plugin: PluginID, DeviceID: fileDeviceID, ID: "file"})
	if err != nil {
		return
	}
	var entity domain.Entity
	if json.Unmarshal(raw, &entity) != nil {
		return
	}
	state, ok := entity.State.(domain.RecordingFile)
	if !ok {
		return
	}
	now := time.Now().UTC()
	state.Status = domain.RecordingStatusFailed
	state.Error = "recording interrupted by restart"
	state.FinishedAt = &now
	entity.State = state
	_ = a.store.Save(entity)
}

func (a *App) refreshGuide(ctx context.Context) error {
	if a.guideData != nil && time.Now().Before(a.nextGuide) {
		return a.applyGuide(*a.guideData)
	}
	guideURL := a.config.XMLTVURL
	if guideURL == "" && len(a.deviceAuth) > 0 {
		guideURL = "https://api.hdhomerun.com/api/xmltv?DeviceAuth=" + url.QueryEscape(strings.Join(a.deviceAuth, ""))
	}
	if guideURL == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, guideURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("guide HTTP %d", resp.StatusCode)
	}
	data, err := guide.Parse(resp.Body)
	if err != nil {
		return err
	}
	a.guideData = &data
	a.nextGuide = time.Now().Add(time.Duration(20+rand.IntN(9)) * time.Hour)
	return a.applyGuide(data)
}

func (a *App) applyGuide(data guide.Data) error {
	now := time.Now()
	updated := now.UTC()
	for key, state := range a.channelStates {
		var matched []domain.TVProgram
		for xmlID, names := range data.Names {
			if contains(names, state.Number) || contains(names, state.Name) || xmlID == state.Number {
				matched = data.Programs[xmlID]
				break
			}
		}
		state.GuideUpdatedAt = &updated
		state.GuideError = ""
		state.CurrentProgram = nil
		state.NextProgram = nil
		for i := range matched {
			p := matched[i]
			if !now.Before(p.StartsAt) && now.Before(p.EndsAt) {
				cp := p
				state.CurrentProgram = &cp
			}
			if p.StartsAt.After(now) && (state.NextProgram == nil || p.StartsAt.Before(state.NextProgram.StartsAt)) {
				np := p
				state.NextProgram = &np
			}
		}
		a.channelStates[key] = state
		a.programs[key] = matched
		parts := strings.Split(key, ".")
		_ = a.store.Save(domain.Entity{ID: parts[2], Plugin: PluginID, DeviceID: "television", Type: "tv_channel", Name: state.Number + " " + state.Name, Commands: []string{"tv_record_current", "tv_record_airing", "tv_record_series"}, State: state})
	}
	return a.materializeRules(now)
}

func (a *App) handleCommand(addr messenger.Address, cmd any) {
	key := addr.Key()
	switch c := cmd.(type) {
	case domain.SwitchTurnOn:
		if addr.DeviceID == "recording-rules" {
			a.setRuleEnabled(addr.EntityID, true)
		} else {
			a.startRecording(key, c)
		}
	case domain.SwitchTurnOff:
		if addr.DeviceID == "recording-rules" {
			a.setRuleEnabled(addr.EntityID, false)
		} else {
			a.stopRecording(key)
		}
	case domain.TVRecordCurrent:
		a.recordCurrent(key, c)
	case domain.TVRecordSeries:
		a.createSeriesRule(key, c)
	case domain.TVRecordAiring:
		a.recordAiring(key, c.AiringID)
	case domain.TVRecordCancel:
		a.cancelTVRecording(addr)
	case domain.SelectOption:
		if addr.EntityID == "channel" {
			a.selected[addr.DeviceID] = c.Option
			_ = a.store.Save(domain.Entity{ID: "channel", Plugin: PluginID, DeviceID: addr.DeviceID, Type: "select", Name: "Channel", Commands: []string{"select_option"}, State: domain.Select{Option: c.Option}})
		}
	}
}

func (a *App) recordAiring(channelKey, airingID string) {
	for i := range a.programs[channelKey] {
		p := a.programs[channelKey][i]
		if p.AiringID == airingID {
			a.createAiringTimer(channelKey, &p, a.config.Profile)
			return
		}
	}
}

func (a *App) recordCurrent(channelKey string, cmd domain.TVRecordCurrent) {
	state, ok := a.channelStates[channelKey]
	if !ok || state.CurrentProgram == nil {
		return
	}
	recorder := cmd.Recorder
	if recorder == "" && len(a.recorders) > 0 {
		recorder = a.recorders[0]
	}
	if recorder == "" {
		return
	}
	now := time.Now().Add(time.Second)
	input, _ := json.Marshal(domain.RecordingStartInput{ChannelKey: channelKey, Program: state.CurrentProgram, Profile: a.config.Profile})
	id := "airing-" + safeID(state.CurrentProgram.AiringID)
	_, err := a.scheduler.CreateTimer(scheduler.CreateTimerRequest{ID: id, Name: state.CurrentProgram.Title, Timer: domain.Timer{Enabled: true, Target: recorder, Input: input, On: domain.TimerTrigger{Kind: "at", At: &now}, Off: domain.TimerTrigger{Kind: "at", At: &state.CurrentProgram.EndsAt}}})
	if err != nil {
		log.Printf("plugin-hdhomerun: schedule current: %v", err)
	}
}

func jsonString(v string) json.RawMessage { b, _ := json.Marshal(v); return b }
func appendUnique(xs []string, v string) []string {
	for _, x := range xs {
		if x == v {
			return xs
		}
	}
	return append(xs, v)
}
func contains(xs []string, v string) bool {
	for _, x := range xs {
		if strings.EqualFold(strings.TrimSpace(x), strings.TrimSpace(v)) {
			return true
		}
	}
	return false
}
func safeID(v string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(v) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
