// Package heartmula talks to the HeartMuLa music service (a .NET service), which runs on this
// machine or on another one, and finds out whether there is one to use. The service is optional: nothing here
// starts unless a project asks for the heartmula generator. What it shares with the other generators that run as a
// service is in internal/platform/musicsvc.
package heartmula

import "veritmo/internal/platform/musicsvc"

// Version is the version of the service this program is made for.
const Version = "1"

// Request is what the service takes to make a song.
type Request struct {
	Lyrics      string  `json:"lyrics"`
	Tags        string  `json:"tags"`
	MaxMS       int     `json:"max_ms,omitempty"`
	Seed        int64   `json:"seed"`
	Temperature float64 `json:"temperature,omitempty"`
	TopK        int     `json:"topk,omitempty"`
	CfgScale    float64 `json:"cfg_scale,omitempty"`
}

// The answers of the service and the errors the caller tells apart.
type (
	// GPU is one card as the service reports it (see [musicsvc.GPU]).
	GPU = musicsvc.GPU
	// Health is the answer of the service to its health check (see [musicsvc.Health]).
	Health = musicsvc.Health
	// Job is a song being made by the service (see [musicsvc.Job]).
	Job = musicsvc.Job
	// Client talks to a music service over HTTP (see [musicsvc.Client]).
	Client = musicsvc.Client
)

var (
	// ErrUnauthorized is the error of a service that refused the key.
	ErrUnauthorized = musicsvc.ErrUnauthorized
	// ErrUnreachable is the error of a service that cannot be reached.
	ErrUnreachable = musicsvc.ErrUnreachable
)

// NewClient returns a client for an address: a host:port or a URL.
func NewClient(address, token string) *Client { return musicsvc.NewClient("HeartMuLa", address, token) }

// BaseURL turns a host, a host:port or a URL into a URL without a trailing slash.
func BaseURL(address string) string { return musicsvc.BaseURL(address) }
