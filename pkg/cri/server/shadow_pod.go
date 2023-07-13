package server

// use the key to store the shadow pod id
var ShadowPodSet map[string]string

func init() {
	ShadowPodSet = make(map[string]string)
}
