package server

import "github.com/mdlayher/vsock"

type TenantInfo struct {
	Cid  uint32
	Port uint32
}

var TenantTable map[string]TenantInfo

var ShadowpodTenantTable map[string]string

func init() {
	TenantTable = make(map[string]TenantInfo)
	TenantTable["test0"] = TenantInfo{Cid: vsock.Host, Port: 1234}
	TenantTable["test1"] = TenantInfo{Cid: 333, Port: 1234}
}

func init() {
	ShadowpodTenantTable = make(map[string]string)
}
