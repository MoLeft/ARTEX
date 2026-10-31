package server

import (
	"context"
	"errors"
	"testing"

	"github.com/Autumn-27/artex/agent"
)

func TestProxyFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"proxy connect", errors.New("proxyconnect tcp: connection refused"), true},
		{"reset", errors.New("dial tcp: connection reset by peer"), true},
		{"quota", errors.New("status 429: insufficient_quota"), false},
		{"auth", errors.New("status 401: invalid api key"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isProxyFailure(tc.err); got != tc.want {
				t.Fatalf("isProxyFailure(%v)=%v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestProxyPauseCauseIsDistinct(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(agent.AbortPausedByProxy)
	code, short, _, ok := agent.AbortReason(ctx)
	if !ok || code != "paused_by_proxy" || short == "" {
		t.Fatalf("proxy cause = %q/%q/%v", code, short, ok)
	}
}

func TestProxyReconnectReasonGate(t *testing.T) {
	for _, tc := range []struct {
		reason string
		want   bool
	}{
		{"paused_by_proxy", true},
		{"paused_by_user", false},
		{"paused_by_schedule", false},
		{"paused_by_orchestrator", false},
	} {
		got := tc.reason == "paused_by_proxy"
		if got != tc.want {
			t.Errorf("reason %q gate=%v, want %v", tc.reason, got, tc.want)
		}
	}
}
