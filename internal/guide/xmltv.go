package guide

import (
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	domain "github.com/slidebolt/sb-domain"
)

type TV struct {
	Channels []XMLChannel `xml:"channel"`
	Programs []XMLProgram `xml:"programme"`
}
type XMLChannel struct {
	ID    string   `xml:"id,attr"`
	Names []string `xml:"display-name"`
}
type XMLProgram struct {
	Channel        string          `xml:"channel,attr"`
	Start          string          `xml:"start,attr"`
	Stop           string          `xml:"stop,attr"`
	Title          string          `xml:"title"`
	Subtitle       string          `xml:"sub-title"`
	Desc           string          `xml:"desc"`
	EpisodeNumbers []EpisodeNumber `xml:"episode-num"`
	Categories     []string        `xml:"category"`
	New            *struct{}       `xml:"new"`
}
type EpisodeNumber struct {
	System string `xml:"system,attr"`
	Value  string `xml:",chardata"`
}

type Data struct {
	Programs map[string][]domain.TVProgram
	Names    map[string][]string
}

func Parse(r io.Reader) (Data, error) {
	var tv TV
	if err := xml.NewDecoder(r).Decode(&tv); err != nil {
		return Data{}, err
	}
	out := Data{Programs: map[string][]domain.TVProgram{}, Names: map[string][]string{}}
	for _, ch := range tv.Channels {
		out.Names[ch.ID] = ch.Names
	}
	for _, p := range tv.Programs {
		start, err := parseTime(p.Start)
		if err != nil {
			continue
		}
		stop, err := parseTime(p.Stop)
		if err != nil {
			continue
		}
		program := domain.TVProgram{AiringID: fmt.Sprintf("%s-%d", p.Channel, start.Unix()), Title: strings.TrimSpace(p.Title), EpisodeTitle: strings.TrimSpace(p.Subtitle), Synopsis: strings.TrimSpace(p.Desc), StartsAt: start, EndsAt: stop, IsNew: p.New != nil}
		for _, ep := range p.EpisodeNumbers {
			switch ep.System {
			case "xmltv_ns":
				parts := strings.Split(ep.Value, ".")
				if len(parts) > 0 {
					program.Season = parseIndex(parts[0])
				}
				if len(parts) > 1 {
					program.Episode = parseIndex(parts[1])
				}
			case "dd_progid":
				program.SeriesID = strings.Split(ep.Value, ".")[0]
			}
		}
		if program.SeriesID == "" {
			program.SeriesID = strings.ToLower(strings.ReplaceAll(program.Title, " ", "-"))
		}
		out.Programs[p.Channel] = append(out.Programs[p.Channel], program)
	}
	return out, nil
}
func parseIndex(v string) int { n, _ := strconv.Atoi(strings.TrimSpace(v)); return n + 1 }
func parseTime(v string) (time.Time, error) {
	for _, layout := range []string{"20060102150405 -0700", "20060102150405"} {
		if t, e := time.Parse(layout, strings.TrimSpace(v)); e == nil {
			return t, e
		}
	}
	return time.Time{}, fmt.Errorf("bad XMLTV time %q", v)
}
