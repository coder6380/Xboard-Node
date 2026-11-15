package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cedar2025/xboard-node/internal/config"
	"github.com/cedar2025/xboard-node/internal/controlplane"
	"github.com/cedar2025/xboard-node/internal/tracker"
)

type samplingKernel struct{ *fakeKernel }

func (k *samplingKernel) GetUserTraffic(context.Context) (map[int][2]int64, map[int]map[string]bool, int, error) {
	return map[int][2]int64{1: {30, 40}}, nil, 0, nil
}

type reportSink struct {
	calls   chan controlplane.ReportPayload
	results chan error
}

func (s *reportSink) SupportsReporting() bool                                 { return true }
func (s *reportSink) SupportsDeviceReports() bool                             { return false }
func (s *reportSink) ReportDevices(controlplane.PushClient, map[int][]string) {}
func (s *reportSink) Report(p controlplane.ReportPayload) error {
	s.calls <- p
	return <-s.results
}

func reportTestService() (*Service, *reportSink) {
	s := newTestService(&fakeKernel{running: true})
	s.tracker = tracker.New()
	s.source = controlplane.NewLocalControlPlane(&config.Config{})
	sink := &reportSink{calls: make(chan controlplane.ReportPayload, 2), results: make(chan error, 2)}
	s.sink = sink
	return s, sink
}

func TestShutdownReportWaitsForFailedAsyncReport(t *testing.T) {
	s, sink := reportTestService()
	s.tracker.Process(map[int][2]int64{1: {10, 20}}, nil, 0)
	s.pushReportAsync()
	first := <-sink.calls
	if first.Traffic[1] != [2]int64{10, 20} {
		t.Fatalf("first traffic = %v", first.Traffic)
	}
	finished := make(chan error, 1)
	go func() { finished <- s.shutdownReport(context.Background()) }()
	select {
	case <-sink.calls:
		t.Fatal("final report started before in-flight report finished")
	case <-finished:
		t.Fatal("shutdown completed before in-flight report finished")
	default:
	}
	sink.results <- errors.New("failed")
	final := <-sink.calls
	if final.Traffic[1] != [2]int64{10, 20} {
		t.Fatalf("final traffic = %v", final.Traffic)
	}
	sink.results <- nil
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

func TestShutdownReportRestoresFailedFinalTraffic(t *testing.T) {
	s, sink := reportTestService()
	s.tracker.Process(map[int][2]int64{1: {10, 20}}, nil, 0)
	sink.results <- errors.New("failed")
	if err := s.shutdownReport(context.Background()); err == nil {
		t.Fatal("expected report error")
	}
	if got := s.tracker.FlushTraffic()[1]; got != [2]int64{10, 20} {
		t.Fatalf("restored traffic = %v", got)
	}
}

func TestShutdownReportSamplesFinalTraffic(t *testing.T) {
	s, sink := reportTestService()
	s.kernel = &samplingKernel{fakeKernel: &fakeKernel{running: true}}
	sink.results <- nil
	if err := s.shutdownReport(context.Background()); err != nil {
		t.Fatal(err)
	}
	payload := <-sink.calls
	if got := payload.Traffic[1]; got != [2]int64{30, 40} {
		t.Fatalf("final traffic = %v", got)
	}
}

func TestShutdownReportTimeoutWhileInFlight(t *testing.T) {
	s, sink := reportTestService()
	s.pushReportAsync()
	<-sink.calls
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.shutdownReport(ctx); err == nil || !strings.Contains(err.Error(), "delivery is unknown") {
		t.Fatalf("error = %v", err)
	}
	done := s.pushDone
	sink.results <- nil
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("report did not finish")
	}
}
