package app

import (
	"strconv"
	"strings"

	domain "github.com/slidebolt/sb-domain"
	media "github.com/slidebolt/sb-media-sdk"
)

func (a *App) Browse(media.CatalogRequest) media.CatalogPage {
	return media.CatalogPage{Error: "HDHomeRun does not provide a media library catalog"}
}
func (a *App) Artwork(media.ArtworkRequest) media.ArtworkReply {
	return media.ArtworkReply{Error: "HDHomeRun guide artwork is not available"}
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
