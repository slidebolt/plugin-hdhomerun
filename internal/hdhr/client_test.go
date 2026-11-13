package hdhr

import (
	"encoding/json"
	"testing"
)

func TestChannelDiagnosticsDecode(t *testing.T) {
	raw := `{"GuideNumber":"2.1","GuideName":"MNPBS","VideoCodec":"MPEG2","AudioCodec":"AC3","HD":1,"SignalStrength":87,"SignalQuality":97,"URL":"http://tuner/auto/v2.1"}`
	var got Channel
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if got.VideoCodec != "MPEG2" || got.AudioCodec != "AC3" || got.HD != 1 || got.SignalStrength != 87 || got.SignalQuality != 97 {
		t.Fatalf("got %+v", got)
	}
}
