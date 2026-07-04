package sickan

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

type recordLogger struct {
	lines []string
}

func (r *recordLogger) Logf(format string, args ...any) {
	r.lines = append(r.lines, format)
}
func (r *recordLogger) RecordUsage(model string, in, out, cc, cr int64) {}

func Test_runWithFallback_StreamSuccess_NoSync(t *testing.T) {
	streamCalls := 0
	syncCalls := 0
	streamFn := func() (anthropic.Message, bool, error) {
		streamCalls++
		return anthropic.Message{StopReason: "end_turn"}, false, nil
	}
	syncFn := func() (anthropic.Message, error) {
		syncCalls++
		return anthropic.Message{}, nil
	}
	msg, err := runWithFallback(context.Background(), streamFn, syncFn, nil)
	if err != nil {
		t.Fatalf("oväntat fel: %v", err)
	}
	if streamCalls != 1 {
		t.Errorf("streamCalls = %d, vill ha 1", streamCalls)
	}
	if syncCalls != 0 {
		t.Errorf("syncCalls = %d, vill ha 0 (sync ska inte anropas när stream lyckas)", syncCalls)
	}
	if msg.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, vill ha end_turn", msg.StopReason)
	}
}

func Test_runWithFallback_StreamFailNoEmit_FallsBackToSync(t *testing.T) {
	streamCalls := 0
	syncCalls := 0
	streamFn := func() (anthropic.Message, bool, error) {
		streamCalls++
		return anthropic.Message{}, false, io.EOF
	}
	syncFn := func() (anthropic.Message, error) {
		syncCalls++
		return anthropic.Message{StopReason: "end_turn"}, nil
	}
	msg, err := runWithFallback(context.Background(), streamFn, syncFn, nil)
	if err != nil {
		t.Fatalf("oväntat fel: %v", err)
	}
	if streamCalls != 1 {
		t.Errorf("streamCalls = %d, vill ha 1", streamCalls)
	}
	if syncCalls != 1 {
		t.Errorf("syncCalls = %d, vill ha 1 (fallback ska anropa sync)", syncCalls)
	}
	if msg.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, vill ha end_turn (från sync-msg)", msg.StopReason)
	}
}

func Test_runWithFallback_StreamFailWithEmit_NoFallback(t *testing.T) {
	streamCalls := 0
	syncCalls := 0
	streamMsg := anthropic.Message{StopReason: "tool_use"}
	streamFn := func() (anthropic.Message, bool, error) {
		streamCalls++
		return streamMsg, true, io.EOF
	}
	syncFn := func() (anthropic.Message, error) {
		syncCalls++
		return anthropic.Message{}, nil
	}
	msg, err := runWithFallback(context.Background(), streamFn, syncFn, nil)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("vill ha EOF (kan inte retrya efter emit), fick %v", err)
	}
	if streamCalls != 1 {
		t.Errorf("streamCalls = %d, vill ha 1", streamCalls)
	}
	if syncCalls != 0 {
		t.Errorf("syncCalls = %d, vill ha 0 (fallback ska INTE anropas efter emit)", syncCalls)
	}
	if msg.StopReason != "tool_use" {
		t.Errorf("StopReason = %q, vill ha tool_use (från stream-msg)", msg.StopReason)
	}
}

func Test_runWithFallback_BothFail_ReturnsLastError(t *testing.T) {
	streamErr := errors.New("stream-err")
	syncErr := errors.New("sync-err")
	streamFn := func() (anthropic.Message, bool, error) {
		return anthropic.Message{}, false, streamErr
	}
	syncFn := func() (anthropic.Message, error) {
		return anthropic.Message{}, syncErr
	}
	_, err := runWithFallback(context.Background(), streamFn, syncFn, nil)
	if !errors.Is(err, syncErr) {
		t.Fatalf("vill ha sync-err (sista felet), fick %v", err)
	}
}

func Test_runWithFallback_LogsStreamFailureAndFallback(t *testing.T) {
	logger := &recordLogger{}
	streamFn := func() (anthropic.Message, bool, error) {
		return anthropic.Message{}, false, io.EOF
	}
	syncFn := func() (anthropic.Message, error) {
		return anthropic.Message{StopReason: "end_turn"}, nil
	}
	_, _ = runWithFallback(context.Background(), streamFn, syncFn, logger)
	found := false
	for _, line := range logger.lines {
		if strings.Contains(line, "faller tillbaka") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ingen loggrad innehåller 'faller tillbaka', loggrader: %v", logger.lines)
	}
}

func Test_runWithFallback_LogsSyncFailure(t *testing.T) {
	logger := &recordLogger{}
	streamFn := func() (anthropic.Message, bool, error) {
		return anthropic.Message{}, false, io.EOF
	}
	syncFn := func() (anthropic.Message, error) {
		return anthropic.Message{}, errors.New("sync-broken")
	}
	_, _ = runWithFallback(context.Background(), streamFn, syncFn, logger)
	found := false
	for _, line := range logger.lines {
		if strings.Contains(line, "sync-fel") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ingen loggrad innehåller 'sync-fel', loggrader: %v", logger.lines)
	}
}

func Test_runWithFallback_RespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	streamCalls := 0
	syncCalls := 0
	streamFn := func() (anthropic.Message, bool, error) {
		streamCalls++
		return anthropic.Message{}, false, nil
	}
	syncFn := func() (anthropic.Message, error) {
		syncCalls++
		return anthropic.Message{}, nil
	}
	_, err := runWithFallback(ctx, streamFn, syncFn, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("vill ha context.Canceled, fick %v", err)
	}
	if streamCalls != 0 {
		t.Errorf("streamFn ska inte anropas när ctx redan är cancellerad, fick %d", streamCalls)
	}
	if syncCalls != 0 {
		t.Errorf("syncFn ska inte anropas när ctx redan är cancellerad, fick %d", syncCalls)
	}
}
