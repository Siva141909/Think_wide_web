package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestServer() (*Store, http.Handler) {
	store := NewStore()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /events", handlePostEvents(store))
	mux.HandleFunc("GET /campaigns/{campaignID}/stats", handleGetStats(store))
	return store, mux
}

func postEvents(t *testing.T, h http.Handler, body string) (int, BatchResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/events", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var resp BatchResponse
	if rec.Code == http.StatusOK {
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decoding response: %v", err)
		}
	}
	return rec.Code, resp
}

func TestPostEvents_NonArrayBodyIs400(t *testing.T) {
	_, h := newTestServer()
	cases := map[string]string{
		"object":     `{"event_id":"e1"}`,
		"number":     `42`,
		"string":     `"hello"`,
		"null":       `null`,
		"garbage":    `not json at all`,
		"empty body": ``,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/events", strings.NewReader(body))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s: status = %d, want 400", name, rec.Code)
			}
		})
	}
}

func TestPostEvents_EmptyArrayIs200(t *testing.T) {
	_, h := newTestServer()
	code, resp := postEvents(t, h, `[]`)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if resp.Accepted != 0 || len(resp.Results) != 0 {
		t.Fatalf("expected an empty, all-zero response, got %+v", resp)
	}
}

func TestPostEvents_MixedBatchNeverFailsWholeRequest(t *testing.T) {
	_, h := newTestServer()
	body := `[
		{"event_id":"e1","campaign_id":"cmp_a","contact_id":"ct_1","type":"sent","timestamp":"2026-08-10T06:15:00Z"},
		{"event_id":"e2","campaign_id":"cmp_a","type":"delivered","timestamp":"2026-08-10T06:16:00Z"},
		{"event_id":"e3","campaign_id":"cmp_a","contact_id":"ct_2","type":"clicked","timestamp":"2026-08-10T06:17:00Z"}
	]`
	code, resp := postEvents(t, h, body)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even though element 2 is invalid", code)
	}
	if resp.Accepted != 2 {
		t.Fatalf("accepted = %d, want 2", resp.Accepted)
	}
	if resp.Rejected != 1 {
		t.Fatalf("rejected = %d, want 1", resp.Rejected)
	}
	if resp.Results[1].Status != StatusRejected {
		t.Fatalf("element 1 (missing contact_id): status = %q, want rejected", resp.Results[1].Status)
	}
	if resp.Results[0].Status != StatusAccepted || resp.Results[2].Status != StatusAccepted {
		t.Fatalf("valid siblings must still be accepted: %+v", resp.Results)
	}
}

func TestPostEvents_MalformedJSONElementIsRejectedNotFatal(t *testing.T) {
	_, h := newTestServer()
	// second element has a timestamp given as a number, not a string —
	// decoding that single element fails, the rest must still process.
	body := `[
		{"event_id":"e1","campaign_id":"cmp_a","contact_id":"ct_1","type":"sent","timestamp":"2026-08-10T06:15:00Z"},
		{"event_id":"e2","campaign_id":"cmp_a","contact_id":"ct_1","type":"delivered","timestamp":12345},
		{"event_id":"e3","campaign_id":"cmp_a","contact_id":"ct_2","type":"clicked","timestamp":"2026-08-10T06:17:00Z"}
	]`
	code, resp := postEvents(t, h, body)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if resp.Accepted != 2 || resp.Rejected != 1 {
		t.Fatalf("accepted=%d rejected=%d, want 2 and 1: %+v", resp.Accepted, resp.Rejected, resp.Results)
	}
}

func TestGetStats_UnknownCampaignIs404(t *testing.T) {
	_, h := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/campaigns/does-not-exist/stats", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestGetStats_ReflectsPostedEvents(t *testing.T) {
	_, h := newTestServer()
	body := `[
		{"event_id":"e1","campaign_id":"cmp_a","contact_id":"ct_1","type":"sent","timestamp":"2026-08-10T06:15:00Z"},
		{"event_id":"e2","campaign_id":"cmp_a","contact_id":"ct_1","type":"delivered","timestamp":"2026-08-10T06:16:00Z"},
		{"event_id":"e3","campaign_id":"cmp_a","contact_id":"ct_1","type":"opened","timestamp":"2026-08-10T06:17:00Z"}
	]`
	if code, _ := postEvents(t, h, body); code != http.StatusOK {
		t.Fatalf("seeding events: status = %d", code)
	}

	req := httptest.NewRequest(http.MethodGet, "/campaigns/cmp_a/stats", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var stats StatsResponse
	if err := json.NewDecoder(rec.Body).Decode(&stats); err != nil {
		t.Fatalf("decoding stats: %v", err)
	}
	if stats.Sent != 1 || stats.Delivered != 1 || stats.Opened != 1 || stats.UniqueOpens != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestPostEvents_DuplicateAcrossTwoRequests(t *testing.T) {
	_, h := newTestServer()
	body := `[{"event_id":"e1","campaign_id":"cmp_a","contact_id":"ct_1","type":"sent","timestamp":"2026-08-10T06:15:00Z"}]`

	code1, resp1 := postEvents(t, h, body)
	code2, resp2 := postEvents(t, h, body)
	if code1 != http.StatusOK || code2 != http.StatusOK {
		t.Fatalf("expected 200/200, got %d/%d", code1, code2)
	}
	if resp1.Results[0].Status != StatusAccepted {
		t.Fatalf("first POST: status = %q, want accepted", resp1.Results[0].Status)
	}
	if resp2.Results[0].Status != StatusDuplicate {
		t.Fatalf("second POST (retry): status = %q, want duplicate", resp2.Results[0].Status)
	}

	req := httptest.NewRequest(http.MethodGet, "/campaigns/cmp_a/stats", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var stats StatsResponse
	json.NewDecoder(rec.Body).Decode(&stats)
	if stats.Sent != 1 {
		t.Fatalf("sent = %d, want 1 (retry must not double count)", stats.Sent)
	}
}

func TestPostEvents_ResponseIsJSON(t *testing.T) {
	_, h := newTestServer()
	req := httptest.NewRequest(http.MethodPost, "/events", bytes.NewReader([]byte(`[]`)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
}
