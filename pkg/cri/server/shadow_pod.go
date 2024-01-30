package server

// use the key to store the shadow pod id
var ShadowPodSet map[string]string

// use the key to store the shadow container id
var ShadowContainerSet map[string]string

// store the shadow pod-container mapping
var ShadowPodContainer map[string][]string

func init() {
	ShadowPodSet = make(map[string]string)
	ShadowContainerSet = make(map[string]string)
	ShadowPodContainer = make(map[string][]string)
}
