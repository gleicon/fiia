package sdnotify

import (
	"log"
	"net"
	"os"
)

func assert(condition bool, message string) {
	if !condition {
		panic("agent/sdnotify: assertion failed: " + message)
	}
}

// Notify sends a state string to the systemd notification socket.
// No-ops silently if NOTIFY_SOCKET is not set (daemon not managed by systemd).
func Notify(state string) {
	assert(state != "", "state must not be empty")

	socket_path := os.Getenv("NOTIFY_SOCKET")
	if socket_path == "" {
		return
	}

	// Abstract socket names start with '@'; kernel expects a null byte prefix.
	if socket_path[0] == '@' {
		socket_path = "\x00" + socket_path[1:]
	}

	conn, err := net.Dial("unixgram", socket_path)
	if err != nil {
		log.Printf("sdnotify: dial: %v", err)
		return
	}
	defer conn.Close()

	if _, err := conn.Write([]byte(state)); err != nil {
		log.Printf("sdnotify: write: %v", err)
	}
}
