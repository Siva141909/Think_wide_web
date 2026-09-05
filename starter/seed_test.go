package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// TestSeedFile_Reconciles feeds the entire provider-traffic seed through
// POST /events and checks the resulting batch classification counts and
// per-campaign stats against numbers independently computed from the
// same file under the validation/dedup rules documented in NOTES.md.
// This is our equivalent of the debugging exercise's expected_output.txt
// for the live service.
func TestSeedFile_Reconciles(t *testing.T) {
	body, err := os.ReadFile("seed/events.json")
	if err != nil {
		t.Fatalf("reading seed file: %v", err)
	}

	_, h := newTestServer()
	req := httptest.NewRequest(http.MethodPost, "/events", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp BatchResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding batch response: %v", err)
	}

	wantAccepted, wantDuplicate, wantConflict, wantRejected := 210, 15, 3, 7
	if resp.Accepted != wantAccepted || resp.Duplicate != wantDuplicate ||
		resp.Conflict != wantConflict || resp.Rejected != wantRejected {
		t.Fatalf("batch outcome = {accepted:%d duplicate:%d conflict:%d rejected:%d}, want {%d %d %d %d}",
			resp.Accepted, resp.Duplicate, resp.Conflict, resp.Rejected,
			wantAccepted, wantDuplicate, wantConflict, wantRejected)
	}

	wantStats := map[string]StatsResponse{
		"cmp_summer_sale": {Sent: 40, Delivered: 35, Opened: 27, UniqueOpens: 19, Clicked: 12},
		"cmp_welcome":     {Sent: 20, Delivered: 19, Opened: 20, UniqueOpens: 12, Clicked: 3},
		"cmp_winback":     {Sent: 12, Delivered: 11, Opened: 7, UniqueOpens: 6, Clicked: 4},
	}
	for campaignID, want := range wantStats {
		getReq := httptest.NewRequest(http.MethodGet, "/campaigns/"+campaignID+"/stats", nil)
		getRec := httptest.NewRecorder()
		h.ServeHTTP(getRec, getReq)
		if getRec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", campaignID, getRec.Code)
		}
		var got StatsResponse
		if err := json.NewDecoder(getRec.Body).Decode(&got); err != nil {
			t.Fatalf("%s: decoding stats: %v", campaignID, err)
		}
		if got.Sent != want.Sent || got.Delivered != want.Delivered || got.Opened != want.Opened ||
			got.UniqueOpens != want.UniqueOpens || got.Clicked != want.Clicked {
			t.Fatalf("%s: got %+v, want %+v", campaignID, got, want)
		}
	}
}
