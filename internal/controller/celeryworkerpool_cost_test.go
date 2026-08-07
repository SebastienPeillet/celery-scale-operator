/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import "testing"

func TestDeletionCostFor(t *testing.T) {
	busyPodName := "worker-busy"
	idlePodName := "worker-idle"
	tests := []struct {
		name     string
		podName  string
		activity map[string]int32
		wantCost int32
	}{
		{
			name:     "pod with active tasks returns its count",
			podName:  busyPodName,
			activity: map[string]int32{busyPodName: 3},
			wantCost: 3,
		},
		{
			name:     "pod absent from activity map is idle",
			podName:  idlePodName,
			activity: map[string]int32{busyPodName: 3},
			wantCost: 0,
		},
		{
			name:     "empty activity map",
			podName:  idlePodName,
			activity: map[string]int32{},
			wantCost: 0,
		},
		{
			name:     "nil activity map",
			podName:  idlePodName,
			activity: nil,
			wantCost: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deletionCostFor(tt.podName, tt.activity)
			if got != tt.wantCost {
				t.Errorf("deletionCostFor(%q, %v) = %d, want %d", tt.podName, tt.activity, got, tt.wantCost)
			}
		})
	}
}
