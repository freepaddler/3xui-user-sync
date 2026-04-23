package xui

import (
	"testing"

	"github.com/chu/3xui-user-sync/internal/domain"
)

func TestToAPIClientFlow(t *testing.T) {
	t.Run("uses default flow for vless tcp", func(t *testing.T) {
		client := toAPIClient(domain.Inbound{
			Protocol: "vless",
			Network:  "tcp",
		}, domain.RemoteClient{
			UID:    "uid",
			Email:  "user@example.com",
			Enable: true,
		})

		if client.Flow != defaultFlow {
			t.Fatalf("expected default flow %q, got %q", defaultFlow, client.Flow)
		}
	})

	t.Run("preserves explicit flow for vless tcp", func(t *testing.T) {
		client := toAPIClient(domain.Inbound{
			Protocol: "vless",
			Network:  "tcp",
		}, domain.RemoteClient{
			UID:    "uid",
			Email:  "user@example.com",
			Enable: true,
			Flow:   "custom-flow",
		})

		if client.Flow != "custom-flow" {
			t.Fatalf("expected explicit flow to be preserved, got %q", client.Flow)
		}
	})

	t.Run("clears flow for non vless tcp inbound", func(t *testing.T) {
		client := toAPIClient(domain.Inbound{
			Protocol: "vless",
			Network:  "ws",
		}, domain.RemoteClient{
			UID:    "uid",
			Email:  "user@example.com",
			Enable: true,
			Flow:   defaultFlow,
		})

		if client.Flow != "" {
			t.Fatalf("expected empty flow, got %q", client.Flow)
		}
	})
}
