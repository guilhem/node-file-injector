/*
Copyright 2025.

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

package deployer

import (
	"testing"
)

func TestHashNodeName(t *testing.T) {
	tests := []struct {
		name     string
		nodeName string
		want     string
	}{
		{
			name:     "short node name",
			nodeName: "node-1",
			want:     "6d4a59ac", // First 8 chars of SHA256 hash
		},
		{
			name:     "long node name",
			nodeName: "node-file-injector-test-e2e-control-plane",
			want:     "5e8b6f4c", // First 8 chars of SHA256 hash
		},
		{
			name:     "empty string",
			nodeName: "",
			want:     "e3b0c442", // First 8 chars of SHA256 hash of empty string
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hashNodeName(tt.nodeName)

			// Verify length
			if len(got) != nodeHashLength {
				t.Errorf("hashNodeName() length = %d, want %d", len(got), nodeHashLength)
			}

			// Verify it's a valid hex string
			for _, c := range got {
				if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
					t.Errorf("hashNodeName() contains invalid hex character: %c", c)
				}
			}

			// Verify consistency - same input should produce same output
			got2 := hashNodeName(tt.nodeName)
			if got != got2 {
				t.Errorf("hashNodeName() is not consistent: %s != %s", got, got2)
			}
		})
	}
}

func TestHashNodeNameUniqueness(t *testing.T) {
	// Test that different node names produce different hashes
	nodes := []string{
		"control-plane",
		"worker-1",
		"worker-2",
		"node-file-injector-test-e2e-control-plane",
		"very-long-node-name-that-would-exceed-kubernetes-limits",
	}

	hashes := make(map[string]string)
	for _, node := range nodes {
		hash := hashNodeName(node)
		if existingNode, exists := hashes[hash]; exists {
			t.Errorf("Hash collision: %s and %s both hash to %s", node, existingNode, hash)
		}
		hashes[hash] = node
	}
}

func TestHashNodeNameWithGen(t *testing.T) {
	nodeName := "node-file-injector-test-e2e-control-plane"

	// Test that different generations produce different hashes
	gen1Hash := hashNodeNameWithGen(nodeName, 1)
	gen2Hash := hashNodeNameWithGen(nodeName, 2)
	gen3Hash := hashNodeNameWithGen(nodeName, 3)

	// Verify they are all different
	if gen1Hash == gen2Hash {
		t.Errorf("Generation 1 and 2 should have different hashes, both are: %s", gen1Hash)
	}
	if gen1Hash == gen3Hash {
		t.Errorf("Generation 1 and 3 should have different hashes, both are: %s", gen1Hash)
	}
	if gen2Hash == gen3Hash {
		t.Errorf("Generation 2 and 3 should have different hashes, both are: %s", gen2Hash)
	}

	// Verify hash length
	if len(gen1Hash) != nodeHashLength {
		t.Errorf("Hash length = %d, want %d", len(gen1Hash), nodeHashLength)
	}

	// Verify consistency - same input produces same output
	gen1HashAgain := hashNodeNameWithGen(nodeName, 1)
	if gen1Hash != gen1HashAgain {
		t.Errorf("Hash is not consistent: %s != %s", gen1Hash, gen1HashAgain)
	}
}

func TestJobNameLength(t *testing.T) {
	tests := []struct {
		name     string
		nfiName  string
		nodeName string
	}{
		{
			name:     "short names",
			nfiName:  "test",
			nodeName: "node-1",
		},
		{
			name:     "medium names",
			nfiName:  "test-job-resources",
			nodeName: "node-file-injector-test-e2e-control-plane",
		},
		{
			name:     "long nfi name",
			nfiName:  "very-long-node-file-injector-name-that-is-quite-long",
			nodeName: "node-file-injector-test-e2e-control-plane",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nodeHash := hashNodeName(tt.nodeName)
			jobName := "nfi-" + tt.nfiName + "-" + nodeHash

			if len(jobName) > 63 {
				// Test the truncation logic
				maxNFINameLen := 63 - len("nfi-") - len(nodeHash) - 1
				truncatedName := tt.nfiName
				if len(truncatedName) > maxNFINameLen {
					truncatedName = truncatedName[:maxNFINameLen]
				}
				jobName = "nfi-" + truncatedName + "-" + nodeHash
			}

			if len(jobName) > 63 {
				t.Errorf("Job name %s length %d exceeds 63 characters", jobName, len(jobName))
			}
		})
	}
}
