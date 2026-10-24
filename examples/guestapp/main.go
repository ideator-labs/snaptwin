// Command guestapp is the demo workload. It runs as PID 1 inside a
// microVM and serves HTTP on port 8080.
//
// It makes the classic snapshot mistakes on purpose: an instance ID, a signing
// key and a request ID generator are all created during init, so every clone
// restored from one snapshot starts with the same values. With
// snaptwin.mode=fixed on the kernel command line it also serves POST /run,
// which regenerates them, the way the Lambda MicroVMs /run hook is meant to be
// used.
package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log"
	mrand "math/rand/v2"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
)

type state struct {
	mu         sync.Mutex
	instanceID string
	signingKey []byte
	reqIDs     *mrand.Rand
}

func newState() *state {
	s := &state{}
	s.reset()
	return s
}

func (s *state) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.instanceID = hex.EncodeToString(randomBytes(8))
	s.signingKey = randomBytes(32)
	seed := randomBytes(16)
	s.reqIDs = mrand.New(mrand.NewPCG(binary.LittleEndian.Uint64(seed), binary.LittleEndian.Uint64(seed[8:])))
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

func main() {
	_ = os.MkdirAll("/proc", 0o755)
	if err := syscall.Mount("proc", "/proc", "proc", 0, ""); err != nil {
		log.Printf("mount /proc: %v", err)
	}
	cmdline, _ := os.ReadFile("/proc/cmdline")
	fixed := strings.Contains(string(cmdline), "snaptwin.mode=fixed")

	s := newState()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })
	mux.HandleFunc("GET /instance-id", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		fmt.Fprint(w, s.instanceID)
	})
	mux.HandleFunc("GET /request-id", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		fmt.Fprintf(w, "%016x", s.reqIDs.Uint64())
	})
	mux.HandleFunc("GET /session-token", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		m := hmac.New(sha256.New, s.signingKey)
		m.Write([]byte("user-42"))
		fmt.Fprint(w, hex.EncodeToString(m.Sum(nil))[:32])
	})
	mux.HandleFunc("GET /global-rand", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%016x", mrand.Uint64())
	})
	mux.HandleFunc("GET /fresh-random", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, hex.EncodeToString(randomBytes(16)))
	})
	if fixed {
		mux.HandleFunc("POST /run", func(w http.ResponseWriter, r *http.Request) {
			s.reset()
			fmt.Fprint(w, "reset")
		})
	}

	l, err := net.Listen("tcp", ":8080")
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	log.Printf("guestapp ready mode=%s", map[bool]string{true: "fixed", false: "buggy"}[fixed])
	log.Fatal(http.Serve(l, mux))
}
