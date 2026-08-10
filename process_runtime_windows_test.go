//go:build windows

package main

import "testing"

func TestCodexProcessVisibilityMatchesWPFThreeStateContract(t *testing.T) {
	tests := []struct {
		name       string
		stateKnown bool
		wasVisible bool
		visible    bool
		manual     bool
		want       codexVisibilityAction
	}{
		{"initial absence hides", false, false, false, false, codexVisibilityHide},
		{"initial visible state shows", false, false, true, false, codexVisibilityShow},
		{"restore shows", true, false, true, false, codexVisibilityShow},
		{"minimize hides", true, true, false, false, codexVisibilityHide},
		{"manual hide survives restore", true, false, true, true, codexVisibilityUnchanged},
		{"unchanged absence does nothing", true, false, false, false, codexVisibilityUnchanged},
		{"unchanged visible state does nothing", true, true, true, false, codexVisibilityUnchanged},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := codexProcessVisibilityAction(
				test.stateKnown,
				test.wasVisible,
				test.visible,
				test.manual,
			)
			if got != test.want {
				t.Fatalf("action = %d, want %d", got, test.want)
			}
		})
	}
}
