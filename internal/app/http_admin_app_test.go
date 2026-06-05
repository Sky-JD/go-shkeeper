package app

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestAdminAppPageServesEmbeddedVueShell(t *testing.T) {
	handler := &HTTPHandler{}
	req := httptest.NewRequest(http.MethodGet, "/wallets", nil)
	res := httptest.NewRecorder()

	handler.adminAppPage(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d", res.Code)
	}
	body := res.Body.String()
	for _, want := range []string{
		`id="app"`,
		`type="module"`,
		`/admin/assets/`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("admin app page missing %q", want)
		}
	}
	if strings.Contains(body, "vue.esm-browser") || strings.Contains(body, "unpkg.com") {
		t.Fatalf("admin app page should not depend on CDN Vue runtime")
	}
}

func TestAdminAssetServesBuiltVueBundle(t *testing.T) {
	handler := &HTTPHandler{}
	indexReq := httptest.NewRequest(http.MethodGet, "/wallets", nil)
	indexRes := httptest.NewRecorder()
	handler.adminAppPage(indexRes, indexReq)

	match := regexp.MustCompile(`src="(/admin/assets/[^"]+\.js)"`).FindStringSubmatch(indexRes.Body.String())
	if len(match) != 2 {
		t.Fatalf("cannot find built admin bundle in index")
	}

	req := httptest.NewRequest(http.MethodGet, match[1], nil)
	res := httptest.NewRecorder()
	handler.adminAsset(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d", res.Code)
	}
	if got := res.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Fatalf("Cache-Control=%q", got)
	}
	body := res.Body.String()
	for _, want := range []string{`/api/v1/admin/wallets`, `订单管理`, `提现管理`, `汇率自动设置`} {
		if !strings.Contains(body, want) {
			t.Fatalf("admin bundle missing %q", want)
		}
	}
}
