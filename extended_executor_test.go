package service

import (
	"testing"

	gotracker "github.com/remerge/go-tracker"
	"github.com/stretchr/testify/require"
)

func newTestServiceWithTracker() *testService {
	s := &testService{}
	s.Executor = NewExecutor("test_service", s).WithTracker()
	return s
}

func TestMockTrackerIsNotOverriden(t *testing.T) {
	subject := newTestServiceWithTracker()
	var metadata gotracker.EventMetadata
	subject.Tracker.Tracker = gotracker.NewMockTracker(&metadata)
	go subject.Execute()
	_, ok := <-subject.Ready()
	require.True(t, ok)
	require.NotNil(t, subject.Tracker.Tracker)
	require.IsType(t, &gotracker.MockTracker{}, subject.Tracker.Tracker)
}

func TestTrackerIsIniterOnInit(t *testing.T) {
	subject := newTestServiceWithTracker()
	go subject.Execute()
	_, ok := <-subject.Ready()
	require.True(t, ok)
	require.NotNil(t, subject.Tracker.Tracker)
}
