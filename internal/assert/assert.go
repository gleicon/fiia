package assert

func True(condition bool, message string) {
	if !condition {
		panic("assertion failed: " + message)
	}
}
