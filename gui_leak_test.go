package main

import (
	"os"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
)

// procCounts reports this process's open file descriptors, its live child
// processes, and children that exited without being reaped.
func procCounts(t *testing.T) (fds, children, zombies int) {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	fds = len(ents)
	procs, _ := os.ReadDir("/proc")
	self := os.Getpid()
	for _, p := range procs {
		data, err := os.ReadFile("/proc/" + p.Name() + "/stat")
		if err != nil {
			continue
		}
		// pid (comm) state ppid ...: comm may hold spaces, so split after ')'.
		i := strings.LastIndexByte(string(data), ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(string(data[i+1:]))
		if len(f) < 2 || f[1] != strconv.Itoa(self) {
			continue
		}
		if f[0] == "Z" {
			zombies++
		} else {
			children++
		}
	}
	return fds, children, zombies
}

func heapAlloc() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// Opening and closing tabs and split panes with output in them gives back
// every goroutine, file descriptor, child process, and heap byte, leaves no
// zombies. (Idle CPU is measured on a real run: this harness polls the main
// loop every millisecond.)
func TestGUI_NoLeaks(t *testing.T) {
	w := newTestWin(t, "exec sleep 60", nil)
	cycle := func() {
		w.key(gdk.KEY_T, ctrl|shift)
		w.key(gdk.KEY_F1, 0)
		w.key(gdk.KEY_F1, 0)
		w.waitPanes(3)
		w.typeText("seq 1 20000")
		w.key(gdk.KEY_Return, 0)
		settle(150 * time.Millisecond)
		w.key(gdk.KEY_W, ctrl|shift)
		onMain(func() {
			if len(w.tabs) != 1 {
				t.Fatalf("%d tabs after closing, want 1", len(w.tabs))
			}
		})
	}
	cycle() // warm up: caches, fonts, one-time goroutines
	settle(500 * time.Millisecond)
	baseG := runtime.NumGoroutine()
	baseFD, baseKids, _ := procCounts(t)
	baseHeap := heapAlloc()

	const rounds = 15
	for range rounds {
		cycle()
	}

	deadline := time.Now().Add(5 * time.Second)
	var g, fds, kids, zombies int
	for {
		g = runtime.NumGoroutine()
		fds, kids, zombies = procCounts(t)
		if (g <= baseG+2 && fds <= baseFD+2 && kids <= baseKids && zombies == 0) || time.Now().After(deadline) {
			break
		}
		settle(100 * time.Millisecond)
	}
	if g > baseG+2 {
		t.Errorf("goroutines: %d after %d tab cycles, %d before", g, rounds, baseG)
	}
	if fds > baseFD+2 {
		t.Errorf("file descriptors: %d after %d tab cycles, %d before", fds, rounds, baseFD)
	}
	if kids > baseKids || zombies > 0 {
		t.Errorf("child processes: %d live (%d before), %d zombies", kids, baseKids, zombies)
	}
	if path := os.Getenv("BUNKER_LEAK_HEAPPROFILE"); path != "" {
		heapAlloc()
		if f, err := os.Create(path); err == nil {
			pprof.WriteHeapProfile(f) //nolint:errcheck // debugging aid
			f.Close()                 //nolint:errcheck // debugging aid
		}
	}
	if heap := heapAlloc(); heap > baseHeap+16<<20 {
		t.Errorf("heap grew from %d MiB to %d MiB over %d tab cycles", baseHeap>>20, heap>>20, rounds)
	}
}
