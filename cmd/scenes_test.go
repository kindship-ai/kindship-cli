package cmd

import (
	"strings"
	"testing"
)

const thothSnippet = `const stageLabels = ["MAP", "RELATE", "DESCEND", "RETURN"];

const sceneTimeline = [
  { label: "Threshold", duration: 240 },
  { label: "Landscape", duration: 240 },
  { label: "Tree", duration: 255 },
  { label: "Correspondence", duration: 270 },
  { label: "Archive", duration: 240 },
  { label: "Return", duration: 195 },
] as const;

const sceneOffsets = sceneTimeline.reduce<number[]>((acc, scene, index) => {
  if (index === 0) {
    acc.push(0);
    return acc;
  }
  acc.push(acc[index - 1] + sceneTimeline[index - 1].duration);
  return acc;
}, []);
`

func TestParseSceneTimeline_ThothPattern(t *testing.T) {
	scenes := parseSceneTimeline(thothSnippet)
	if len(scenes) != 6 {
		t.Fatalf("expected 6 scenes, got %d: %+v", len(scenes), scenes)
	}
	want := []sceneMeta{
		{Name: "Threshold", From: 0, DurationInFrames: 240},
		{Name: "Landscape", From: 240, DurationInFrames: 240},
		{Name: "Tree", From: 480, DurationInFrames: 255},
		{Name: "Correspondence", From: 735, DurationInFrames: 270},
		{Name: "Archive", From: 1005, DurationInFrames: 240},
		{Name: "Return", From: 1245, DurationInFrames: 195},
	}
	for i, w := range want {
		if scenes[i] != w {
			t.Errorf("scene[%d]: want %+v, got %+v", i, w, scenes[i])
		}
	}
}

func TestParseLiteralSequences_NumericOnly(t *testing.T) {
	src := `
		<Sequence from={0} durationInFrames={120}>...</Sequence>
		<Sequence from={120} durationInFrames={180} name="middle">...</Sequence>
		<Sequence from={offset} durationInFrames={dur}>...</Sequence>
		<Sequence from={300} durationInFrames={60}>...</Sequence>
	`
	scenes := parseLiteralSequences(src)
	if len(scenes) != 3 {
		t.Fatalf("expected 3 scenes (skipping computed), got %d: %+v", len(scenes), scenes)
	}
	if scenes[1].Name != "middle" {
		t.Errorf("expected name='middle' on second scene, got %q", scenes[1].Name)
	}
	if scenes[0].DurationInFrames != 120 || scenes[2].From != 300 {
		t.Errorf("frame numbers wrong: %+v", scenes)
	}
}

func TestParseSceneTimeline_WithDurationInFramesField(t *testing.T) {
	src := `const sceneTimeline = [
		{ name: "intro", durationInFrames: 60 },
		{ name: "outro", durationInFrames: 90 }
	] as const;`
	scenes := parseSceneTimeline(src)
	if len(scenes) != 2 {
		t.Fatalf("expected 2 scenes, got %d", len(scenes))
	}
	if scenes[0].DurationInFrames != 60 || scenes[1].From != 60 {
		t.Errorf("scene values wrong: %+v", scenes)
	}
}

func TestParseSceneTimeline_FallbackName(t *testing.T) {
	src := `const SCENES = [
		{ duration: 30 },
		{ duration: 60 }
	];`
	scenes := parseSceneTimeline(src)
	if len(scenes) != 2 {
		t.Fatalf("expected 2 scenes, got %d", len(scenes))
	}
	if !strings.HasPrefix(scenes[0].Name, "scene-") {
		t.Errorf("expected fallback scene-N name, got %q", scenes[0].Name)
	}
}

func TestParseParallelArrays_AkashaPattern(t *testing.T) {
	src := `
		const sceneDurations = [180, 240, 210, 210, 210, 240, 240, 240, 240, 240, 240, 270];
		const sceneStarts = sceneDurations.reduce<number[]>((acc, duration, index) => { ... }, []);
		const scenes = [
			OpeningScene,
			ProblemScene,
			UndividedScene,
			PrinciplesScene,
			StakesScene,
			AkashaScene,
			DocumentsScene,
			WorkScene,
			MethodScene,
			ArchiveScene,
			ForwardScene,
			ClosingScene,
		];
	`
	scenes := parseParallelArrays(src)
	if len(scenes) != 12 {
		t.Fatalf("expected 12 scenes, got %d: %+v", len(scenes), scenes)
	}
	if scenes[0].Name != "Opening" || scenes[0].DurationInFrames != 180 || scenes[0].From != 0 {
		t.Errorf("scene[0]: want Opening/180/0, got %+v", scenes[0])
	}
	if scenes[1].Name != "Problem" || scenes[1].From != 180 {
		t.Errorf("scene[1]: want Problem at from=180, got %+v", scenes[1])
	}
	if scenes[11].Name != "Closing" || scenes[11].DurationInFrames != 270 {
		t.Errorf("scene[11]: want Closing/270, got %+v", scenes[11])
	}
}

func TestParseParallelArrays_LengthMismatch(t *testing.T) {
	src := `
		const sceneDurations = [100, 200, 300];
		const scenes = [A, B, C, D];
	`
	scenes := parseParallelArrays(src)
	if scenes != nil {
		t.Errorf("expected nil when lengths differ, got %+v", scenes)
	}
}

func TestParseSceneTimeline_NoMatch(t *testing.T) {
	src := `const stageLabels = ["MAP", "RELATE"];
		function App() { return <div />; }`
	scenes := parseSceneTimeline(src)
	if scenes != nil {
		t.Errorf("expected nil for non-matching source, got %+v", scenes)
	}
}
