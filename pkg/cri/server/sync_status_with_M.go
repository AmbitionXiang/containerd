package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net"

	runtime "k8s.io/cri-api/pkg/apis/runtime/v1"
)

const maxByte = 4096

func SyncStatusWithM(tenant TenantInfo, data []byte) (*runtime.PodSandboxStatusResponse, error) {

	fmt.Println("[Extended CRI shim] Calling SendStatus2M. Data: ", string(data))
	// prepare the payload
	request_type := []byte("PodSandboxStatus")
	var request_len uint8 = uint8(len(request_type))
	// add the header (request_len || request_type) before the data
	request := append([]byte{request_len}, request_type...)
	vsock_payload := append(request, data...)
	fmt.Println("[Extended CRI shim] Vsock payload size: ", int(len(vsock_payload)))

	// connect the vsock using the TenantId
	// use a fixed port number
	// conn, err := vsock.Dial(tenant.Cid, 1234, nil)
	// // conn, err := vsock.Dial(tenant.Cid, tenant.Port, nil)
	conn, err := net.Dial("tcp", "192.168.122.66:1234")
	if err != nil {
		return nil, fmt.Errorf("[Extended CRI shim] Failed to connect: %w", err)
	}
	defer conn.Close()

	// sending the PodSandboxStatus request to delegated kubelet
	fmt.Printf("[Extended CRI shim] Sending payload to [Cid: %d, Port: %d (1234, actually)]\n", tenant.Cid, tenant.Port)
	_, err = conn.Write(vsock_payload)
	if err != nil {
		return nil, fmt.Errorf("[Extended CRI shim] Failed to send data via vsock: %w", err)
	}

	// getting and parsing the response
	// TODO: try to avoid setting maxByte
	buf := make([]byte, maxByte)
	n, err := conn.Read(buf)
	if err != nil {
		log.Fatalf("[Extended CRI shim] Failed to read: %v", err)
	}
	fmt.Printf("[Extended CRI shim] Received status from server: %s\n", string(buf[:n]))

	podSandboxStatusResponse := &runtime.PodSandboxStatusResponse{}
	trimmedData := bytes.Trim(buf, "\x00")
	if err := json.Unmarshal(trimmedData, podSandboxStatusResponse); err != nil {
		log.Fatalf("Failed to unmarshal PodSandboxStatus: %v", err)
	}
	shadow_pod_id := podSandboxStatusResponse.Status.Id
	fmt.Println("[Extended CRI shim] Pod sandbox id: ", shadow_pod_id)
	fmt.Println("[Extended CRI shim] State got: ", podSandboxStatusResponse.Status.State)

	// store the status
	ShadowPodStatus[shadow_pod_id] = podSandboxStatusResponse.Status

	// fmt.Println("[Extended CRI shim] Getting container status...")
	// for idx := range podSandboxStatusResponse.ContainersStatuses {
	// 	containerStatus := podSandboxStatusResponse.ContainersStatuses[idx]
	// 	fmt.Println("[Extended CRI shim] Container id: ", containerStatus.Id)
	// 	fmt.Println("[Extended CRI shim] Container state: ", containerStatus.State)
	// }

	return podSandboxStatusResponse, nil
}
