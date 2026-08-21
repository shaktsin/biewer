package cli

import (
	"reflect"
	"testing"
)

func TestRunSetupStepsRunsAllStepsInOrder(t *testing.T) {
	var called []string
	code := runSetupSteps([]setupStep{
		{name: "one", run: func() int { called = append(called, "one"); return 0 }},
		{name: "two", run: func() int { called = append(called, "two"); return 0 }},
		{name: "three", run: func() int { called = append(called, "three"); return 0 }},
	})
	if code != 0 {
		t.Fatalf("expected success, got %d", code)
	}
	if want := []string{"one", "two", "three"}; !reflect.DeepEqual(called, want) {
		t.Fatalf("steps called in wrong order: got %v want %v", called, want)
	}
}

func TestRunSetupStepsContinuesAfterFailure(t *testing.T) {
	var called []string
	code := runSetupSteps([]setupStep{
		{name: "one", run: func() int { called = append(called, "one"); return 1 }},
		{name: "two", run: func() int { called = append(called, "two"); return 0 }},
	})
	if code != 1 {
		t.Fatalf("expected failure, got %d", code)
	}
	if want := []string{"one", "two"}; !reflect.DeepEqual(called, want) {
		t.Fatalf("expected setup to continue after a failed step: got %v want %v", called, want)
	}
}
