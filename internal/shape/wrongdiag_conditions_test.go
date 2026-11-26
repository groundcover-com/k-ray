package shape

import "testing"

// Item 4: condition polarity. Node conditions from a node problem detector
// are healthy when False.
var detectorTypes = []string{
	"KernelDeadlock", "ReadonlyFilesystem", "FrequentKubeletRestart", "FrequentDockerRestart",
	"FrequentContainerdRestart", "FrequentUnregisterNetDevice", "CorruptDockerOverlay2", "NTPProblem", "KubeletProblem",
	"ContainerRuntimeProblem", "FilesystemCorruptionProblem", "VMEventScheduled",
}

func nodeWith(status string) string {
	conds := `{"type":"Ready","status":"True"}`
	for _, c := range detectorTypes {
		conds += `,{"type":"` + c + `","status":"` + status + `","reason":"R","message":"m"}`
	}
	return `{"kind":"Node","metadata":{"name":"n"},"status":{"conditions":[` + conds + `]}}`
}

func TestSilentNodeDetectorConditionsAtTheirHealthyStatus(t *testing.T) {
	r := Inspect(obj(t, nodeWith("False")), now)
	if r.State != Healthy || len(r.Problems) != 0 {
		t.Errorf("node: %v %v", r.State, r.Problems)
	}
	for _, c := range detectorTypes {
		if !ConditionHealthy(c, "False", "") || ConditionHealthy(c, "True", "") || ConditionHealthy(c, "Unknown", "") {
			t.Errorf("%s: wrong polarity", c)
		}
	}
}

func TestNodeDetectorConditionTrueIsAProblem(t *testing.T) {
	r := Inspect(obj(t, nodeWith("True")), now)
	if r.State != Unhealthy || len(r.Problems) != len(detectorTypes) {
		t.Errorf("node: %v %v", r.State, r.Problems)
	}
}

// A type that is in no table and matches no word has an unknown polarity: its
// False is a warning that does not claim a fault.
func TestUnknownPolarityFalseIsAWarning(t *testing.T) {
	u := obj(t, `{"apiVersion":"example.com/v1","kind":"Widget","metadata":{"name":"w"},"status":{"conditions":[{"type":"Frobnicated","status":"False","reason":"NotYet"}]}}`)
	r := Inspect(u, now)
	if r.State != Warning || len(r.Problems) != 1 || r.Problems[0] != "condition Frobnicated is False: NotYet" {
		t.Errorf("row: %v %v", r.State, r.Problems)
	}
	// True is still healthy for an unknown type.
	u = obj(t, `{"apiVersion":"example.com/v1","kind":"Widget","metadata":{"name":"w"},"status":{"conditions":[{"type":"Frobnicated","status":"True"}]}}`)
	if r := Inspect(u, now); r.State != Healthy {
		t.Errorf("true: %v", r.State)
	}
}

func TestInformationalConditionNeverSignals(t *testing.T) {
	for _, status := range []string{"True", "False", "Unknown"} {
		u := obj(t, `{"kind":"PersistentVolumeClaim","metadata":{"name":"c"},"status":{"phase":"Bound","conditions":[{"type":"Unused","status":"`+status+`","reason":"PodUsingPVC"}]}}`)
		if r := Inspect(u, now); r.State != Healthy || len(r.Problems) != 0 {
			t.Errorf("Unused=%s: %v %v", status, r.State, r.Problems)
		}
	}
}

// Types that name a problem or are known to be healthy when True keep Unhealthy.
func TestKnownPolarityFalseStaysUnhealthy(t *testing.T) {
	for _, typ := range []string{"Ready", "Available", "ScalingActive", "Synced", "ContainersReady", "Established"} {
		u := obj(t, `{"apiVersion":"example.com/v1","kind":"Widget","metadata":{"name":"w"},"status":{"conditions":[{"type":"`+typ+`","status":"False","reason":"R"}]}}`)
		if r := Inspect(u, now); r.State != Unhealthy {
			t.Errorf("%s False: %v %v", typ, r.State, r.Problems)
		}
	}
	u := obj(t, `{"apiVersion":"example.com/v1","kind":"Widget","metadata":{"name":"w"},"status":{"conditions":[{"type":"Frobnicated","status":"Unknown"}]}}`)
	if r := Inspect(u, now); r.State != Unhealthy {
		t.Errorf("unknown status: %v", r.State)
	}
}
