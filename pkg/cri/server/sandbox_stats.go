/*
   Copyright The containerd Authors.

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

package server

import (
	"context"
	"encoding/json"
	"fmt"

	runtime "k8s.io/cri-api/pkg/apis/runtime/v1"
)

func (c *criService) PodSandboxStats(
	ctx context.Context,
	r *runtime.PodSandboxStatsRequest,
) (*runtime.PodSandboxStatsResponse, error) {

	// Start to process the Shadow Pod

	// get the (shadow) pod id from the reqeust
	sandbox_id := r.GetPodSandboxId()
	// forward this PodSandboxStats request if the pod is a shadow pod
	if _, ok := ShadowPodSet[sandbox_id]; ok {
		fmt.Println("[Extended CRI shim] querying a shadow pod's stats ...")
		// get the tenant id from the ShadowpodTenantTable using the sandbox id
		tenant_id := ShadowpodTenantTable[sandbox_id]
		// get the tenant info from the TenantTable
		tenant_info := TenantTable[tenant_id]
		jsonBytes, err := json.Marshal(r)
		if err != nil {
			return nil, fmt.Errorf("[Extended CRI shim] Failed to serialize %w", err)
		}
		// send marshaled requests to the delegated kubelet
		SendReq2M(tenant_info, jsonBytes)
	}
	//TODO: we should get the pod stats from the trusted plane at delegated kubelet
	//TODO: Send2M should return the response from the delegated kubelet

	sandbox, err := c.sandboxStore.Get(r.GetPodSandboxId())
	if err != nil {
		return nil, fmt.Errorf("an error occurred when trying to find sandbox %s: %w", r.GetPodSandboxId(), err)
	}

	podSandboxStats, err := c.podSandboxStats(ctx, sandbox)
	if err != nil {
		return nil, fmt.Errorf("failed to decode pod sandbox metrics %s: %w", r.GetPodSandboxId(), err)
	}

	return &runtime.PodSandboxStatsResponse{Stats: podSandboxStats}, nil
}
