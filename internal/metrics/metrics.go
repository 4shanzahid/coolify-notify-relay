// Package metrics is a minimal Prometheus text-format counter registry.
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

// labelEscaper applies the Prometheus text-format escapes for label values,
// which differ from Go's %q (no \uXXXX or \xNN escapes).
var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// Registry holds counters and gauges and renders them in the Prometheus
// text exposition format.
type Registry struct {
	mu       sync.Mutex
	counters map[string]map[string]uint64 // name → rendered labels → value
	help     map[string]string
	gauges   map[string]func() float64
}

// New returns an empty Registry.
func New() *Registry {
	return &Registry{
		counters: map[string]map[string]uint64{},
		help:     map[string]string{},
		gauges:   map[string]func() float64{},
	}
}

// Describe sets the HELP text for a metric.
func (r *Registry) Describe(name, help string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.help[name] = help
}

// Inc increments counter name; labels are key, value pairs.
func (r *Registry) Inc(name string, labels ...string) {
	var b strings.Builder
	for i := 0; i+1 < len(labels); i += 2 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(labels[i])
		b.WriteString(`="`)
		b.WriteString(labelEscaper.Replace(labels[i+1]))
		b.WriteByte('"')
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.counters[name] == nil {
		r.counters[name] = map[string]uint64{}
	}
	r.counters[name][b.String()]++
}

// Gauge registers a gauge whose value is read from fn at scrape time.
func (r *Registry) Gauge(name, help string, fn func() float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.help[name] = help
	r.gauges[name] = fn
}

// Write renders all metrics in the Prometheus text format.
func (r *Registry) Write(w io.Writer) {
	r.mu.Lock()
	defer r.mu.Unlock()

	names := make([]string, 0, len(r.counters))
	for n := range r.counters {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if h := r.help[n]; h != "" {
			fmt.Fprintf(w, "# HELP %s %s\n", n, h)
		}
		fmt.Fprintf(w, "# TYPE %s counter\n", n)
		series := r.counters[n]
		keys := make([]string, 0, len(series))
		for k := range series {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if k == "" {
				fmt.Fprintf(w, "%s %d\n", n, series[k])
			} else {
				fmt.Fprintf(w, "%s{%s} %d\n", n, k, series[k])
			}
		}
	}

	gnames := make([]string, 0, len(r.gauges))
	for n := range r.gauges {
		gnames = append(gnames, n)
	}
	sort.Strings(gnames)
	for _, n := range gnames {
		if h := r.help[n]; h != "" {
			fmt.Fprintf(w, "# HELP %s %s\n", n, h)
		}
		fmt.Fprintf(w, "# TYPE %s gauge\n%s %g\n", n, n, r.gauges[n]())
	}
}
