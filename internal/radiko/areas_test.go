package radiko

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// full.xml の station の area_id から、エリアの一覧（エリア ID の番号の順、重ねなし）と局→エリアの対応を作る。
func TestAreas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<region>
<stations region_id="kinki"><station><id>ABC</id><area_id>JP27</area_id></station><station><id>KBS</id><area_id>JP26</area_id></station></stations>
<stations region_id="kanto"><station><id>TBS</id><area_id>JP13</area_id></station><station><id>QRR</id><area_id>JP13</area_id></station>
<station><id>NOAREA</id></station><station><id>X</id><area_id>ZZ</area_id></station></stations>
</region>`))
	}))
	defer srv.Close()
	c := NewClient()
	c.ProgramHost = srv.URL
	got, err := c.Areas(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Area{{"JP13", "東京都"}, {"JP26", "京都府"}, {"JP27", "大阪府"}, {"ZZ", "ZZ"}}
	if !slices.Equal(got.Areas, want) {
		t.Errorf("Areas = %+v, want %+v", got.Areas, want)
	}
	if got.StationArea["ABC"] != "JP27" || got.StationArea["QRR"] != "JP13" || got.StationArea["NOAREA"] != "" || len(got.StationArea) != 5 {
		t.Errorf("StationArea = %v", got.StationArea)
	}
}

func TestAreaName(t *testing.T) {
	for id, want := range map[string]string{"JP1": "北海道", "JP13": "東京都", "JP47": "沖縄県", "JP48": "JP48", "JP0": "JP0", "x": "x"} {
		if got := AreaName(id); got != want {
			t.Errorf("AreaName(%q) = %q, want %q", id, got, want)
		}
	}
}
