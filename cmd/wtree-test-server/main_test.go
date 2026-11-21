package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIndependentInstancesIdentifyThemselves(t *testing.T) {
	first := httptest.NewServer(newHandler("first"))
	defer first.Close()
	second := httptest.NewServer(newHandler("second"))
	defer second.Close()

	for _, instance := range []struct {
		url  string
		name string
	}{{first.URL, "first"}, {second.URL, "second"}} {
		resp, err := http.Get(instance.url + "/probe")
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Instance string `json:"instance"`
			Host     string `json:"host"`
			Path     string `json:"path"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK || body.Instance != instance.name || body.Host == "" || body.Path != "/probe" {
			t.Fatalf("%s: status=%d body=%+v", instance.name, resp.StatusCode, body)
		}
	}
}
