package services

import "testing"

func TestStreamLoopDetectorDetectsRepeatedContent(t *testing.T) {
	detector := streamLoopDetector{}
	phrase := "checking the workspace now "

	for i := 0; i < 3; i++ {
		if detection, ok := detector.observe(streamLoopContent, phrase); ok {
			t.Fatalf("detected loop too early: %#v", detection)
		}
	}

	detection, ok := detector.observe(streamLoopContent, phrase)
	if !ok {
		t.Fatal("expected repeated content to be detected")
	}
	if detection.Kind != streamLoopContent || detection.Repetitions != 4 {
		t.Fatalf("unexpected detection: %#v", detection)
	}
}

func TestStreamLoopDetectorKeepsContentAndThinkingSeparate(t *testing.T) {
	detector := streamLoopDetector{}
	phrase := "checking the workspace now "

	for i := 0; i < 3; i++ {
		if detection, ok := detector.observe(streamLoopReasoning, phrase); ok {
			t.Fatalf("detected loop too early: %#v", detection)
		}
	}
	if detection, ok := detector.observe(streamLoopContent, phrase); ok {
		t.Fatalf("content should not inherit thinking repetitions: %#v", detection)
	}

	detection, ok := detector.observe(streamLoopReasoning, phrase)
	if !ok {
		t.Fatal("expected repeated thinking to be detected")
	}
	if detection.Kind != streamLoopReasoning {
		t.Fatalf("expected thinking detection, got %#v", detection)
	}
}
