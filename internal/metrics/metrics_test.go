package metrics

import (
	"strings"
	"testing"
)

func TestWrite(t *testing.T) {
	r := New()
	r.Describe("events_total", "Events.")
	r.Inc("events_total", "event", "deploy")
	r.Inc("events_total", "event", "deploy")
	r.Inc("events_total", "event", "a\"b\\c\nd é")
	r.Inc("plain_total")
	r.Gauge("depth", "Queue depth.", func() float64 { return 3 })

	var b strings.Builder
	r.Write(&b)
	want := `# HELP events_total Events.
# TYPE events_total counter
events_total{event="a\"b\\c\nd é"} 1
events_total{event="deploy"} 2
# TYPE plain_total counter
plain_total 1
# HELP depth Queue depth.
# TYPE depth gauge
depth 3
`
	if b.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", b.String(), want)
	}
}
