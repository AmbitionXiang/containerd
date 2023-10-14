package server

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/mdlayher/vsock"
)

func Send2M(tenant TenantInfo, data []byte) error {
	fmt.Print("[Extended CRI shim] Calling Send2M\n[Extended CRI shim] Data: ", string(data))
	// get the request type (aka. the function calls this Send2M)
	pc, _, _, _ := runtime.Caller(2)
	caller_name := runtime.FuncForPC(pc).Name()
	parts := strings.Split(caller_name, ".")
	function_name := parts[len(parts)-1]
	fmt.Println("[Extended CRI shim] Request type: ", function_name)
	// prepare the payload
	request_type := []byte(function_name)
	var request_len uint8 = uint8(len(request_type))
	// add the header (request_len || request_type) before the data
	request := append([]byte{request_len}, request_type...)
	vsock_payload := append(request, data...)
	fmt.Println("[Extended CRI shim] Vsock payload size: ", int(len(vsock_payload)))

	// connect the vsock using the TenantId
	// use a fixed port number
	conn, err := vsock.Dial(tenant.Cid, 1234, nil)
	// conn, err := vsock.Dial(tenant.Cid, tenant.Port, nil)
	if err != nil {
		return fmt.Errorf("[Extended CRI shim] Failed to connect: %w", err)
	}
	defer conn.Close()

	// sending the data
	fmt.Printf("[Extended CRI shim] Sending payload to [Cid: %d, Port: %d (1234, actually)]\n", tenant.Cid, tenant.Port)
	_, err = conn.Write(vsock_payload)
	if err != nil {
		return fmt.Errorf("[Extended CRI shim] Failed to send data via vsock: %w", err)
	}
	return nil
}
