package gui

import "testing"

func TestGetMidSectionWeights(t *testing.T) {
	scenarios := []struct {
		name                              string
		sidePanelWidthRatio               float64
		halfScreenModeSidePanelWidthRatio float64
		currentWindow                     string
		screenMode                        WindowMaximisation
		expectedSideSectionWeight         int
		expectedMainSectionWeight         int
	}{
		{
			name:                              "normal screen mode with default width",
			sidePanelWidthRatio:               0.3333,
			halfScreenModeSidePanelWidthRatio: 0.5,
			currentWindow:                     "containers",
			screenMode:                        SCREEN_NORMAL,
			expectedSideSectionWeight:         40,
			expectedMainSectionWeight:         80,
		},
		{
			name:                              "normal screen mode with adjusted width",
			sidePanelWidthRatio:               0.5,
			halfScreenModeSidePanelWidthRatio: 0.5,
			currentWindow:                     "containers",
			screenMode:                        SCREEN_NORMAL,
			expectedSideSectionWeight:         60,
			expectedMainSectionWeight:         60,
		},
		{
			name:                              "half screen mode with default width",
			sidePanelWidthRatio:               0.3333,
			halfScreenModeSidePanelWidthRatio: 0.5,
			currentWindow:                     "containers",
			screenMode:                        SCREEN_HALF,
			expectedSideSectionWeight:         60,
			expectedMainSectionWeight:         60,
		},
		{
			name:                              "half screen mode with adjusted width",
			sidePanelWidthRatio:               0.3333,
			halfScreenModeSidePanelWidthRatio: 0.65,
			currentWindow:                     "containers",
			screenMode:                        SCREEN_HALF,
			expectedSideSectionWeight:         78,
			expectedMainSectionWeight:         42,
		},
		{
			name:                              "full screen mode from a side panel",
			sidePanelWidthRatio:               0.3333,
			halfScreenModeSidePanelWidthRatio: 0.5,
			currentWindow:                     "containers",
			screenMode:                        SCREEN_FULL,
			expectedSideSectionWeight:         40,
			expectedMainSectionWeight:         0,
		},
		{
			name:                              "full screen mode from the main panel",
			sidePanelWidthRatio:               0.3333,
			halfScreenModeSidePanelWidthRatio: 0.5,
			currentWindow:                     "main",
			screenMode:                        SCREEN_FULL,
			expectedSideSectionWeight:         0,
			expectedMainSectionWeight:         1,
		},
	}

	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			sideSectionWeight, mainSectionWeight := getMidSectionWeights(
				s.sidePanelWidthRatio,
				s.halfScreenModeSidePanelWidthRatio,
				s.currentWindow,
				s.screenMode,
			)

			if sideSectionWeight != s.expectedSideSectionWeight {
				t.Errorf("expected side section weight %d, got %d", s.expectedSideSectionWeight, sideSectionWeight)
			}
			if mainSectionWeight != s.expectedMainSectionWeight {
				t.Errorf("expected main section weight %d, got %d", s.expectedMainSectionWeight, mainSectionWeight)
			}
		})
	}
}
