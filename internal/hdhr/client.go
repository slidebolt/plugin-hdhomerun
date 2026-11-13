package hdhr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Discover struct {
	FriendlyName string `json:"FriendlyName"`
	ModelNumber  string `json:"ModelNumber"`
	DeviceID     string `json:"DeviceID"`
	DeviceAuth   string `json:"DeviceAuth"`
	BaseURL      string `json:"BaseURL"`
	LineupURL    string `json:"LineupURL"`
	TunerCount   int    `json:"TunerCount"`
}

type Channel struct {
	GuideNumber    string `json:"GuideNumber"`
	GuideName      string `json:"GuideName"`
	URL            string `json:"URL"`
	Tags           string `json:"Tags"`
	VideoCodec     string `json:"VideoCodec"`
	AudioCodec     string `json:"AudioCodec"`
	HD             int    `json:"HD"`
	SignalStrength int    `json:"SignalStrength"`
	SignalQuality  int    `json:"SignalQuality"`
}

type Client struct{ HTTP *http.Client }

func New() *Client { return &Client{HTTP: &http.Client{Timeout: 30 * time.Second}} }

func (c *Client) Discover(ctx context.Context, base string) (Discover, error) {
	var out Discover
	if err := c.getJSON(ctx, strings.TrimRight(base, "/")+"/discover.json", &out); err != nil {
		return out, err
	}
	if out.BaseURL == "" {
		out.BaseURL = strings.TrimRight(base, "/")
	}
	if out.LineupURL == "" {
		out.LineupURL = out.BaseURL + "/lineup.json"
	}
	if out.DeviceID == "" {
		return out, fmt.Errorf("discover response missing DeviceID")
	}
	return out, nil
}

func (c *Client) Lineup(ctx context.Context, url string) ([]Channel, error) {
	var out []Channel
	if err := c.getJSON(ctx, url, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
