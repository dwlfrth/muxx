package metrics

import (
	"net/http"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"

	"github.com/mjl-/mox/moxvar"
)

// Handler returns the HTTP handler serving the prometheus metrics. For builds as a
// distribution, the metrics of mox, named "mox_...", are served with the product
// name instead, e.g. "muxx_...". The metrics are registered under their mox names,
// so packages need no changes.
func Handler() http.Handler {
	product := moxvar.ProductName()
	if product == "mox" {
		return promhttp.Handler()
	}
	gatherer := prometheus.GathererFunc(func() ([]*dto.MetricFamily, error) {
		families, err := prometheus.DefaultGatherer.Gather()
		renameFamilies(families, product)
		return families, err
	})
	return promhttp.InstrumentMetricHandler(prometheus.DefaultRegisterer, promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{}))
}

// renameFamilies replaces the "mox_" prefix of metric names with the product name.
func renameFamilies(families []*dto.MetricFamily, product string) {
	for _, f := range families {
		if name, ok := strings.CutPrefix(f.GetName(), "mox_"); ok {
			n := product + "_" + name
			f.Name = &n
		}
	}
}
