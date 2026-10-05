package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// server answers each path with the given status and body, recording requests.
type server struct {
	*httptest.Server
	requests []recorded
}

type recorded struct {
	path, authorization string
	body                map[string]any
}

func newServer(t *testing.T, answers map[string]func() (int, string)) *server {
	s := &server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		s.requests = append(s.requests, recorded{r.URL.Path, r.Header.Get("Authorization"), body})
		answer, ok := answers[r.URL.Path]
		if !ok {
			t.Errorf("unexpected request to %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		status, payload := answer()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestClaimInstall(t *testing.T) {
	status := 200
	s := newServer(t, map[string]func() (int, string){
		"/print-agent/claim": func() (int, string) {
			if status != 200 {
				return status, `{"error":"Invalid or expired code"}`
			}
			return 200, `{"data":{"token":"jwt"}}`
		},
	})
	client := New(s.URL+"/", "", "1.0.0")

	token, err := client.ClaimInstall(context.Background(), "K7MPQ2XD")
	if err != nil || token != "jwt" {
		t.Fatalf("got %q, %v", token, err)
	}
	if s.requests[0].authorization != "" || s.requests[0].body["code"] != "K7MPQ2XD" {
		t.Fatalf("request: %+v", s.requests[0])
	}

	status = 404
	if _, err := client.ClaimInstall(context.Background(), "WRONG"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a code that isn't good: got %v", err)
	}
}

func TestClaimDecodesJobs(t *testing.T) {
	s := newServer(t, map[string]func() (int, string){
		"/print-agent/jobs/claim": func() (int, string) {
			return 200, `{"data":[{"id":"job-1","attempt":2,"reprint":true,"copies":2,
				"printer":{"id":"p1","name":"Kitchen","connection":"NETWORK","address":"10.0.0.1:9100","paperWidthMm":58},
				"ticket":{"kind":"order","title":"Cocina","tableNumber":5,"orderNumber":42,"people":null,
					"placedAt":"2026-10-01T12:00:00.000Z","note":null,"continuation":false,
					"lines":[{"quantity":2,"name":"Burger","subProducts":[{"name":"Cheese","quantity":1}],"meatPoint":"MEDIUM","servingTime":null,"note":null}]}}]}`
		},
	})
	client := New(s.URL, "token", "1.0.0")

	jobs, retryAfter, err := client.Claim(context.Background(), 25*time.Second)
	if err != nil || len(jobs) != 1 || retryAfter != 0 {
		t.Fatalf("got %+v, retry after %v, %v", jobs, retryAfter, err)
	}
	job := jobs[0]
	if job.ID != "job-1" || !job.Reprint || job.Copies != 2 || job.Printer.PaperWidthMm != 58 {
		t.Errorf("job: %+v", job)
	}
	if *job.Ticket.TableNumber != 5 || job.Ticket.People != nil || job.Ticket.Note != "" {
		t.Errorf("ticket: %+v", job.Ticket)
	}
	if line := job.Ticket.Lines[0]; line.MeatPoint != "MEDIUM" || line.ServingTime != "" || line.SubProducts[0].Name != "Cheese" {
		t.Errorf("line: %+v", line)
	}
	if got := s.requests[0]; got.authorization != "Bearer token" || got.body["wait"] != float64(25) || got.body["idleOk"] != true {
		t.Errorf("request: %+v", got)
	}
}

func TestClaimToldToStayAway(t *testing.T) {
	s := newServer(t, map[string]func() (int, string){
		"/print-agent/jobs/claim": func() (int, string) { return 200, `{"data":[],"meta":{"retryAfterSeconds":10}}` },
	})

	jobs, retryAfter, err := New(s.URL, "token", "1.0.0").Claim(context.Background(), 25*time.Second)

	if err != nil || len(jobs) != 0 || retryAfter != 10*time.Second {
		t.Fatalf("got %+v, retry after %v, %v", jobs, retryAfter, err)
	}
}

func TestReportAndHeartbeat(t *testing.T) {
	s := newServer(t, map[string]func() (int, string){
		"/print-agent/jobs/job-1/result": func() (int, string) { return 200, `{"data":{"id":"job-1","status":"PRINTED"}}` },
		"/print-agent/heartbeat": func() (int, string) {
			return 200, `{"data":{"update":{"version":"1.1.0","url":"https://x/agent.exe","sha256":"abc",
				"signatures":[{"keyId":"k","signature":"c2ln"}],"manifest":{}},"updateWindow":{"startMinute":180,"endMinute":360}}}`
		},
	})
	client := New(s.URL, "token", "1.0.0")

	if err := client.Report(context.Background(), "job-1", nil); err != nil {
		t.Fatal(err)
	}
	if err := client.Report(context.Background(), "job-1", errors.New("printer offline")); err != nil {
		t.Fatal(err)
	}
	beat, err := client.Heartbeat(context.Background(), nil, &UpdateFailure{Version: "1.0.9", Error: "it did not start"})
	if err != nil {
		t.Fatal(err)
	}
	if beat.Update == nil || beat.Update.Version != "1.1.0" || beat.Update.Signatures[0].KeyID != "k" {
		t.Errorf("update: %+v", beat.Update)
	}
	if beat.UpdateWindow != (UpdateWindow{StartMinute: 180, EndMinute: 360}) {
		t.Errorf("window: %+v", beat.UpdateWindow)
	}

	if s.requests[0].body["status"] != "PRINTED" {
		t.Errorf("printed report: %+v", s.requests[0].body)
	}
	if got := s.requests[1].body; got["status"] != "FAILED" || got["error"] != "printer offline" {
		t.Errorf("failed report: %+v", got)
	}
	if got := s.requests[2].body; got["version"] != "1.0.0" || got["printers"] == nil || got["updateFailure"] == nil {
		t.Errorf("heartbeat: %+v", got)
	}
}

func TestErrors(t *testing.T) {
	status := 401
	s := newServer(t, map[string]func() (int, string){
		"/print-agent/jobs/claim":        func() (int, string) { return status, `{"error":"Invalid or expired token"}` },
		"/print-agent/jobs/job-1/result": func() (int, string) { return 409, `{"error":"not held"}` },
	})
	client := New(s.URL, "token", "1.0.0")

	if _, _, err := client.Claim(context.Background(), 0); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("401: got %v", err)
	}
	status = 502
	if _, _, err := client.Claim(context.Background(), 0); err == nil || errors.Is(err, ErrUnauthorized) {
		t.Errorf("502: got %v", err)
	}
	if err := client.Report(context.Background(), "job-1", nil); !errors.Is(err, ErrNotHeld) {
		t.Errorf("409: got %v", err)
	}
}
