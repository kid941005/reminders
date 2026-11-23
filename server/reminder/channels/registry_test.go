package channels

import (
	"context"
	"strings"
	"testing"

	"smallgo/server/reminder/core"

	"gorm.io/gorm"
)

type stubNotifier struct {
	channel string
	sent    int
}

func (s *stubNotifier) Channel() string { return s.channel }
func (s *stubNotifier) Send(ctx context.Context, msg core.OutboundMessage) (core.SendResult, error) {
	s.sent++
	return core.SendResult{ExternalID: "ok"}, nil
}

func TestDispatcherContract(t *testing.T) {
	reg := NewRegistry()
	var got *stubNotifier
	reg.Register("stub", func(_ *gorm.DB) core.Notifier {
		got = &stubNotifier{channel: "stub"}
		return got
	})
	d := NewDispatcher(reg)
	res, err := d.Dispatch(context.Background(), nil, core.OutboundMessage{Channel: "stub", Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExternalID != "ok" || got == nil || got.sent != 1 {
		t.Fatalf("dispatch did not reach notifier: res=%+v sent=%v", res, got)
	}
	if _, err := d.Dispatch(context.Background(), nil, core.OutboundMessage{Channel: "missing"}); err == nil || !strings.Contains(err.Error(), "未注册") {
		t.Fatalf("expected unregistered-channel error, got %v", err)
	}
	if names := reg.Channels(); len(names) != 1 || names[0] != "stub" {
		t.Fatalf("unexpected channel list %v", names)
	}
}
