// Package api is the agent's side of the QuickTable API's /print-agent routes.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/quicktable/print-agent/internal/escpos"
	"github.com/quicktable/print-agent/internal/update"
)

// ErrUnauthorized means the agent's token is no longer good: it was unpaired.
var ErrUnauthorized = errors.New("the agent is not paired (anymore)")

// ErrNotFound means the API doesn't know what was asked for — for an install,
// a code that isn't good (anymore).
var ErrNotFound = errors.New("not found")

// ErrNotHeld means the job isn't this agent's anymore (its lease ran out, or
// it was cancelled): there is nothing to report.
var ErrNotHeld = errors.New("the job is not held by this agent")

// Slack on top of a long-poll's wait before the request is given up.
const longPollSlack = 15 * time.Second

// Client talks to one API with one agent's token.
type Client struct {
	baseURL string
	token   string
	version string
	http    *http.Client
}

// New returns a client for the API at baseURL. token is empty until the install is claimed.
func New(baseURL, token, version string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		version: version,
		http:    &http.Client{},
	}
}

// Printer is where a job prints.
type Printer struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Connection   string `json:"connection"`
	Address      string `json:"address"`
	PaperWidthMm int    `json:"paperWidthMm"`
}

// Job is a claimed print job.
type Job struct {
	ID      string        `json:"id"`
	Attempt int           `json:"attempt"`
	Reprint bool          `json:"reprint"`
	Copies  int           `json:"copies"`
	Printer Printer       `json:"printer"`
	Ticket  escpos.Ticket `json:"ticket"`
}

// ReportedPrinter is a printer Windows has installed, as the heartbeat reports it.
type ReportedPrinter struct {
	Name   string `json:"name"`
	Status string `json:"status,omitempty"`
}

// ClaimInstall trades an install's code — the long one from the program's
// file name, or the short one the admin shows — for the agent's token.
// ErrNotFound means the code isn't good (anymore).
func (c *Client) ClaimInstall(ctx context.Context, code string) (string, error) {
	var claimed struct {
		Token string `json:"token"`
	}
	err := c.post(ctx, "/print-agent/claim", map[string]string{"code": code}, &claimed, 30*time.Second)
	return claimed.Token, err
}

// UpdateFailure is an update the agent tried and undid.
type UpdateFailure struct {
	Version string `json:"version"`
	Error   string `json:"error"`
}

// UpdateWindow is when the agent may update on its own: minutes from
// midnight on the PC's clock. Start after End wraps past midnight.
type UpdateWindow struct {
	StartMinute int `json:"startMinute"`
	EndMinute   int `json:"endMinute"`
}

// HeartbeatResult is the API's answer to a heartbeat.
type HeartbeatResult struct {
	// Update is the release the agent should move to; nil when it runs what it should.
	Update       *update.Release `json:"update"`
	UpdateWindow UpdateWindow    `json:"updateWindow"`
}

// Heartbeat tells the API the agent is alive, which printers its PC has and
// (failure, optional) an update it couldn't apply; the answer says whether
// there's a release to update to.
func (c *Client) Heartbeat(ctx context.Context, printers []ReportedPrinter, failure *UpdateFailure) (HeartbeatResult, error) {
	if printers == nil {
		printers = []ReportedPrinter{}
	}
	body := map[string]any{"version": c.version, "printers": printers}
	if failure != nil {
		body["updateFailure"] = failure
	}
	var result HeartbeatResult
	err := c.post(ctx, "/print-agent/heartbeat", body, &result, 30*time.Second)
	return result, err
}

// Claim long-polls for jobs: it returns as soon as there are some, or empty
// after about wait.
func (c *Client) Claim(ctx context.Context, wait time.Duration) ([]Job, error) {
	var jobs []Job
	body := map[string]int{"wait": int(wait.Seconds())}
	err := c.post(ctx, "/print-agent/jobs/claim", body, &jobs, wait+longPollSlack)
	return jobs, err
}

// Report settles a job: printed when printErr is nil, failed otherwise.
func (c *Client) Report(ctx context.Context, jobID string, printErr error) error {
	body := map[string]string{"status": "PRINTED"}
	if printErr != nil {
		body = map[string]string{"status": "FAILED", "error": printErr.Error()}
	}
	return c.post(ctx, "/print-agent/jobs/"+jobID+"/result", body, nil, 30*time.Second)
}

// post sends body as JSON and decodes the response's "data" into out.
func (c *Client) post(ctx context.Context, path string, body, out any, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	payload := []byte("{}")
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "quicktable-print-agent/"+c.version)
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}

	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return err
	}

	switch {
	case response.StatusCode == http.StatusUnauthorized:
		return ErrUnauthorized
	case response.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case response.StatusCode == http.StatusConflict:
		return ErrNotHeld
	case response.StatusCode >= 300:
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &failure)
		return fmt.Errorf("%s: HTTP %d %s", path, response.StatusCode, failure.Error)
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	envelope := struct {
		Data any `json:"data"`
	}{Data: out}
	return json.Unmarshal(raw, &envelope)
}
