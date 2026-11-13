package app

import (
	"net/url"
	"testing"

	domain "github.com/slidebolt/sb-domain"
)

func TestGuideURLUsesLatestAuthPerDeviceInStableOrder(t *testing.T) {
	a := New()
	a.deviceIDs["B"] = struct{}{}
	a.deviceIDs["A"] = struct{}{}
	a.deviceAuth["A"] = "new-a"
	a.deviceAuth["B"] = "new-b"
	got, err := url.Parse(a.guideURL())
	if err != nil {
		t.Fatal(err)
	}
	if value := got.Query().Get("DeviceAuth"); value != "new-anew-b" {
		t.Fatalf("DeviceAuth = %q", value)
	}
}

func TestGuideURLPrecedence(t *testing.T) {
	a := New()
	a.config.XMLTVURL = "https://guide.invalid/custom.xml"
	a.config.GuideEmail = "person@example.invalid"
	a.deviceIDs["A"] = struct{}{}
	a.deviceAuth["A"] = "auth"
	if got := a.guideURL(); got != a.config.XMLTVURL {
		t.Fatalf("explicit URL = %q", got)
	}

	a.config.XMLTVURL = ""
	got, err := url.Parse(a.guideURL())
	if err != nil {
		t.Fatal(err)
	}
	if got.Query().Get("Email") != a.config.GuideEmail || got.Query().Get("DeviceID") != "A" || got.Query().Has("DeviceAuth") {
		t.Fatalf("email guide query = %q", got.RawQuery)
	}
}

func TestChannelEntityBelongsToHardwareDevice(t *testing.T) {
	entity := channelEntity(PluginID+".104E2170.channel-5-1", domain.TVChannel{Number: "5.1", Name: "Example"})
	if entity.DeviceID != "104E2170" || entity.ID != "channel-5-1" {
		t.Fatalf("entity = %+v", entity)
	}
}
