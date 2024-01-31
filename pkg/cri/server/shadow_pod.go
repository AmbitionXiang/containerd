package server

import runtime "k8s.io/cri-api/pkg/apis/runtime/v1"

// use the key to store the shadow pod id
var ShadowPodSet map[string]string

// use the key to store the shadow container id
var ShadowContainerSet map[string]string

// store the shadow pod-container mapping
var ShadowPodContainer map[string][]string

var ShadowPodStatus map[string]*runtime.PodSandboxStatus

func init() {
	ShadowPodSet = make(map[string]string)
	ShadowContainerSet = make(map[string]string)
	ShadowPodContainer = make(map[string][]string)
	ShadowPodStatus = make(map[string]*runtime.PodSandboxStatus)
}
