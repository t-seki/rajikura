package app

import (
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/t-seki/rajikura/internal/metrics"
)

// scrape は /metrics の本文を返す。
func scrape(t *testing.T, m *metrics.Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	return rec.Body.String()
}

// formatSci は Prometheus のテキスト形式の浮動小数の書き方（大きい値は指数表記）にそろえる。
func formatSci(n int64) string {
	return strconv.FormatFloat(float64(n), 'g', -1, 64)
}

// ジョブを保存したら、保存した時刻をメトリクスと DB に記録する。起動し直しても（LoadMetrics）0 に戻さない。
func TestMetricsLastSaved(t *testing.T) {
	e := newFlowEnv(t)
	e.app.Metrics = metrics.New()
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := scrape(t, e.app.Metrics); !strings.Contains(got, "rajikura_last_saved_timestamp_seconds 0\n") {
		t.Fatalf("保存前 = %s", got)
	}
	if !e.runDue() {
		t.Fatal("実行されない")
	}
	want := e.clock.Now().Unix()
	line := "rajikura_last_saved_timestamp_seconds " + formatSci(want) + "\n"
	if got := scrape(t, e.app.Metrics); !strings.Contains(got, line) {
		t.Fatalf("保存後 = %s（want %q）", got, line)
	}

	// 起動し直した App（新しいメトリクス）が DB から読む。
	m := metrics.New()
	e.app.Metrics = m
	if err := e.app.LoadMetrics(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := scrape(t, m); !strings.Contains(got, line) {
		t.Fatalf("再読み込み後 = %s（want %q）", got, line)
	}
}

// 作ってから 1 時間を過ぎた通知を送ろうとして送れず諦めたら（「送れなかった」の印）、kind ごとに数える。
// 1 時間を過ぎる前の、送り直している間は数えない。
func TestMetricsNotifyFailures(t *testing.T) {
	e := newFlowEnv(t)
	e.app.Metrics = metrics.New()
	e.app.Metrics.InitNotifyKinds(NotifyKinds...)
	d := e.useDiscord()
	d.setFail(true)
	if err := e.app.Startup(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.fake.set(func(f *fakeRadiko) { f.noTimefree = true })
	e.runDue()
	e.runDue()
	for range 3 {
		e.checkNotifications()
	}
	want0 := `rajikura_notify_failures_total{kind="expired"} 0` + "\n"
	if got := scrape(t, e.app.Metrics); !strings.Contains(got, want0) {
		t.Fatalf("送り直している間に数えた: %s", got)
	}

	e.clock.Set(e.clock.Now().Add(time.Hour))
	e.checkNotifications()
	e.checkNotifications()
	got := scrape(t, e.app.Metrics)
	for _, want := range []string{
		`rajikura_notify_failures_total{kind="expired"} 2` + "\n",
		`rajikura_notify_failures_total{kind="recorded"} 0` + "\n",
		`rajikura_notify_failures_total{kind="recovered"} 0` + "\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("%q が無い: %s", want, got)
		}
	}
	if strings.Contains(got, "daily_summary") {
		t.Errorf("やめた日次サマリの kind が出ている: %s", got)
	}
}
