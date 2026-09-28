package settings

import "testing"

func TestNormalizeTextSize(t *testing.T) {
	for in, want := range map[int]int{0: 100, 50: 80, 80: 80, 135: 135, 150: 150, 400: 150} {
		s := Defaults()
		s.TextSize = in
		if got := normalize(s).TextSize; got != want {
			t.Errorf("TextSize %d = %d, want %d", in, got, want)
		}
	}
	if s := normalize(Settings{}); s.HiddenKubeconfigs == nil || s.Kubeconfigs == nil {
		t.Error("normalize must create empty lists, so the frontend gets arrays")
	}
}
