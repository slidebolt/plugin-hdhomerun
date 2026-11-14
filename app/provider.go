package app

import (
	"encoding/json"
	"strconv"
	"strings"

	domain "github.com/slidebolt/sb-domain"
	media "github.com/slidebolt/sb-media-sdk"
	storage "github.com/slidebolt/sb-storage-sdk"
)

func (a *App) Browse(req media.CatalogRequest) media.CatalogPage {
	if req.LibraryID != recordingsDeviceID && req.ServerID != recordingsDeviceID {
		return media.CatalogPage{Error: "media library not found"}
	}
	entries, err := a.store.SearchFiles(storage.Internal, PluginID+"."+recordingsDeviceID+".recording-*")
	if err != nil {
		return media.CatalogPage{Error: err.Error()}
	}
	items := []media.MediaItem{}
	for _, entry := range entries {
		var record recordingRecord
		if json.Unmarshal(entry.Data, &record) != nil {
			continue
		}
		if req.ItemID != "" && record.ID != req.ItemID {
			continue
		}
		title := fileName(record)
		if req.Query != "" && !strings.Contains(strings.ToLower(title), strings.ToLower(req.Query)) {
			continue
		}
		metadata := map[string]json.RawMessage{}
		putRaw(metadata, "status", record.Status)
		putRaw(metadata, "format", record.Format)
		putRaw(metadata, "profile", record.Profile)
		putRaw(metadata, "relativePath", record.RelativePath)
		putRaw(metadata, "startedAt", record.StartedAt)
		putRaw(metadata, "finishedAt", record.FinishedAt)
		putRaw(metadata, "sizeBytes", record.BytesWritten)
		putRaw(metadata, "error", record.Error)
		putRaw(metadata, "channel", record.Channel)
		putRaw(metadata, "program", record.Program)
		items = append(items, media.MediaItem{Ref: mediaItemRef(record.ID), Kind: mediaKind(record), Title: title, Playable: record.Status == recordingStatusCompleted, Metadata: metadata})
	}
	return media.CatalogPage{Items: items, Total: len(items)}
}
func putRaw(values map[string]json.RawMessage, key string, value any) {
	data, _ := json.Marshal(value)
	if string(data) != "null" && string(data) != "\"\"" && string(data) != "0" {
		values[key] = data
	}
}
func mediaKind(record recordingRecord) string {
	if record.Program != nil && record.Program.EpisodeTitle != "" {
		return "episode"
	}
	return "recording"
}
func (a *App) Artwork(media.ArtworkRequest) media.ArtworkReply {
	return media.ArtworkReply{Error: "HDHomeRun guide artwork is not available"}
}
func (a *App) Resolve(req media.ResolveRequest) media.ResolveReply {
	if req.Source.Kind != "live" || req.Source.Entity == "" {
		return media.ResolveReply{Error: "HDHomeRun resolves live sources only"}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	channel, ok := a.channels[req.Source.Entity]
	if !ok {
		return media.ResolveReply{Error: "live source not found"}
	}
	if strings.Contains(channel.Tags, "drm") {
		return media.ResolveReply{Error: "source is DRM protected"}
	}
	return media.ResolveReply{URL: channel.URL, Kind: "video", Format: "mpegts"}
}
func (a *App) Guide(req media.GuideRequest) media.GuidePage {
	a.mu.Lock()
	defer a.mu.Unlock()
	channels := []media.GuideChannel{}
	programs := []domain.TVProgram{}
	for key, state := range a.channelStates {
		parts := strings.SplitN(key, ".", 3)
		if req.ServerID != "" && (len(parts) < 3 || req.ServerID != parts[1]) {
			continue
		}
		if req.Channel != "" && req.Channel != state.Number && req.Channel != key {
			continue
		}
		channels = append(channels, media.GuideChannel{ID: key, State: state})
		for _, p := range a.programs[key] {
			if (!req.Start.IsZero() && !p.EndsAt.After(req.Start)) || (!req.End.IsZero() && !p.StartsAt.Before(req.End)) || (req.Query != "" && !strings.Contains(strings.ToLower(p.Title+" "+p.EpisodeTitle), strings.ToLower(req.Query))) {
				continue
			}
			programs = append(programs, p)
		}
	}
	offset, _ := strconv.Atoi(req.Cursor)
	limit := req.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	if offset > len(programs) {
		offset = len(programs)
	}
	end := offset + limit
	if end > len(programs) {
		end = len(programs)
	}
	next := ""
	if end < len(programs) {
		next = strconv.Itoa(end)
	}
	return media.GuidePage{Channels: channels, Programs: programs[offset:end], NextCursor: next}
}
