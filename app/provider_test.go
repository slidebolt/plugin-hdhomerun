package app

import (
	"testing"
	"time"

	domain "github.com/slidebolt/sb-domain"
	media "github.com/slidebolt/sb-media-sdk"
)

func TestGuideFiltersByWindowAndQuery(t *testing.T) {
	now := time.Now()
	key := PluginID + ".television.channel-7-1"
	a := New()
	a.channelStates[key] = domain.TVChannel{Number: "7.1", Name: "Example", Available: true}
	a.programs[key] = []domain.TVProgram{{AiringID: "a", Title: "Morning News", StartsAt: now, EndsAt: now.Add(time.Hour)}, {AiringID: "b", Title: "Movie", StartsAt: now.Add(2 * time.Hour), EndsAt: now.Add(3 * time.Hour)}}
	page := a.Guide(media.GuideRequest{ServerID: "television", Start: now.Add(-time.Minute), End: now.Add(90 * time.Minute), Query: "news"})
	if len(page.Channels) != 1 || len(page.Programs) != 1 || page.Programs[0].AiringID != "a" {
		t.Fatalf("page = %#v", page)
	}
}
