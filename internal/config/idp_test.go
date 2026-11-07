package config

import "testing"

func TestNormalizeIdP(t *testing.T) {
	got, err := NormalizeIdP(IdP{Issuer: " https://idp.example/ ", ClientID: " rajikura ", WebURL: "https://rajikura.example:8443/"})
	if err != nil {
		t.Fatal(err)
	}
	want := IdP{Issuer: "https://idp.example/", ClientID: "rajikura", WebURL: "https://rajikura.example:8443"}
	if got != want {
		t.Errorf("got %+v、want %+v", got, want)
	}
	if got.RedirectURL() != "https://rajikura.example:8443/api/idp/callback" || got.WebHost() != "rajikura.example:8443" ||
		got.IssuerHostname() != "idp.example" {
		t.Errorf("RedirectURL %q WebHost %q IssuerHostname %q", got.RedirectURL(), got.WebHost(), got.IssuerHostname())
	}
	if empty, err := NormalizeIdP(IdP{}); err != nil || empty.Configured() || empty.RedirectURL() != "" {
		t.Errorf("空 = %+v, %v", empty, err)
	}
	for _, p := range []IdP{
		{Issuer: "https://idp.example", ClientID: "a"},
		{Issuer: "idp.example", ClientID: "a", WebURL: "https://r.example"},
		{Issuer: "https://idp.example", ClientID: "a", WebURL: "https://r.example/ui"},
		{Issuer: "https://idp.example?x=1", ClientID: "a", WebURL: "https://r.example"},
		{Issuer: "https://idp.example", ClientID: "a", WebURL: "https://u:p@r.example"},
		{Issuer: "https://idp.example", ClientID: "a", WebURL: "https://r.example#x"},
		{Issuer: "https://", ClientID: "a", WebURL: "https://r.example"},
	} {
		if _, err := NormalizeIdP(p); err == nil {
			t.Errorf("%+v を受け付けた", p)
		}
	}
}
