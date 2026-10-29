// Package metrics は GET /metrics に出す Prometheus のメトリクスを持つ。
//
// メトリクスの名前とラベルは、監視（Prometheus のアラート）との約束なので変えない（docs/api.md の
// 「メトリクス（`GET /metrics`）」）。ラベルには番組名・番組の名前を入れない。
//
//   - rajikura_last_saved_timestamp_seconds: 最後にジョブを保存した時刻（Unix 秒）。まだ無ければ 0
//   - rajikura_notify_failures_total{kind}: 通知先に送れなかった数。kind は数える通知の種類（テスト通知を除く。app.NotifyKinds）
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics はメトリクスの入れ物。nil のメソッド呼び出しは何もしない。
type Metrics struct {
	reg            *prometheus.Registry
	lastSaved      prometheus.Gauge
	notifyFailures *prometheus.CounterVec
}

// New は空のレジストリでメトリクスを作る。
func New() *Metrics {
	m := &Metrics{
		reg: prometheus.NewRegistry(),
		lastSaved: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "rajikura_last_saved_timestamp_seconds",
			Help: "Unix time when a job was last saved. 0 if none has been saved yet.",
		}),
		notifyFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rajikura_notify_failures_total",
			Help: "Number of notifications that could not be sent, by kind.",
		}, []string{"kind"}),
	}
	m.reg.MustRegister(m.lastSaved, m.notifyFailures)
	return m
}

// SetLastSaved は最後にジョブを保存した時刻を記録する。零値は 0 にする。
func (m *Metrics) SetLastSaved(t time.Time) {
	if m == nil {
		return
	}
	if t.IsZero() {
		m.lastSaved.Set(0)
		return
	}
	m.lastSaved.Set(float64(t.Unix()))
}

// NotifyFailed は kind の通知を送れなかったことを数える。
func (m *Metrics) NotifyFailed(kind string) {
	if m == nil {
		return
	}
	m.notifyFailures.WithLabelValues(kind).Inc()
}

// InitNotifyKinds は kinds の送れなかった数を 0 で出しておく（系列が無いと rate() や increase() が空になるため）。
func (m *Metrics) InitNotifyKinds(kinds ...string) {
	if m == nil {
		return
	}
	for _, k := range kinds {
		m.notifyFailures.WithLabelValues(k)
	}
}

// Handler は /metrics のハンドラ。m が nil なら 404 を返す。
func (m *Metrics) Handler() http.Handler {
	if m == nil {
		return http.NotFoundHandler()
	}
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}
