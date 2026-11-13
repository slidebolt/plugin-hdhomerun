package guide

import (
	"strings"
	"testing"
)

func TestParseProgram(t *testing.T) {
	xml := `<tv><channel id="5.1"><display-name>5.1</display-name></channel><programme channel="5.1" start="20261112190000 -0600" stop="20261112200000 -0600"><title>Nova</title><sub-title>Ancient Builders</sub-title><episode-num system="xmltv_ns">1.4.</episode-num><episode-num system="dd_progid">EP123.0005</episode-num><new/></programme></tv>`
	data, err := Parse(strings.NewReader(xml))
	if err != nil {
		t.Fatal(err)
	}
	programs := data.Programs["5.1"]
	if len(programs) != 1 {
		t.Fatalf("programs=%d", len(programs))
	}
	got := programs[0]
	if got.Title != "Nova" || got.Season != 2 || got.Episode != 5 || got.SeriesID != "EP123" || !got.IsNew {
		t.Fatalf("got %+v", got)
	}
}
